package plugins

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestShareLinkRE(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"2.35 05/26 :2pm fOX:/ M@W.zG 评测山姆 https://v.douyin.com/4wZAPduuQ2c/ 复制此链接", "https://v.douyin.com/4wZAPduuQ2c/"},
		{"https://v.kuaishou.com/nqQim11p 用你的快乐告诉我", "https://v.kuaishou.com/nqQim11p"},
		{"看看这个 http://v.douyin.com/abc123/ 哈哈", "http://v.douyin.com/abc123/"},
		{"没有链接的普通消息", ""},
		{"https://example.com/v.douyin.com/fake", ""}, // 域名不在 host 位置
	}
	for _, c := range cases {
		if got := shareLinkRE.FindString(c.in); got != c.want {
			t.Errorf("FindString(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseDouyinXZG(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/douyin/" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("url") == "" {
			t.Error("缺少 url 参数")
		}
		fmt.Fprint(w, `{"code":0,"msg":"解析成功","data":{"jx":{"url":"x","item_id":"1","type":"视频"},
		"author":{"name":"馋大鑫","uid":"x"},"stat":{"like":14446,"comment":185,"share":1137},
		"item":{"title":"评测山姆超市预制菜 # 预制菜","cover":"c","url":"https://cdn.example/v.mp4",
		"quality":"1080p","duration":554.9,"size_str":"66.48MB"}}}`)
	}))
	defer srv.Close()
	p := &VideoPlugin{apiBase: srv.URL}
	res, err := p.parseDouyinXZG(context.Background(), "https://v.douyin.com/4wZAPduuQ2c/")
	if err != nil {
		t.Fatal(err)
	}
	if res.VideoURL != "https://cdn.example/v.mp4" || res.Author != "馋大鑫" || res.Like != 14446 {
		t.Errorf("解析结果不对: %+v", res)
	}
	s := res.Summary()
	for _, want := range []string{"馋大鑫", "👍1.4万", "1080p", "9分14秒", "66.48MB", "评测山姆"} {
		if !strings.Contains(s, want) {
			t.Errorf("简介缺 %q: %s", want, s)
		}
	}
}

func TestParseDouyinErrCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"code":-1,"msg":"链接失效"}`)
	}))
	defer srv.Close()
	p := &VideoPlugin{apiBase: srv.URL}
	if _, err := p.parseDouyinXZG(context.Background(), "https://v.douyin.com/x/"); err == nil || !strings.Contains(err.Error(), "链接失效") {
		t.Errorf("期望 code 错误, got %v", err)
	}
}

func TestParseDouyinGalleryXZG(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"code":0,"msg":"ok","data":{"jx":{"type":"图集"},"author":{"name":"a"},
		"item":{"title":"图集","images":["https://c/1.jpg","https://c/2.jpg","https://c/3.jpg"]}}}`)
	}))
	defer srv.Close()
	p := &VideoPlugin{apiBase: srv.URL}
	res, err := p.parseDouyinXZG(context.Background(), "https://v.douyin.com/g/")
	if err != nil {
		t.Fatal(err)
	}
	if res.VideoURL != "" || len(res.Images) != 3 {
		t.Errorf("图集解析不对: %+v", res)
	}
	if !strings.Contains(res.Summary(), "图集 3 张") {
		t.Errorf("简介应含图集张数: %s", res.Summary())
	}
}

func TestVideoClaimDedup(t *testing.T) {
	p := &VideoPlugin{}
	if !p.claim("https://v.douyin.com/a/") {
		t.Error("首次应放行")
	}
	if p.claim("https://v.douyin.com/a/") {
		t.Error("窗口内重复应拦截")
	}
	if !p.claim("https://v.douyin.com/b/") {
		t.Error("不同链接应放行")
	}
}

func TestFmtNumAndDur(t *testing.T) {
	cases := map[int64]string{999: "999", 12345: "1.2万", 250_000_000: "2.5亿"}
	for in, want := range cases {
		if got := fmtNum(in); got != want {
			t.Errorf("fmtNum(%d)=%s want %s", in, got, want)
		}
	}
	if got := fmtDur(65); got != "1分05秒" {
		t.Errorf("fmtDur(65)=%s", got)
	}
	if got := fmtDur(45); got != "45秒" {
		t.Errorf("fmtDur(45)=%s", got)
	}
}

func TestExtOf(t *testing.T) {
	if got := extOf("video/mp4", "https://x/v.mp4?k=1"); got != "mp4" {
		t.Errorf("content-type 分支: %s", got)
	}
	if got := extOf("", "https://x/v.mp4?k=1"); got != "mp4" {
		t.Errorf("url 分支: %s", got)
	}
	if got := extOf("", "https://x/noext"); got != "mp4" {
		t.Errorf("默认: %s", got)
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("  a\n b  c ", 100); got != "a b c" {
		t.Errorf("oneLine 清洗: %q", got)
	}
	if got := oneLine("", 10); got != "(无标题)" {
		t.Errorf("空标题: %q", got)
	}
	if got := oneLine(strings.Repeat("字", 150), 120); !strings.HasSuffix(got, "…") || len([]rune(got)) != 121 {
		t.Errorf("截断: %d", len([]rune(got)))
	}
}

// 编译期确认超时/体积常量未被误改。
func TestVideoConstants(t *testing.T) {
	if videoDedupTTL < time.Minute {
		t.Error("去重窗口过短")
	}
	if maxProxyBytes != 200<<20 {
		t.Error("中转上限被改动")
	}
}
