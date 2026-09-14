package bot

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/oauth2"

	"qqbot/internal/imgstore"
)

// fakeTS 假 token source（测试用）。
type fakeTS struct{ tok string }

func (f *fakeTS) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: f.tok, TokenType: "QQBot"}, nil
}

func TestUploadRichMediaURLMode(t *testing.T) {
	var gotPath, gotAuth, gotCT string
	var bodyBytes []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		bodyBytes, _ = io.ReadAll(r.Body)
		fmt.Fprint(w, `{"file_uuid":"u1","file_info":"THEINFO","ttl":300}`)
	}))
	defer srv.Close()
	old := qqAPIBase
	qqAPIBase = srv.URL
	defer func() { qqAPIBase = old }()

	b := &Bot{tokenSource: &fakeTS{tok: "tok"}, appID: "APP"}
	info, err := b.uploadRichMedia(context.Background(), true, "GID", 1, "https://img.example.com/a.png")
	if err != nil {
		t.Fatalf("uploadRichMedia: %v", err)
	}
	if info != "THEINFO" {
		t.Errorf("file_info=%q want THEINFO", info)
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
	if req.FileType != 1 || req.URL != "https://img.example.com/a.png" || req.SrvSendMsg != false {
		t.Errorf("请求体字段错: %+v", req)
	}
}

func TestUploadRichMediaC2C(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"file_info":"INFO2"}`)
	}))
	defer srv.Close()
	old := qqAPIBase
	qqAPIBase = srv.URL
	defer func() { qqAPIBase = old }()

	b := &Bot{tokenSource: &fakeTS{tok: "t"}, appID: "A"}
	if _, err := b.uploadRichMedia(context.Background(), false, "UID", 1, "https://x/b.jpg"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gotPath, "/v2/users/UID/files") {
		t.Errorf("C2C path=%q", gotPath)
	}
}

func TestUploadRichMediaHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"code":850026,"message":"富媒体文件下载失败"}`)
	}))
	defer srv.Close()
	old := qqAPIBase
	qqAPIBase = srv.URL
	defer func() { qqAPIBase = old }()

	b := &Bot{tokenSource: &fakeTS{tok: "t"}, appID: "A"}
	if _, err := b.uploadRichMedia(context.Background(), true, "G", 1, "https://x/a.png"); err == nil {
		t.Fatal("HTTP 400 应报错")
	}
}

func TestUploadRichMediaMissingFileInfo(t *testing.T) {
	// 响应只有 file_uuid（无 file_info）→ 兜底用 file_uuid。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"file_uuid":"ONLYUUID"}`)
	}))
	defer srv.Close()
	old := qqAPIBase
	qqAPIBase = srv.URL
	defer func() { qqAPIBase = old }()

	b := &Bot{tokenSource: &fakeTS{tok: "t"}, appID: "A"}
	info, err := b.uploadRichMedia(context.Background(), true, "G", 1, "https://x/a.png")
	if err != nil {
		t.Fatal(err)
	}
	if info != "ONLYUUID" {
		t.Errorf("无 file_info 应兜底 file_uuid，got %q", info)
	}
}

func TestUploadRichMediaNoToken(t *testing.T) {
	b := &Bot{} // 无 tokenSource
	if _, err := b.uploadRichMedia(context.Background(), true, "G", 1, "https://x/a.png"); err == nil {
		t.Fatal("无 tokenSource 应报错")
	}
}

