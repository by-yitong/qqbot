package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"qqbot/internal/bot"
)

// TestVideoOnMessageAbortsShareLink 截流契约：命中分享链接的消息 Abort
// （注册在其后的监听器——AI 兜底——不再执行），非链接消息原样放行。
// 链上 Abort 短路语义见 internal/bot 的 TestAbortStopsListeners；
// 装配顺序（Recorder → Video → Agent）由 main.go 的注释钉住。
func TestVideoOnMessageAbortsShareLink(t *testing.T) {
	p := NewVideo(nil)
	// 解析 API 指向本地死端口：后台 goroutine 秒失败，测试不触外网。
	p.chain = NewParserChain(&LuoyingClient{Base: "http://127.0.0.1:1", APIKey: "k",
		HTTP: &http.Client{Timeout: time.Second}})
	b := bot.New(bot.Config{})

	c := bot.NewEventCtx(b, "group", "看看这个 https://v.douyin.com/4wZAPduuQ2c/ 复制此链接",
		"U1", "小明", "G1", "M1", "", false)
	p.onMessage(c)
	if !c.Aborted() {
		t.Error("分享链接应 Abort（截住 AI 兜底）")
	}

	plain := bot.NewEventCtx(b, "group", "普通聊天", "U1", "小明", "G1", "M2", "", false)
	p.onMessage(plain)
	if plain.Aborted() {
		t.Error("非链接消息不应 Abort")
	}
}

// TestVideoDefaultParserOrder 装配契约：默认序 zhishuzhan（默认启用、凭据可空）
// 首选、luoying（配了 key 才在）备份；VIDEO_PARSERS 可重排/取子集。
func TestVideoDefaultParserOrder(t *testing.T) {
	names := func(p *VideoPlugin) []string {
		var out []string
		for _, x := range p.chain.parsers {
			out = append(out, x.Name())
		}
		return out
	}
	t.Setenv("ZHISHUZHAN_SESSION_FILE", filepath.Join(t.TempDir(), "no-such.json"))
	t.Setenv("ZHISHUZHAN_TOKENS", "")
	t.Setenv("ZHISHUZHAN_USER_ID", "")
	t.Setenv("LUOYING_API_KEY", "k")
	t.Setenv("VIDEO_PARSERS", "")
	if got := names(NewVideo(nil)); strings.Join(got, ",") != "zhishuzhan,luoying" {
		t.Errorf("默认序应 zhishuzhan,luoying: %v", got)
	}
	t.Setenv("VIDEO_PARSERS", "luoying")
	if got := names(NewVideo(nil)); strings.Join(got, ",") != "luoying" {
		t.Errorf("VIDEO_PARSERS=luoying 应只剩 luoying: %v", got)
	}
}

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
	c := &LuoyingClient{Base: srv.URL, APIKey: "test-key", HTTP: srv.Client()}
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
	for _, want := range []string{"[抖音]", "瞎看什么", "1080p", "NEVER GONNA GIVE YOU UP"} {
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
	c := &LuoyingClient{Base: srv.URL, APIKey: "test-key", HTTP: srv.Client()}
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
	c := &LuoyingClient{Base: srv.URL, APIKey: "test-key", HTTP: srv.Client()}
	res, err := c.ParseKuaishou(context.Background(), "https://v.kuaishou.com/KPgjzK9F")
	if err != nil {
		t.Fatal(err)
	}
	if res.VideoURL != "https://js.example/play.mp4" || res.Title == "" {
		t.Errorf("快手解析不对: %+v", res)
	}
}

// 快速失败契约：502 恒定失败时只请求 1 次、立即报错——重试/故障转移交给 ParserChain。
func TestLuoyingFastFailOn502(t *testing.T) {
	var n int
	srv := lyTestServer(t, "test-key", func(_ string, calls int) (int, string) {
		n = calls
		return 200, `{"code":502,"error":"解析失败, 请检查关键字是否正确"}`
	})
	defer srv.Close()
	c := &LuoyingClient{Base: srv.URL, APIKey: "test-key", HTTP: srv.Client()}
	_, err := c.ParseKuaishou(context.Background(), "https://v.kuaishou.com/x")
	if err == nil || !strings.Contains(err.Error(), "解析失败") {
		t.Fatalf("502 应立即失败: %v", err)
	}
	if n != 1 {
		t.Errorf("快速失败应只请求 1 次，实际 %d 次", n)
	}
}

