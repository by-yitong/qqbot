package v1

// 本地 fork 补充的富媒体上传协议测试：经真实 openAPI 实例打到 httptest 假 server
// （覆盖 constant.APIDomain 指向本机）。

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/oauth2"

	"qqbot/pkg/botgo/constant"
)

// testAPI 返回指向 srv 的 openAPI 实例（静态 token）。
// constant.APIDomain 是全局变量，勿与 t.Parallel 混用。
func testAPI(t *testing.T, srv *httptest.Server) *openAPI {
	t.Helper()
	old := constant.APIDomain
	constant.APIDomain = srv.URL
	t.Cleanup(func() { constant.APIDomain = old })
	return (&openAPI{}).Setup("APP", oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: "tok", TokenType: "QQBot",
	}), false).(*openAPI)
}

func TestUploadRichMediaURLMode(t *testing.T) {
	var gotPath, gotAuth, gotCT string
	var bodyBytes []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		bodyBytes, _ = io.ReadAll(r.Body)
		fmt.Fprint(w, `{"file_uuid":"u1","file_info":"THEINFO","ttl":300}`)
	}))
	t.Cleanup(srv.Close)

	api := testAPI(t, srv)
	f, err := api.UploadGroupRichMedia(context.Background(), "GID", 1, "https://img.example.com/a.png")
	if err != nil {
		t.Fatalf("UploadGroupRichMedia: %v", err)
	}
	if f.FileInfo != "THEINFO" || f.FileUUID != "u1" || f.TTL != 300 {
		t.Errorf("RichMediaFile=%+v", f)
	}
	if !strings.HasSuffix(gotPath, "/v2/groups/GID/files") {
		t.Errorf("path=%q", gotPath)
	}
	if gotAuth != "QQBot tok" {
		t.Errorf("Authorization=%q want QQBot tok", gotAuth)
	}
	if !strings.HasPrefix(gotCT, "application/json") {
		t.Errorf("Content-Type 应为 application/json: %q", gotCT)
	}
	// 关键：URL 模式请求体是 JSON {file_type, url, srv_send_msg}，不是 multipart。
	var req struct {
		FileType   int    `json:"file_type"`
		URL        string `json:"url"`
		SrvSendMsg bool   `json:"srv_send_msg"`
	}
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		t.Fatalf("请求体应为合法 JSON: %v body=%q", err, bodyBytes)
	}
	if req.FileType != 1 || req.URL != "https://img.example.com/a.png" || req.SrvSendMsg {
		t.Errorf("请求体字段错: %+v", req)
	}
}

func TestUploadRichMediaUserScope(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"file_info":"INFO2"}`)
	}))
	t.Cleanup(srv.Close)

	api := testAPI(t, srv)
	if _, err := api.UploadUserRichMedia(context.Background(), "UID", 1, "https://x/b.jpg"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gotPath, "/v2/users/UID/files") {
		t.Errorf("C2C path=%q", gotPath)
	}
}

func TestUploadRichMediaHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"code":850026,"message":"富媒体文件下载失败"}`)
	}))
	t.Cleanup(srv.Close)

	api := testAPI(t, srv)
	if _, err := api.UploadGroupRichMedia(context.Background(), "G", 1, "https://x/a.png"); err == nil {
		t.Fatal("HTTP 400 应报错")
	}
}

func TestUploadRichMediaMissingFileInfo(t *testing.T) {
	// 响应只有 file_uuid（无 file_info）→ 兜底用 file_uuid。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"file_uuid":"ONLYUUID"}`)
	}))
	t.Cleanup(srv.Close)

	api := testAPI(t, srv)
	f, err := api.UploadGroupRichMedia(context.Background(), "G", 1, "https://x/a.png")
	if err != nil {
		t.Fatal(err)
	}
	if f.FileInfo != "ONLYUUID" {
		t.Errorf("无 file_info 应兜底 file_uuid, got %q", f.FileInfo)
	}
}

