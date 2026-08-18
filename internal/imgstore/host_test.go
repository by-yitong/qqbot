package imgstore

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostNotConfigured(t *testing.T) {
	h := &Host{}
	if h.Configured() {
		t.Error("空 BaseURL 不应 Configured")
	}
	if _, err := h.Upload(context.Background(), []byte("x"), "png"); err == nil {
		t.Error("未配置应报错")
	}
	h2 := &Host{BaseURL: "https://img.example.com"}
	if !h2.Configured() {
		t.Error("有 BaseURL 应 Configured")
	}
}

func TestHostUpload(t *testing.T) {
	var gotAuth, gotCT, gotPath, gotQuery string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		body, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"url":"https://img.example.com/abc.png","id":"abc"}`))
	}))
	defer srv.Close()

	h := &Host{BaseURL: srv.URL, Token: "sekret"}
	url, err := h.Upload(context.Background(), []byte("PNGDATA"), "png")
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if url != "https://img.example.com/abc.png" {
		t.Errorf("url=%q", url)
	}
	if gotAuth != "Bearer sekret" {
		t.Errorf("Authorization=%q", gotAuth)
	}
	if !strings.HasPrefix(gotCT, "application/octet-stream") {
		t.Errorf("Content-Type=%q", gotCT)
	}
	if gotPath != "/upload" || !strings.Contains(gotQuery, "ext=png") {
		t.Errorf("path=%q query=%q", gotPath, gotQuery)
	}
	if string(body) != "PNGDATA" {
		t.Errorf("上传字节=%q", string(body))
	}
}

func TestHostUploadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"msg":"bad token"}`))
	}))
	defer srv.Close()

	h := &Host{BaseURL: srv.URL, Token: "wrong"}
	if _, err := h.Upload(context.Background(), []byte("x"), "png"); err == nil {
		t.Fatal("401 应报错")
	}
}

func TestHostUploadNoURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"id":"abc"}`)) // 无 url 字段
	}))
	defer srv.Close()

	h := &Host{BaseURL: srv.URL}
	if _, err := h.Upload(context.Background(), []byte("x"), "png"); err == nil {
		t.Fatal("无 url 应报错")
	}
}

func TestHostUploadLocal(t *testing.T) {
	dir := t.TempDir()
	h := &Host{BaseURL: "http://162.example.com/files", Dir: dir}

	url, err := h.Upload(context.Background(), []byte("videobytes"), "mp4")
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(url, "http://162.example.com/files/")
	if !strings.HasSuffix(name, ".mp4") || len(name) != len("1234567890abcdef.mp4") {
		t.Fatalf("文件名应为 <16位id>.mp4: %q", name)
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || string(data) != "videobytes" {
		t.Fatalf("落盘内容不符: %v %q", err, data)
	}

	// 空扩展名默认 png
	u2, err := h.Upload(context.Background(), []byte("img"), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(u2, ".png") {
		t.Errorf("空 ext 应默认 png: %q", u2)
	}
}

func TestHostUploadLocalUnconfigured(t *testing.T) {
	h := &Host{Dir: t.TempDir()} // BaseURL 空 = 未配置
	if _, err := h.Upload(context.Background(), []byte("x"), "png"); err == nil {
		t.Fatal("BaseURL 为空应报未配置")
	}
}
