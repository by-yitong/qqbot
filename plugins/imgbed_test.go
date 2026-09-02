package plugins

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qqbot/internal/bot"
	"qqbot/pkg/imgbed"
)

// newImgbedPair 起一对假服务器：cdn 模拟 QQ 附件图源，bed 模拟图床 API。
func newImgbedPair(t *testing.T, cdn http.HandlerFunc, bed http.HandlerFunc) (*httptest.Server, *httptest.Server) {
	t.Helper()
	cdnSrv := httptest.NewServer(cdn)
	bedSrv := httptest.NewServer(bed)
	t.Cleanup(cdnSrv.Close)
	t.Cleanup(bedSrv.Close)
	return cdnSrv, bedSrv
}

func newImgbedForTest(bedURL string, maxBytes int64) *Imgbed {
	return &Imgbed{
		cli:      &imgbed.Client{BaseURL: bedURL, Token: "tk"},
		maxBytes: maxBytes,
		fetch:    &http.Client{Timeout: 5 * time.Second},
	}
}

func TestImgbedUploadAll(t *testing.T) {
	var gotFilename, gotPartCT string
	cdn, bed := newImgbedPair(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = io.WriteString(w, "jpeg-bytes")
		},
		func(w http.ResponseWriter, r *http.Request) {
			f, hdr, err := r.FormFile("file")
			if err != nil {
				t.Errorf("缺 file 字段: %v", err)
				return
			}
			defer f.Close()
			b, _ := io.ReadAll(f)
			if string(b) != "jpeg-bytes" {
				t.Errorf("上传内容 = %q", b)
			}
			gotFilename, gotPartCT = hdr.Filename, hdr.Header.Get("Content-Type")
			_, _ = io.WriteString(w, `{"id":"x1","url":"https://bed.example/i/x1"}`)
		})
	p := newImgbedForTest(bed.URL, 1<<20)
	out := p.uploadAll(context.Background(), []bot.Image{{URL: cdn.URL + "/img", ContentType: "image/jpeg"}})
	if !strings.Contains(out, "1/1") || !strings.Contains(out, "https://bed.example/i/x1") {
		t.Errorf("回复应含成功计数与外链: %s", out)
	}
	// QQ 附件无文件名 → 从 content-type 推成 image.jpg
	if gotFilename != "image.jpg" || gotPartCT != "image/jpeg" {
		t.Errorf("文件名/类型 = %q/%q, want image.jpg/image/jpeg", gotFilename, gotPartCT)
	}
}

func TestImgbedUploadAllPartialFail(t *testing.T) {
	cdn, bed := newImgbedPair(t,
		func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "missing") {
				http.NotFound(w, r)
				return
			}
			_, _ = io.WriteString(w, "ok")
		},
		func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) })
	p := newImgbedForTest(bed.URL, 1<<20)
	out := p.uploadAll(context.Background(), []bot.Image{
		{URL: cdn.URL + "/good", Filename: "a.png"},    // 下载成功、图床 500
		{URL: cdn.URL + "/missing", Filename: "b.png"}, // CDN 404
	})
	if !strings.Contains(out, "0/2") {
		t.Errorf("两张都失败应计 0/2: %s", out)
	}
	if !strings.Contains(out, "图床 HTTP 500") || !strings.Contains(out, "下载图片 HTTP 404") {
		t.Errorf("应分别报上传/下载错误: %s", out)
	}
}

func TestImgbedSizeLimit(t *testing.T) {
	cdn, bed := newImgbedPair(t,
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(w, io.LimitReader(zeroReader{}, 3<<20)) // 3MB 假图
		},
		func(w http.ResponseWriter, r *http.Request) { t.Error("超限图不应到达图床") })
	p := newImgbedForTest(bed.URL, 1<<20) // 上限 1MB
	out := p.uploadAll(context.Background(), []bot.Image{{URL: cdn.URL + "/big"}})
	if !strings.Contains(out, "0/1") || !strings.Contains(out, "1MB") {
		t.Errorf("超限应报上限错误: %s", out)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { return len(p), nil }

func TestImgbedInstall(t *testing.T) {
	b := bot.New(bot.Config{}, bot.WithPrefixes("/"))
	newImgbedForTest("http://bed", 0).Install(b)
	if help := b.Help(); !strings.Contains(help, "图床") || !strings.Contains(help, "imgbed") {
		t.Errorf("命令应注册进 help: %s", help)
	}
}
