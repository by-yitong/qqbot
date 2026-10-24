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

// lyTestServer 起一个 luoying API 假服务：校验 Bearer 头与 keyword 参数，
// 按 path 返回预置 JSON；hook 允许用例动态改写响应（模拟 502/429 抖动）。
func lyTestServer(t *testing.T, key string, hook func(path string, n int) (int, string)) *httptest.Server {
	t.Helper()
	var n int
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if got := r.Header.Get("Authorization"); got != "Bearer "+key {
			t.Errorf("Authorization = %q, want Bearer %s", got, key)
		}
		if kw := r.URL.Query().Get("keyword"); kw == "" {
			t.Error("缺少 keyword 参数")
		}
		code, body := hook(r.URL.Path, n)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	}))
}

func TestLuoyingParseDouyin(t *testing.T) {
	srv := lyTestServer(t, "test-key", func(path string, _ int) (int, string) {
		if path != "/api/douyin/info" {
			return 404, `{"code":404,"message":"Not Found"}`
		}
		return 200, `{"code":200,"message":"OK","data":{
			"title":"80年代舞曲《NEVER GONNA GIVE YOU UP》4K 60FPS","desc":"d",
			"author":"瞎看什么",
			"videos":["https://aweme.snssdk.com/aweme/v1/play?video_id=v0d00fg"],
			"formats":[{"url":"https://cdn.example/1080.mp4","width":"1440","height":"1080","size":"0 B"},
			           {"url":"https://cdn.example/720.mp4","width":"960","height":"720","size":"0 B"}],
			"images":[]}}`
	})
	defer srv.Close()
	c := &LuoyingClient{Base: srv.URL, APIKey: "test-key", Backoff: time.Millisecond, HTTP: srv.Client()}
	res, err := c.ParseDouyin(context.Background(), "https://v.douyin.com/O9lqomfBjaI/")
	if err != nil {
		t.Fatal(err)
	}
	if res.VideoURL != "https://aweme.snssdk.com/aweme/v1/play?video_id=v0d00fg" {
		t.Errorf("VideoURL = %q", res.VideoURL)
	}
	if res.Author != "瞎看什么" || res.Quality != "1080p" {
		t.Errorf("作者/清晰度不对: %+v", res)
	}
	s := res.Summary()
	for _, want := range []string{"瞎看什么", "1080p", "NEVER GONNA GIVE YOU UP"} {
		if !strings.Contains(s, want) {
			t.Errorf("简介缺 %q: %s", want, s)
		}
	}
}

func TestLuoyingParseDouyinGallery(t *testing.T) {
	srv := lyTestServer(t, "test-key", func(path string, _ int) (int, string) {
		return 200, `{"code":200,"message":"OK","data":{
			"title":"图集","author":"a",
			"videos":["https://bgm.example/fake.mp4"],
			"images":["https://c/1.jpg","https://c/2.jpg","https://c/3.jpg"]}}`
	})
	defer srv.Close()
	c := &LuoyingClient{Base: srv.URL, APIKey: "test-key", Backoff: time.Millisecond, HTTP: srv.Client()}
	res, err := c.ParseDouyin(context.Background(), "https://v.douyin.com/g/")
	if err != nil {
		t.Fatal(err)
	}
	if res.VideoURL != "" || len(res.Images) != 3 {
		t.Errorf("图集解析不对（videos 应清空）: %+v", res)
	}
	if !strings.Contains(res.Summary(), "图集 3 张") {
		t.Errorf("简介应含图集张数: %s", res.Summary())
	}
}

func TestLuoyingParseKuaishou(t *testing.T) {
	srv := lyTestServer(t, "test-key", func(path string, _ int) (int, string) {
		if path != "/api/kuaishou/info" {
			return 404, `{"code":404,"message":"Not Found"}`
		}
		return 200, `{"code":200,"message":"OK","data":{
			"title":"用你的快乐告诉我 现在放开双手是对的",
			"videos":["https://js.example/play.mp4"]}}`
	})
	defer srv.Close()
	c := &LuoyingClient{Base: srv.URL, APIKey: "test-key", Backoff: time.Millisecond, HTTP: srv.Client()}
	res, err := c.ParseKuaishou(context.Background(), "https://v.kuaishou.com/KPgjzK9F")
	if err != nil {
		t.Fatal(err)
	}
	if res.VideoURL != "https://js.example/play.mp4" || res.Title == "" {
		t.Errorf("快手解析不对: %+v", res)
	}
}

// 实测快手接口偶发 502「解析失败」，同链接重试即穿过——验证重试逻辑。
func TestLuoyingRetryOn502(t *testing.T) {
	srv := lyTestServer(t, "test-key", func(_ string, n int) (int, string) {
		if n == 1 {
			return 502, `{"code":502,"message":"Bad Gateway","error":"解析失败, 请检查关键字是否正确"}`
		}
		return 200, `{"code":200,"message":"OK","data":{"title":"ok","videos":["https://v/1.mp4"]}}`
	})
	defer srv.Close()
	c := &LuoyingClient{Base: srv.URL, APIKey: "test-key", Backoff: time.Millisecond, HTTP: srv.Client()}
	if _, err := c.ParseKuaishou(context.Background(), "https://v.kuaishou.com/x"); err != nil {
		t.Fatalf("502 后重试应成功: %v", err)
	}
}

func TestLuoyingErrPaths(t *testing.T) {
	// 无 key。
	c := &LuoyingClient{Backoff: time.Millisecond}
	if _, err := c.ParseDouyin(context.Background(), "https://v.douyin.com/x/"); err == nil || !strings.Contains(err.Error(), "LUOYING_API_KEY") {
		t.Errorf("缺 key 应报错: %v", err)
	}
	// 业务失败且持续：重试耗尽后带上接口的错误信息。
	srv := lyTestServer(t, "k", func(_ string, _ int) (int, string) {
		return 200, `{"code":502,"error":"解析失败, 请检查关键字是否正确"}`
	})
	defer srv.Close()
	c2 := &LuoyingClient{Base: srv.URL, APIKey: "k", Backoff: time.Millisecond, HTTP: srv.Client()}
	if _, err := c2.ParseKuaishou(context.Background(), "https://v.kuaishou.com/x"); err == nil || !strings.Contains(err.Error(), "解析失败") {
		t.Errorf("持续失败应透出接口错误: %v", err)
	}
	// 429：透出限频语义并遵守 retry_after。
	var n int
	srv2 := lyTestServer(t, "k", func(_ string, _ int) (int, string) {
		n++
		return 429, `{"code":429,"error":"请求过于频繁","details":{"retry_after":1}}`
	})
	defer srv2.Close()
	c3 := &LuoyingClient{Base: srv2.URL, APIKey: "k", Backoff: time.Millisecond, HTTP: srv2.Client()}
	start := time.Now()
	if _, err := c3.ParseKuaishou(context.Background(), "https://v.kuaishou.com/x"); err == nil || !strings.Contains(err.Error(), "限频") {
		t.Errorf("429 应报限频: %v", err)
	}
	if n < 2 || time.Since(start) < 900*time.Millisecond {
		t.Errorf("429 应按 retry_after 等待后重试: n=%d elapsed=%s", n, time.Since(start))
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