// TestUploadFileChunked 分片直传全流程：mock upload_prepare（校验 md5/sha1/md5_10m
// 与 file_size）、预签名 PUT（回收分片）、upload_part_finish（校验分片 md5）、
// /files 合并（校验 upload_id），最后比对分片重组内容与原始字节。
func TestUploadFileChunked(t *testing.T) {
	data := []byte("0123456789abcdefghijQRSTUVWXYZ") // 30 字节，切成 5+5+20 三片
	var mu sync.Mutex
	chunks := map[int][]byte{}
	mergeDone := false

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/upload_prepare"):
			var req struct {
				FileType int    `json:"file_type"`
				FileName string `json:"file_name"`
				FileSize string `json:"file_size"`
				MD5      string `json:"md5"`
				SHA1     string `json:"sha1"`
				MD510m   string `json:"md5_10m"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("prepare 请求体非 JSON: %v", err)
			}
			if req.FileType != 4 || req.FileName != "报表.bin" || req.FileSize != "30" {
				t.Errorf("prepare 字段错: %+v", req)
			}
			md5Full := fmt.Sprintf("%x", md5.Sum(data))
			sha1Full := fmt.Sprintf("%x", sha1.Sum(data))
			if req.MD5 != md5Full || req.SHA1 != sha1Full || req.MD510m != md5Full { // <10MB 时 md5_10m == 全量 md5
				t.Errorf("校验值错: got md5=%s sha1=%s md5_10m=%s", req.MD5, req.SHA1, req.MD510m)
			}
			// 注意 block_size 在本实现里是 int；官方文档为字符串，两版都兼容。
			fmt.Fprint(w, fmt.Sprintf(`{"upload_id":"UP1","block_size":"5","parts":[
				{"index":0,"presigned_url":%q,"block_size":5},
				{"index":1,"presigned_url":%q,"block_size":5},
				{"index":2,"presigned_url":%q,"block_size":20}]}`,
				srv.URL+"/chunk/0", srv.URL+"/chunk/1", srv.URL+"/chunk/2"))
		case strings.HasPrefix(r.URL.Path, "/chunk/"):
			idx, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/chunk/"))
			if r.Method != http.MethodPut {
				t.Errorf("分片应 PUT, got %s", r.Method)
			}
			mu.Lock()
			chunks[idx] = append([]byte(nil), body...)
			mu.Unlock()
		case strings.HasSuffix(r.URL.Path, "/upload_part_finish"):
			var req struct {
				UploadID  string `json:"upload_id"`
				PartIndex int    `json:"part_index"`
				BlockSize string `json:"block_size"`
				MD5       string `json:"md5"`
			}
			_ = json.Unmarshal(body, &req)
			if req.UploadID != "UP1" {
				t.Errorf("part_finish upload_id=%q", req.UploadID)
			}
			mu.Lock()
			part := chunks[req.PartIndex]
			mu.Unlock()
			if part == nil {
				t.Errorf("part_finish 在 PUT 之前到达: index=%d", req.PartIndex)
			}
			if req.MD5 != fmt.Sprintf("%x", md5.Sum(part)) || req.BlockSize != strconv.Itoa(len(part)) {
				t.Errorf("分片 %d 完成通知校验错: size=%s md5=%s", req.PartIndex, req.BlockSize, req.MD5)
			}
		case strings.HasSuffix(r.URL.Path, "/files"):
			// 合并：upload_id 模式（无 url）
			var req struct {
				FileType int    `json:"file_type"`
				URL      string `json:"url"`
				UploadID string `json:"upload_id"`
				FileName string `json:"file_name"`
			}
			_ = json.Unmarshal(body, &req)
			if req.UploadID != "UP1" || req.URL != "" || req.FileType != 4 || req.FileName != "报表.bin" {
				t.Errorf("合并请求字段错: %+v", req)
			}
			mergeDone = true
			fmt.Fprint(w, `{"file_uuid":"FU","file_info":"CHUNKINFO","ttl":0}`)
		default:
			t.Errorf("意外请求: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	api := testAPI(t, srv)
	f, err := api.UploadGroupFileChunked(context.Background(), "GID", 4, "报表.bin", data)
	if err != nil {
		t.Fatalf("UploadGroupFileChunked: %v", err)
	}
	if f.FileInfo != "CHUNKINFO" {
		t.Errorf("file_info=%q want CHUNKINFO", f.FileInfo)
	}
	if !mergeDone || len(chunks) != 3 {
		t.Fatalf("分片流程不完整: merge=%v chunks=%v", mergeDone, chunks)
	}
	var reassembled []byte
	for i := 0; i < len(chunks); i++ {
		reassembled = append(reassembled, chunks[i]...)
	}
	if !bytes.Equal(reassembled, data) {
		t.Errorf("分片重组不等于原文件: %q", reassembled)
	}
}

// TestUploadFileChunkedUserScope 私聊分片直传走 /v2/users/* 端点。
func TestUploadFileChunkedUserScope(t *testing.T) {
	var userScopeHit bool
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/v2/users/U1/upload_prepare") {
			userScopeHit = true
			fmt.Fprint(w, `{"upload_id":"UP2","parts":[{"index":0,"presigned_url":`+strconv.Quote(srv.URL+"/c0")+`,"block_size":4}]}`)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/c0") {
			return // 分片 PUT
		}
		if strings.HasSuffix(r.URL.Path, "/files") {
			fmt.Fprint(w, `{"file_info":"UINFO"}`)
			return
		}
	}))
	t.Cleanup(srv.Close)

	api := testAPI(t, srv)
	f, err := api.UploadUserFileChunked(context.Background(), "U1", 4, "a.bin", []byte("abcd"))
	if err != nil {
		t.Fatalf("UploadUserFileChunked: %v", err)
	}
	if f.FileInfo != "UINFO" || !userScopeHit {
		t.Errorf("user scope: info=%q hit=%v", f.FileInfo, userScopeHit)
	}
}

// TestUploadFileChunkedServerError prepare 阶段 HTTP 错误应报错。
func TestUploadFileChunkedServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"code":40093001,"message":"文件上传失败，请重试"}`)
	}))
	t.Cleanup(srv.Close)

	api := testAPI(t, srv)
	if _, err := api.UploadGroupFileChunked(context.Background(), "G", 4, "a.bin", []byte("x")); err == nil ||
		!strings.Contains(err.Error(), "upload_prepare") {
		t.Fatalf("prepare 失败应报错: %v", err)
	}
}
