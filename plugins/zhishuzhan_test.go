package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// zszTestServer 起一个 zhishuzhan 假服务：校验 POST 方法、三个必带头与 body 的
// PageUrl，按 hook 返回预置 JSON。凭据一律用假值（真实 token 不得进任何文件）。
func zszTestServer(t *testing.T, hook func(pageUrl string) (int, string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		if got := r.Header.Get("cookie"); got != "tokens=test-token; user_id=42" {
			t.Errorf("cookie = %q, want tokens=test-token; user_id=42", got)
		}
		if got := r.Header.Get("origin"); got != zszDefaultBase {
			t.Errorf("origin = %q, want %s", got, zszDefaultBase)
		}
		if got := r.Header.Get("user-agent"); got != zszUserAgent {
			t.Errorf("user-agent = %q, want 桌面 UA", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("读 body 失败: %v", err)
		}
		var req struct {
			PageUrl string `json:"PageUrl"`
		}
		if json.Unmarshal(body, &req) != nil || req.PageUrl == "" {
			t.Errorf("body 应是含 PageUrl 的 JSON: %s", body)
		}
		code, resp := hook(req.PageUrl)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		fmt.Fprint(w, resp)
	}))
}

// newZszTestClient 指向假服务的客户端（假凭据）。
func newZszTestClient(srv *httptest.Server) *ZhishuzhanClient {
	return &ZhishuzhanClient{Base: srv.URL, Tokens: "test-token", UserID: "42", HTTP: srv.Client()}
}

// 抖音视频：videoUrls 映射 VideoURL；Formats 混有 daf0.cn 推广条目时 Quality 取干净条目。
func TestZhishuzhanParseDouyinVideo(t *testing.T) {
	srv := zszTestServer(t, func(pageUrl string) (int, string) {
		if pageUrl != "https://v.douyin.com/abc123/" {
			t.Errorf("PageUrl = %q", pageUrl)
		}
		return 200, `{"Code":200,"Msg":"请求成功","Data":{
			"title":"宫崎骏的夏天","author":null,"isVideo":true,
			"videoUrls":"https://dl.example/video.mp4",
			"lives":[],
			"Formats":[
				{"qualityNote":"最高画质","vext":"mp4","video":"https://test.daf0.cn/invite/index","audio":"","separate":0},
				{"qualityNote":"1080P","vext":"mp4","video":"https://aweme.example/1080.mp4","audio":"https://aweme.example/a.m4a","separate":1}
			]}}`
	})
	defer srv.Close()
	res, err := newZszTestClient(srv).Parse(context.Background(), "https://v.douyin.com/abc123/")
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "宫崎骏的夏天" || res.VideoURL != "https://aweme.example/1080.mp4" {
		t.Errorf("默认应选最高干净档 1080P: %+v", res)
	}
	if res.Quality != "1080P" {
		t.Errorf("Quality 应为选中档位 1080P, 实际 %q", res.Quality)
	}
	if len(res.Images) != 0 {
		t.Errorf("视频不应带图: %+v", res.Images)
	}
}

// 快手图集：pics → Images，VideoURL 必须为空。
func TestZhishuzhanParseKuaishouGallery(t *testing.T) {
	srv := zszTestServer(t, func(pageUrl string) (int, string) {
		if pageUrl != "https://v.kuaishou.com/xyz" {
			t.Errorf("PageUrl = %q", pageUrl)
		}
		return 200, `{"Code":200,"Msg":"请求成功","Data":{
			"title":"用你的快乐告诉我","isVideo":false,
			"videoUrls":"","pics":["https://p.example/1.jpg","https://p.example/2.jpg","https://p.example/3.jpg"]}}`
	})
	defer srv.Close()
	res, err := newZszTestClient(srv).Parse(context.Background(), "https://v.kuaishou.com/xyz")
	if err != nil {
		t.Fatal(err)
	}
	if res.VideoURL != "" || len(res.Images) != 3 {
		t.Errorf("图集映射不对（VideoURL 应为空）: %+v", res)
	}
	if !strings.Contains(res.Summary(), "图集 3 张") {
		t.Errorf("简介应含图集张数: %s", res.Summary())
	}
}