func TestLuoyingErrPaths(t *testing.T) {
	// 无 key。
	c := &LuoyingClient{}
	if _, err := c.ParseDouyin(context.Background(), "https://v.douyin.com/x/"); err == nil || !strings.Contains(err.Error(), "LUOYING_API_KEY") {
		t.Errorf("缺 key 应报错: %v", err)
	}
	// 业务失败：快速失败，错误信息带上接口的原始错误。
	srv := lyTestServer(t, "k", func(_ string, _ int) (int, string) {
		return 200, `{"code":502,"error":"解析失败, 请检查关键字是否正确"}`
	})
	defer srv.Close()
	c2 := &LuoyingClient{Base: srv.URL, APIKey: "k", HTTP: srv.Client()}
	if _, err := c2.ParseKuaishou(context.Background(), "https://v.kuaishou.com/x"); err == nil || !strings.Contains(err.Error(), "解析失败") {
		t.Errorf("持续失败应透出接口错误: %v", err)
	}
	// 429：快速返回并透出限频语义（不再有 retry_after 等待）。
	var n int
	srv2 := lyTestServer(t, "k", func(_ string, calls int) (int, string) {
		n = calls
		return 429, `{"code":429,"error":"请求过于频繁","details":{"retry_after":1}}`
	})
	defer srv2.Close()
	c3 := &LuoyingClient{Base: srv2.URL, APIKey: "k", HTTP: srv2.Client()}
	if _, err := c3.ParseKuaishou(context.Background(), "https://v.kuaishou.com/x"); err == nil || !strings.Contains(err.Error(), "限频") {
		t.Errorf("429 应报限频: %v", err)
	}
	if n != 1 {
		t.Errorf("429 应快速返回只请求 1 次，实际 %d 次", n)
	}
}

// TestLuoyingAsParser 验证 Parse 按链接域名分流：抖音/快手各打对应接口，
// 其他域名返回 ErrUnsupportedPlatform（把机会让给链上的下一个源）。
func TestLuoyingAsParser(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := lyTestServer(t, "k", func(path string, _ int) (int, string) {
		mu.Lock()
		paths = append(paths, path)
		mu.Unlock()
		switch path {
		case "/api/douyin/info":
			return 200, `{"code":200,"message":"OK","data":{"title":"抖音视频","videos":["https://v/d.mp4"]}}`
		case "/api/kuaishou/info":
			return 200, `{"code":200,"message":"OK","data":{"title":"快手视频","videos":["https://v/k.mp4"]}}`
		}
		return 404, `{"code":404,"message":"Not Found"}`
	})
	defer srv.Close()
	c := &LuoyingClient{Base: srv.URL, APIKey: "k", HTTP: srv.Client()}
	if c.Name() != "luoying" {
		t.Errorf("Name = %q, want luoying", c.Name())
	}
	res, err := c.Parse(context.Background(), "https://v.douyin.com/x/")
	if err != nil || res.Title != "抖音视频" {
		t.Errorf("抖音链接分流错误: %v %+v", err, res)
	}
	res, err = c.Parse(context.Background(), "https://v.kuaishou.com/y")
	if err != nil || res.Title != "快手视频" {
		t.Errorf("快手链接分流错误: %v %+v", err, res)
	}
	mu.Lock()
	want := []string{"/api/douyin/info", "/api/kuaishou/info"}
	got := paths
	mu.Unlock()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("请求路径 = %v, want %v", got, want)
	}
	if _, err := c.Parse(context.Background(), "https://example.com/x"); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Errorf("其他链接应返回 ErrUnsupportedPlatform: %v", err)
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

// TestVideoRetryAfterRelogin 扫码成功 → retryPending 自动重试失败链接：
// 重试用原链接打到链上、去重键被清（手动重发也放行）、重试一次后 pending 清空。
func TestVideoRetryAfterRelogin(t *testing.T) {
	parsed := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/parse/index" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			PageUrl string
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		parsed <- body.PageUrl
		fmt.Fprint(w, `{"Code":200,"Msg":"OK","Data":{"title":"t","isVideo":true,"videoUrls":"https://dl/v.mp4"}}`)
	}))
	defer srv.Close()
	zs := &ZhishuzhanClient{Base: srv.URL, Tokens: "t", UserID: "1", HTTP: srv.Client(),
		sessionFile: filepath.Join(t.TempDir(), "s.json")}
	p := &VideoPlugin{chain: NewParserChain(zs), zs: zs, client: &http.Client{Timeout: time.Second}}

	link := "https://v.douyin.com/retry1/"
	c := bot.NewEventCtx(bot.New(bot.Config{}), "group", "看 "+link, "U1", "小明", "G1", "M1", "", false)
	p.claim(link) // 模拟首轮已处理过（占住去重键）
	p.storePending(link, c)
	p.retryPending()

	select {
	case got := <-parsed:
		if got != link {
			t.Errorf("重试应解析原链接: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("重试未发起")
	}
	if !p.claim(link) { // 去重键已清：同链接手动重发能再次放行
		t.Error("重试后去重键应被清除")
	}
	p.retryPending() // pending 已消费：再次触发无副作用
	select {
	case got := <-parsed:
		t.Errorf("pending 已消费，不应再重试: %q", got)
	case <-time.After(200 * time.Millisecond):
	}
}
