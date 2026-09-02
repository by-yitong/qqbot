package imgbed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newSrv 起一个记录请求要素的假图床；handler 里可按需返回错误。
func newSrv(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestUpload(t *testing.T) {
	var gotAuth, gotFilename, gotPartCT, gotBody string
	srv := newSrv(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/api/upload" {
			t.Errorf("路径 = %s, want /api/upload", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("multipart 解析失败: %v", err)
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			t.Errorf("缺 file 字段: %v", err)
			return
		}
		defer f.Close()
		b, _ := io.ReadAll(f)
		gotBody, gotFilename, gotPartCT = string(b), hdr.Filename, hdr.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "abc123", "url": "https://img.example/i/abc123", "filename": "image.png", "size": 5})
	})
	c := &Client{BaseURL: srv.URL + "/", Token: "tk"} // 末尾 / 应被容忍
	f, err := c.Upload(context.Background(), "image.png", "image/png", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if f.ID != "abc123" || f.URL != "https://img.example/i/abc123" || f.Size != 5 {
		t.Errorf("解析结果不对: %+v", f)
	}
	if gotAuth != "Bearer tk" {
		t.Errorf("Authorization = %q, want Bearer tk", gotAuth)
	}
	if gotBody != "hello" {
		t.Errorf("上传内容 = %q, want hello", gotBody)
	}
	if gotFilename != "image.png" || gotPartCT != "image/png" {
		t.Errorf("part 文件名/类型 = %q/%q", gotFilename, gotPartCT)
	}
}

func TestUploadDefaults(t *testing.T) {
	srv := newSrv(t, func(w http.ResponseWriter, r *http.Request) {
		_, hdr, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Filename != "image.png" {
			t.Errorf("空文件名应默认 image.png, got %q", hdr.Filename)
		}
		if ct := hdr.Header.Get("Content-Type"); ct != "application/octet-stream" {
			t.Errorf("空类型应默认 octet-stream, got %q", ct)
		}
		_, _ = io.WriteString(w, `{"id":"x","url":"u"}`)
	})
	if _, err := (&Client{BaseURL: srv.URL}).Upload(context.Background(), "", "", nil); err != nil {
		t.Fatal(err)
	}
}

func TestUploadServerErr(t *testing.T) {
	srv := newSrv(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no auth", http.StatusUnauthorized)
	})
	_, err := (&Client{BaseURL: srv.URL}).Upload(context.Background(), "a.png", "image/png", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "no auth") {
		t.Errorf("应报 HTTP 401 错误, got %v", err)
	}
}

func TestUploadNoURL(t *testing.T) {
	srv := newSrv(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"x"}`) // 有 id 无 url
	})
	_, err := (&Client{BaseURL: srv.URL}).Upload(context.Background(), "a.png", "", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "无 url") {
		t.Errorf("响应无 url 应报错, got %v", err)
	}
	srv2 := newSrv(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "not json") })
	_, err = (&Client{BaseURL: srv2.URL}).Upload(context.Background(), "a.png", "", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "解析") {
		t.Errorf("非法 JSON 应报解析错误, got %v", err)
	}
}

func TestUploadURL(t *testing.T) {
	var gotBody string
	srv := newSrv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/upload/url" {
			t.Errorf("请求 = %s %s, want POST /api/upload/url", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, `{"id":"i1","url":"https://bed/i/i1","filename":"image.jpg"}`)
	})
	f, err := (&Client{BaseURL: srv.URL, Token: "tk"}).UploadURL(context.Background(), "https://example.com/a.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if f.URL != "https://bed/i/i1" {
		t.Errorf("url = %q", f.URL)
	}
	if gotBody != `{"url":"https://example.com/a.jpg"}` {
		t.Errorf("body = %s", gotBody)
	}
}

func TestListAndDelete(t *testing.T) {
	var gotMethod, gotPath string
	srv := newSrv(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		switch gotPath {
		case "/api/images":
			_, _ = io.WriteString(w, `[{"id":"a","url":"u","filename":"f","size":1}]`)
		case "/api/images/a b":
			_, _ = io.WriteString(w, `{"message":"deleted"}`)
		default:
			http.NotFound(w, r)
		}
	})
	c := &Client{BaseURL: srv.URL}
	files, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].ID != "a" || files[0].Size != 1 {
		t.Errorf("List 解析不对: %+v", files)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("List method = %s", gotMethod)
	}
	if err := c.Delete(context.Background(), "a b"); err != nil { // id 应被转义成路径
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/images/a b" {
		t.Errorf("Delete = %s %s", gotMethod, gotPath)
	}
}
