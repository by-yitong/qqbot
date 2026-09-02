package imgbed

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDownloadImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = io.WriteString(w, "png-bytes")
		case "/noct": // 无 Content-Type 头也要能下
			_, _ = io.WriteString(w, "raw")
		case "/404":
			http.NotFound(w, r)
		case "/big":
			_, _ = io.Copy(w, io.LimitReader(endlessZero{}, 3<<20)) // 3MB > 1MB 上限
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	data, ct, err := DownloadImage(context.Background(), srv.URL+"/ok.png", 0, nil)
	if err != nil || string(data) != "png-bytes" || ct != "image/png" {
		t.Errorf("正常下载: data=%q ct=%q err=%v", data, ct, err)
	}
	if data, _, err = DownloadImage(context.Background(), srv.URL+"/noct", 0, nil); err != nil || string(data) != "raw" {
		t.Errorf("无类型下载: data=%q ct=%q err=%v", data, ct, err)
	}
	if _, _, err = DownloadImage(context.Background(), srv.URL+"/404", 0, nil); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("404 应报错, got %v", err)
	}
	if _, _, err = DownloadImage(context.Background(), srv.URL+"/big", 1<<20, nil); err == nil || !strings.Contains(err.Error(), "1MB") {
		t.Errorf("超限应报上限错误, got %v", err)
	}
	if _, _, err = DownloadImage(context.Background(), "://bad url", 0, nil); err == nil {
		t.Error("非法 URL 应报错")
	}
}

func TestFilenameFor(t *testing.T) {
	cases := []struct{ hint, ct, want string }{
		{"photo.JPG", "", "photo.JPG"}, // 有文件名原样用
		{"", "image/png", "image.png"},
		{"", "IMAGE/JPEG", "image.jpg"},
		{"", "image/gif", "image.gif"},
		{"  ", "", "image.png"}, // 全空兜底 png
	}
	for _, c := range cases {
		if got := FilenameFor(c.hint, c.ct); got != c.want {
			t.Errorf("FilenameFor(%q,%q) = %q, want %q", c.hint, c.ct, got, c.want)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	if got := HumanBytes(10 << 20); got != "10MB" {
		t.Errorf("HumanBytes(10MB) = %q", got)
	}
	if got := HumanBytes(500 << 10); got != "500KB" {
		t.Errorf("HumanBytes(500KB) = %q", got)
	}
}

type endlessZero struct{}

func (endlessZero) Read(p []byte) (int, error) { return len(p), nil }
