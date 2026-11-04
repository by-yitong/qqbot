package v1

// 素材管理协议测试：经真实 openAPI 实例打到 httptest 假 server（覆盖
// constant.APIDomain 指向本机），复用 richmedia_test.go 的 testAPI 助手。

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"qqbot/pkg/botgo/dto"
)

// TestUploadMediaImage 一步式全流程：prepare 校验请求体（文件名/大小/类型/摘要）→
// 预签名 PUT（校验方法、Content-Type、字节一致；回 ETag）→ complete（校验 upload_id
// 与去引号的 ETag）→ 返回 qqbot-media:// 引用。
func TestUploadMediaImage(t *testing.T) {
	data := []byte("PNGDATA-fake-bytes")
	var mu sync.Mutex
	putBody := []byte(nil)
	putCT := ""

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/v2/media-images/prepare"):
			var req dto.MediaImagePrepareReq
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("prepare 请求体非 JSON: %v", err)
			}
			md5Full := fmt.Sprintf("%x", md5.Sum(data))
			sha1Full := fmt.Sprintf("%x", sha1.Sum(data))
			if req.FileName != "k.png" || req.FileSize != uint64(len(data)) ||
				req.ContentType != "image/png" || req.MD5 != md5Full || req.SHA1 != sha1Full {
				t.Errorf("prepare 字段错: %+v", req)
			}
			fmt.Fprintf(w, `{"upload_id":"UP1","presigned_url":%q,"expires_in":3600}`, srv.URL+"/put")
		case r.URL.Path == "/put":
			if r.Method != http.MethodPut {
				t.Errorf("直传应 PUT, got %s", r.Method)
			}
			mu.Lock()
			putBody = append([]byte(nil), body...)
			putCT = r.Header.Get("Content-Type")
			mu.Unlock()
			w.Header().Set("ETag", `"`+fmt.Sprintf("%x", md5.Sum(data))+`"`)
		case strings.HasSuffix(r.URL.Path, "/v2/media-images/complete"):
			var req dto.MediaImageCompleteReq
			_ = json.Unmarshal(body, &req)
			if req.UploadID != "UP1" || req.MD5 != fmt.Sprintf("%x", md5.Sum(data)) {
				t.Errorf("complete 字段错（须为去引号 ETag）: %+v", req)
			}
			fmt.Fprint(w, `{"image_id":"IMG1","url":"qqbot-media://image/v1/TOKEN","ttl":7776000,"preview_url":"https://pv"}`)
		default:
			t.Errorf("意外请求: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	api := testAPI(t, srv)
	img, err := api.UploadMediaImage(context.Background(), "k.png", "image/png", data)
	if err != nil {
		t.Fatalf("UploadMediaImage: %v", err)
	}
	if img.ImageID != "IMG1" || img.URL != "qqbot-media://image/v1/TOKEN" || img.TTL != 7776000 {
		t.Errorf("MediaImage=%+v", img)
	}
	if string(putBody) != string(data) || putCT != "image/png" {
		t.Errorf("直传字节/Content-Type 错: ct=%q len=%d", putCT, len(putBody))
	}
}

// TestUploadMediaImageValidation 客户端侧校验：空内容、超 10MB、非法 content_type、
// 扩展名不一致——都不应发出任何 HTTP 请求。
func TestUploadMediaImageValidation(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
	}))
	t.Cleanup(srv.Close)

	api := testAPI(t, srv)
	oversize := make([]byte, maxMediaImageBytes+1)
	cases := []struct {
		name        string
		fileName    string
		contentType string
		data        []byte
	}{
		{"空内容", "a.png", "image/png", nil},
		{"超10MB", "a.png", "image/png", oversize},
		{"非法类型", "a.png", "image/bmp", []byte("x")},
		{"扩展名不符", "a.bmp", "image/png", []byte("x")},
		{"无扩展名", "png", "image/png", []byte("x")},
	}
	for _, c := range cases {
		if _, err := api.UploadMediaImage(context.Background(), c.fileName, c.contentType, c.data); err == nil {
			t.Errorf("%s: 应报错", c.name)
		}
	}
	if calls != 0 {
		t.Errorf("校验失败不应发请求, got %d 次", calls)
	}
}

// jpeg 扩展名允许 .jpg 与 .jpeg 两种，均应走通全流程。
func TestUploadMediaImageJPEGExt(t *testing.T) {
	data := []byte("jpeg-bytes")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/v2/media-images/prepare"):
			fmt.Fprintf(w, `{"upload_id":"U","presigned_url":%q,"expires_in":60}`, srv.URL+"/p")
		case r.URL.Path == "/put" || r.URL.Path == "/p":
			w.Header().Set("ETag", `"`+fmt.Sprintf("%x", md5.Sum(data))+`"`)
		case strings.HasSuffix(r.URL.Path, "/v2/media-images/complete"):
			fmt.Fprint(w, `{"image_id":"J1","url":"qqbot-media://image/v1/J","ttl":7776000}`)
		default:
			t.Errorf("意外请求: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	api := testAPI(t, srv)
	for _, name := range []string{"a.jpg", "a.jpeg"} {
		if _, err := api.UploadMediaImage(context.Background(), name, "image/jpeg", data); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestPrepareMediaImageBadRsp prepare 响应缺 upload_id 应报错。
func TestPrepareMediaImageBadRsp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(srv.Close)

	api := testAPI(t, srv)
	if _, err := api.PrepareMediaImage(context.Background(), dto.MediaImagePrepareReq{}); err == nil ||
		!strings.Contains(err.Error(), "upload_id") {
		t.Fatalf("缺 upload_id 应报错: %v", err)
	}
}

// TestListMediaImages cursor 透传与响应解析。
func TestListMediaImages(t *testing.T) {
	var gotCursor string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		gotCursor = r.URL.Query().Get("cursor")
		fmt.Fprint(w, `{"images":[{"image_id":"I1","url":"qqbot-media://image/v1/A","file_name":"a.png","created_at":1,"expires_at":2}],"next_cursor":"","total":1}`)
	}))
	t.Cleanup(srv.Close)

	api := testAPI(t, srv)
	rsp, err := api.ListMediaImages(context.Background(), "CUR1")
	if err != nil {
		t.Fatal(err)
	}
	if gotCursor != "CUR1" {
		t.Errorf("cursor=%q", gotCursor)
	}
	if rsp.Total != 1 || len(rsp.Images) != 1 || rsp.Images[0].FileName != "a.png" {
		t.Errorf("ListRsp=%+v", rsp)
	}
}

// TestDeleteMediaImage 走 DELETE /v2/media-images/{image_id}/delete。
func TestDeleteMediaImage(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		gotMethod, gotPath = r.Method, r.URL.Path
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(srv.Close)

	api := testAPI(t, srv)
	if err := api.DeleteMediaImage(context.Background(), "IMG9"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/v2/media-images/IMG9/delete" {
		t.Errorf("delete: %s %s", gotMethod, gotPath)
	}
}