func TestZhishuzhanErrPaths(t *testing.T) {
	ctx := context.Background()
	// Code 201：Data 是原因字符串（cookie 失效形态），错误应透出该文案。
	srv := zszTestServer(t, func(_ string) (int, string) {
		return 200, `{"Code":201,"Msg":"解析失败，请联系微信：kaolajiexi2","Data":"请退出app重新打开尝试"}`
	})
	defer srv.Close()
	if _, err := newZszTestClient(srv).Parse(ctx, "https://v.douyin.com/x/"); err == nil ||
		!strings.Contains(err.Error(), "请退出app重新打开尝试") {
		t.Errorf("201 应透出 Data 提示: %v", err)
	}
	// Code 201 且 Data 解不出字符串（如 null）：退回 Msg。
	srv2 := zszTestServer(t, func(_ string) (int, string) {
		return 200, `{"Code":201,"Msg":"解析失败，请联系微信：kaolajiexi2","Data":null}`
	})
	defer srv2.Close()
	if _, err := newZszTestClient(srv2).Parse(ctx, "https://v.douyin.com/x/"); err == nil ||
		!strings.Contains(err.Error(), "kaolajiexi2") {
		t.Errorf("Data 解不出应用 Msg: %v", err)
	}
	// Code 200 但既无视频也无图。
	srv3 := zszTestServer(t, func(_ string) (int, string) {
		return 200, `{"Code":200,"Msg":"请求成功","Data":{"title":"空","isVideo":true,"videoUrls":"","pics":[]}}`
	})
	defer srv3.Close()
	if _, err := newZszTestClient(srv3).Parse(ctx, "https://v.douyin.com/x/"); err == nil ||
		!strings.Contains(err.Error(), "解析结果里没有视频/图片") {
		t.Errorf("无视频无图应报错: %v", err)
	}
	// 网络/HTTP 层错误（死端口）：请求失败快速返回。
	dead := &ZhishuzhanClient{Base: "http://127.0.0.1:1", Tokens: "t", UserID: "1",
		HTTP: &http.Client{Timeout: http.DefaultClient.Timeout}}
	if _, err := dead.Parse(ctx, "https://v.douyin.com/x/"); err == nil ||
		!strings.Contains(err.Error(), "请求解析接口失败") {
		t.Errorf("网络错误应报请求失败: %v", err)
	}
}

// Parse 分流：douyin/kuaishou 各打一次接口；其他域名返回 ErrUnsupportedPlatform 且不打接口。
func TestZhishuzhanAsParser(t *testing.T) {
	var mu sync.Mutex
	var calls int
	srv := zszTestServer(t, func(pageUrl string) (int, string) {
		mu.Lock()
		calls++
		mu.Unlock()
		return 200, fmt.Sprintf(`{"Code":200,"Msg":"请求成功","Data":{"title":"%s","isVideo":true,"videoUrls":"https://dl.example/v.mp4"}}`, pageUrl)
	})
	defer srv.Close()
	c := newZszTestClient(srv)
	if c.Name() != "zhishuzhan" {
		t.Errorf("Name = %q, want zhishuzhan", c.Name())
	}
	ctx := context.Background()
	res, err := c.Parse(ctx, "https://v.douyin.com/x/")
	if err != nil || res.Title != "https://v.douyin.com/x/" {
		t.Errorf("抖音链接应打接口: %v %+v", err, res)
	}
	if res, err = c.Parse(ctx, "https://v.kuaishou.com/y"); err != nil || res.VideoURL == "" {
		t.Errorf("快手链接应打接口: %v %+v", err, res)
	}
	if _, err := c.Parse(ctx, "https://example.com/x"); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Errorf("其他链接应返回 ErrUnsupportedPlatform: %v", err)
	}
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 2 {
		t.Errorf("接口应恰好被调 2 次（douyin+kuaishou），实际 %d", got)
	}
}