// TestUploadFileChunked 分片上传全流程：mock upload_prepare（校验 md5/sha1/md5_10m
// 与 file_size）、预签名 PUT（回收分片）、upload_part_finish（校验分片 md5）、
// /files 合并（校验 upload_id），最后比对分片重组内容与原始字节。
func TestUploadFileChunked(t *testing.T) {
	data := []byte("0123456789abcdefghijQRSTUVWXYZ") // 30 字节，切成 5+5+20 三片
	var mu sync.Mutex
	chunks := map[int][]byte{}
	mergeDone := false

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			fmt.Fprint(w, fmt.Sprintf(`{"upload_id":"UP1","block_size":"5","parts":[
				{"index":0,"presigned_url":%q,"block_size":"5"},
				{"index":1,"presigned_url":%q,"block_size":"5"},
				{"index":2,"presigned_url":%q,"block_size":"20"}]}`,
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
	defer srv.Close()
	old := qqAPIBase
	qqAPIBase = srv.URL
	defer func() { qqAPIBase = old }()

	b := &Bot{tokenSource: &fakeTS{tok: "t"}, appID: "A"}
	info, err := b.uploadFileChunked(context.Background(), true, "GID", 4, "报表.bin", data)
	if err != nil {
		t.Fatalf("uploadFileChunked: %v", err)
	}
	if info != "CHUNKINFO" {
		t.Errorf("file_info=%q want CHUNKINFO", info)
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

// TestUploadFileChunkedServerError prepare 阶段 HTTP 错误应带状态码返回。
func TestUploadFileChunkedServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"code":40093001,"message":"文件上传失败，请重试"}`)
	}))
	defer srv.Close()
	old := qqAPIBase
	qqAPIBase = srv.URL
	defer func() { qqAPIBase = old }()

	b := &Bot{tokenSource: &fakeTS{tok: "t"}, appID: "A"}
	if _, err := b.uploadFileChunked(context.Background(), true, "G", 4, "a.bin", []byte("x")); err == nil ||
		!strings.Contains(err.Error(), "upload_prepare") {
		t.Fatalf("prepare 失败应报错: %v", err)
	}
}

// TestFetchFile 代下载：内容一致、文件名取 URL 末段、无扩展名兜底 file.bin。
func TestFetchFile(t *testing.T) {
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("APKBYTES"))
	}))
	defer src.Close()

	data, name, err := fetchFile(context.Background(), src.URL+"/GoStudio-v1.0.12.apk?token=1")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "APKBYTES" || name != "GoStudio-v1.0.12.apk" {
		t.Errorf("fetchFile: name=%q data=%q", name, data)
	}
	if _, name, err := fetchFile(context.Background(), src.URL+"/"); err != nil || name != "file.bin" {
		t.Errorf("无文件名应兜底 file.bin: %q %v", name, err)
	}
}

func TestSendGroupVideo(t *testing.T) {
	var gotFileType int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/files") {
			var req struct {
				FileType int    `json:"file_type"`
				URL      string `json:"url"`
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &req)
			gotFileType = req.FileType
			fmt.Fprint(w, `{"file_info":"VINFO"}`)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	old := qqAPIBase
	qqAPIBase = srv.URL
	defer func() { qqAPIBase = old }()

	b := &Bot{tokenSource: &fakeTS{tok: "t"}, appID: "A"}
	info, err := b.uploadRichMedia(context.Background(), true, "G", 2, "https://x/v.mp4")
	if err != nil || info != "VINFO" {
		t.Fatalf("视频上传: %v %q", err, info)
	}
	if gotFileType != 2 {
		t.Errorf("视频 file_type 应为 2, got %d", gotFileType)
	}
}

func TestSendGroupVoice(t *testing.T) {
	var gotFileType int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/files") {
			var req struct {
				FileType int    `json:"file_type"`
				URL      string `json:"url"`
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &req)
			gotFileType = req.FileType
			fmt.Fprint(w, `{"file_info":"SINFO"}`)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	old := qqAPIBase
	qqAPIBase = srv.URL
	defer func() { qqAPIBase = old }()

	b := &Bot{tokenSource: &fakeTS{tok: "t"}, appID: "A"}
	info, err := b.uploadRichMedia(context.Background(), true, "G", 3, "https://x/a.silk")
	if err != nil || info != "SINFO" {
		t.Fatalf("语音上传: %v %q", err, info)
	}
	if gotFileType != 3 {
		t.Errorf("语音 file_type 应为 3, got %d", gotFileType)
	}
}

func TestEnsureSilkURLTranscode(t *testing.T) {
	// 源音频：一个 httptest 服务器上的 wav。
	wav := buildTestWAV()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		w.Write(wav)
	}))
	defer src.Close()

	// 图床：本地直存到临时目录，BaseURL 随便（本地模式不回源请求）。
	dir := t.TempDir()
	b := &Bot{mediaHost: &imgstore.Host{BaseURL: "http://cdn.example.com", Dir: dir}}

	url, err := b.ensureSilkURL(context.Background(), src.URL+"/voice.wav")
	if err != nil {
		t.Fatalf("ensureSilkURL: %v", err)
	}
	if !strings.HasPrefix(url, "http://cdn.example.com/") || !strings.HasSuffix(url, ".silk") {
		t.Fatalf("应返回图床 .silk URL, got %q", url)
	}
	// 直存文件应为 QQ 头（0x02 开头）的 silk。
	files, _ := filepath.Glob(filepath.Join(dir, "*.silk"))
	if len(files) != 1 {
		t.Fatalf("直存目录应有 1 个 silk, got %v", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 2 || data[0] != 0x02 {
		t.Fatalf("直存文件应以 0x02 开头, got % X", data[:min(4, len(data))])
	}
}

func TestEnsureSilkURLPassthroughAndErrors(t *testing.T) {
	b := &Bot{mediaHost: &imgstore.Host{BaseURL: "http://cdn.example.com", Dir: t.TempDir()}}

	// .silk 后缀（含大写、带查询串）直接透传，不发起下载。
	got, err := b.ensureSilkURL(context.Background(), "https://x/a.SILK?sign=1")
	if err != nil || got != "https://x/a.SILK?sign=1" {
		t.Fatalf("silk 透传: %q %v", got, err)
	}

	// 未配图床 → 明确报错。
	bare := &Bot{}
	if _, err := bare.ensureSilkURL(context.Background(), "https://x/a.mp3"); err == nil {
		t.Fatal("无图床应报错")
	}

	// 源不可达 → 下载失败错误。
	if _, err := b.ensureSilkURL(context.Background(), "http://127.0.0.1:1/a.mp3"); err == nil {
		t.Fatal("下载失败应报错")
	}
}

// buildTestWAV 生成 0.2s 22050Hz 单声道静音偏置正弦 wav（bot 层测试用）。
func buildTestWAV() []byte {
	const rate, secs = 22050, 1
	n := rate * secs
	data := make([]byte, n*2)
	for i := 0; i < n; i++ {
		v := int16(3000 * math.Sin(2*math.Pi*440*float64(i)/rate))
		binary.LittleEndian.PutUint16(data[2*i:], uint16(v))
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint16(1)) // mono
	binary.Write(&b, binary.LittleEndian, uint32(rate))
	binary.Write(&b, binary.LittleEndian, uint32(rate*2))
	binary.Write(&b, binary.LittleEndian, uint16(2))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	return b.Bytes()
}
