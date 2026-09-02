package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qqbot/pkg/imgbed"
)

// newUploadFixture 起一对假服务器：cdn 模拟用户消息里的图源，bed 模拟图床 API。
func newUploadFixture(t *testing.T) (cdnURL, bedURL string) {
	t.Helper()
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "missing") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = io.WriteString(w, "img-bytes")
	}))
	bed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"i1","url":"https://bed.example/i/i1"}`)
	}))
	t.Cleanup(cdn.Close)
	t.Cleanup(bed.Close)
	return cdn.URL, bed.URL
}

func newUploadTool(bedURL string) *UploadImageTool {
	return &UploadImageTool{
		Cli:      &imgbed.Client{BaseURL: bedURL, Token: "tk"},
		MaxBytes: 1 << 20,
		HTTP:     &http.Client{Timeout: 5 * time.Second},
	}
}

func TestUploadImageToolNoClient(t *testing.T) {
	out, err := (&UploadImageTool{}).Execute(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "未配置") {
		t.Errorf("nil Cli 应提示未配置, got %q", out)
	}
}

func TestUploadImageToolFromContext(t *testing.T) {
	cdn, bed := newUploadFixture(t)
	tool := newUploadTool(bed)
	// 无参调用：从 ctx 取当前消息的图片（模型看不到 URL 字符串的主路径）
	ctx := context.WithValue(context.Background(), imagesKey{}, []string{cdn + "/a.png", cdn + "/b.png"})
	out, err := tool.Execute(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2/2") || strings.Count(out, "https://bed.example/i/i1") != 2 {
		t.Errorf("应上传 ctx 里的两张图: %s", out)
	}
	// ctx 没图也没传参 → 提示
	out, _ = tool.Execute(context.Background(), "")
	if !strings.Contains(out, "没有图片") {
		t.Errorf("无图应提示, got %q", out)
	}
}

func TestUploadImageToolExplicitURL(t *testing.T) {
	cdn, bed := newUploadFixture(t)
	tool := newUploadTool(bed)
	out, err := tool.Execute(context.Background(), `{"image_url":"`+cdn+`/x.png"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1/1") || !strings.Contains(out, "https://bed.example/i/i1") {
		t.Errorf("显式 URL 应上传成功: %s", out)
	}
}

func TestUploadImageToolFailureAsText(t *testing.T) {
	cdn, bed := newUploadFixture(t)
	tool := newUploadTool(bed)
	// 下载 404：失败作为文本回给模型，不返回 error
	out, err := tool.Execute(context.Background(), `{"image_url":"`+cdn+`/missing.png"}`)
	if err != nil {
		t.Fatalf("失败不应返回 error（应作为文本回给模型）: %v", err)
	}
	if !strings.Contains(out, "0/1") || !strings.Contains(out, "HTTP 404") {
		t.Errorf("应报下载失败: %s", out)
	}
}

func TestImagesFromContext(t *testing.T) {
	if got := ImagesFromContext(context.Background()); got != nil {
		t.Errorf("未注入应返回 nil, got %v", got)
	}
	want := []string{"http://a"}
	ctx := context.WithValue(context.Background(), imagesKey{}, want)
	if got := ImagesFromContext(ctx); len(got) != 1 || got[0] != want[0] {
		t.Errorf("应取回注入的图片, got %v", got)
	}
}