// 装配契约：默认启用、永不返回 nil——env 只是可选种子，凭据缺失时靠扫码登录引导。
// sessionFile 指到不存在的临时路径，隔离落盘凭据的干扰（优先级测试见 relogin 测试）。
func TestNewZhishuzhanClientFromEnv(t *testing.T) {
	t.Setenv("ZHISHUZHAN_SESSION_FILE", filepath.Join(t.TempDir(), "no-such-session.json"))
	t.Setenv("ZHISHUZHAN_TOKENS", "test-token")
	t.Setenv("ZHISHUZHAN_USER_ID", "42")
	c := NewZhishuzhanClientFromEnv()
	if c == nil || c.Name() != "zhishuzhan" || c.Tokens != "test-token" || c.UserID != "42" {
		t.Errorf("env 种子应注入客户端: %+v", c)
	}
	// 凭据缺失不再禁用源：返回空凭据客户端，首个解析请求触发扫码登录。
	t.Setenv("ZHISHUZHAN_USER_ID", "")
	t.Setenv("ZHISHUZHAN_TOKENS", "")
	if c := NewZhishuzhanClientFromEnv(); c == nil || c.Tokens != "" || c.UserID != "" {
		t.Errorf("无凭据应返回空凭据客户端（扫码引导）: %+v", c)
	}
}

// pickFormat 选档规则：默认最高 / 上限内最高 / 全超上限取最低 / 线路二带数字 /
// 全是推广回落空串（parse 再回落 videoUrls）。
func TestZhishuzhanPickFormat(t *testing.T) {
	d := &zszData{}
	if err := json.Unmarshal([]byte(`{"Formats":[
		{"qualityNote":"最高画质","video":"https://test.daf0.cn/invite/index"},
		{"qualityNote":"720p","video":"https://c/720.mp4"},
		{"qualityNote":"1080P","video":"https://c/1080.mp4"},
		{"qualityNote":"540p","video":"https://c/540.mp4"},
		{"qualityNote":"1292P(线路二)","video":"https://c/1292.mp4"}]}`), d); err != nil {
		t.Fatal(err)
	}
	if u, n := d.pickFormat(0); u != "https://c/1292.mp4" || n != "1292P(线路二)" {
		t.Errorf("默认应选最高档(含线路二数字): %s %s", u, n)
	}
	if u, _ := d.pickFormat(720); u != "https://c/720.mp4" {
		t.Errorf("上限 720 应选 720p: %s", u)
	}
	if u, _ := d.pickFormat(480); u != "https://c/540.mp4" {
		t.Errorf("上限 480 全超应取最低档 540p: %s", u)
	}
	d2 := &zszData{}
	_ = json.Unmarshal([]byte(`{"Formats":[{"qualityNote":"最高画质","video":"https://test.daf0.cn/x"}]}`), d2)
	if u, n := d2.pickFormat(0); u != "" || n != "" {
		t.Errorf("全推广应返回空串: %s %s", u, n)
	}
}

// 全推广 Formats → parse 回落顶层 videoUrls；ZHISHUZHAN_QUALITY 数字经构造器注入。
func TestZhishuzhanQualityFallbackAndEnv(t *testing.T) {
	srv := zszTestServer(t, func(string) (int, string) {
		return 200, `{"Code":200,"Msg":"OK","Data":{"title":"t","isVideo":true,
			"videoUrls":"https://dl/default.mp4",
			"Formats":[{"qualityNote":"最高画质","video":"https://test.daf0.cn/x"}]}}`
	})
	defer srv.Close()
	res, err := newZszTestClient(srv).Parse(context.Background(), "https://v.douyin.com/abc123/")
	if err != nil || res.VideoURL != "https://dl/default.mp4" || res.Quality != "" {
		t.Errorf("全推广应回落 videoUrls: %v %+v", err, res)
	}

	t.Setenv("ZHISHUZHAN_QUALITY", "720")
	t.Setenv("ZHISHUZHAN_SESSION_FILE", filepath.Join(t.TempDir(), "none.json"))
	t.Setenv("ZHISHUZHAN_TOKENS", "")
	t.Setenv("ZHISHUZHAN_USER_ID", "")
	if c := NewZhishuzhanClientFromEnv(); c.maxQuality != 720 {
		t.Errorf("数字应注入上限: %d", c.maxQuality)
	}
	t.Setenv("ZHISHUZHAN_QUALITY", "highest")
	if c := NewZhishuzhanClientFromEnv(); c.maxQuality != 0 {
		t.Errorf("highest 应为 0: %d", c.maxQuality)
	}
	t.Setenv("ZHISHUZHAN_QUALITY", "abc")
	if c := NewZhishuzhanClientFromEnv(); c.maxQuality != 0 {
		t.Errorf("非法值应回落最高档: %d", c.maxQuality)
	}
}
