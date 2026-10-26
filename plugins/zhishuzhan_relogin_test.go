package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeNotifier 记录扫码流程推送的文本/图片；每条通知往 ch 发一个信号供测试同步
// （缓冲足够大，Send 永不阻塞流程），不靠 sleep 赌时序。
type fakeNotifier struct {
	mu     sync.Mutex
	texts  []string
	images []string
	ch     chan struct{}
}

func newFakeNotifier() *fakeNotifier { return &fakeNotifier{ch: make(chan struct{}, 64)} }

func (f *fakeNotifier) SendText(_ context.Context, text string) {
	f.mu.Lock()
	f.texts = append(f.texts, text)
	f.mu.Unlock()
	f.ch <- struct{}{}
}

func (f *fakeNotifier) SendImage(_ context.Context, imageURL string) {
	f.mu.Lock()
	f.images = append(f.images, imageURL)
	f.mu.Unlock()
	f.ch <- struct{}{}
}

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.texts) + len(f.images)
}

func (f *fakeNotifier) snapshot() (texts, images []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...), append([]string(nil), f.images...)
}

// wait 等到累计 n 条通知（channel 同步；2s 兜底失败，不 sleep 赌时序）。
func (f *fakeNotifier) wait(t *testing.T, n int) {
	t.Helper()
	for f.count() < n {
		select {
		case <-f.ch:
		case <-time.After(2 * time.Second):
			texts, images := f.snapshot()
			t.Fatalf("等待第 %d 条通知超时（texts=%v images=%v）", n, texts, images)
		}
	}
}

func containsAny(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// newZszReloginClient 建可续期的测试客户端（假凭据 + ms 级轮询参数 + 临时落盘路径）。
func newZszReloginClient(srv *httptest.Server, sessionFile string) *ZhishuzhanClient {
	return &ZhishuzhanClient{
		Base: srv.URL, Tokens: "old-token", UserID: "1", HTTP: srv.Client(),
		sessionFile: sessionFile, autoRelogin: true,
		pollInterval: 5 * time.Millisecond, pollTimeout: 5 * time.Second,
		reloginMinInterval: time.Hour,
	}
}

// 触发+成功全链路：parse 失效照常报错 → 发二维码 → 轮询（前 2 次 201、第 3 次 200）
// → 凭据更新（再 parse 带新 cookie）+ 落盘（内容/0600）+ 成功文案。
func TestZhishuzhanReloginSuccess(t *testing.T) {
	sessionFile := filepath.Join(t.TempDir(), "zhishuzhan-session.json")
	var mu sync.Mutex
	parseCalls, qrCalls, checkCalls := 0, 0, 0
	lastCookie := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/parse/index":
			mu.Lock()
			parseCalls++
			nth := parseCalls
			lastCookie = r.Header.Get("cookie")
			mu.Unlock()
			if nth == 1 { // 首次：会话失效特征
				fmt.Fprint(w, `{"Code":201,"Msg":"解析失败","Data":"请退出app重新打开尝试"}`)
				return
			}
			fmt.Fprint(w, `{"Code":200,"Msg":"请求成功","Data":{"title":"已续期","isVideo":true,"videoUrls":"https://dl.example/v.mp4"}}`)
		case "/v1/qrcode/get":
			mu.Lock()
			qrCalls++
			mu.Unlock()
			fmt.Fprint(w, `{"Code":200,"Msg":"请求成功","Data":{"Url":"https://mp.weixin.qq.com/qr.jpg","Ticket":"ticket-1"}}`)
		case "/v2/qrcode/check":
			mu.Lock()
			checkCalls++
			nth := checkCalls
			mu.Unlock()
			if nth <= 2 { // 未扫码
				fmt.Fprint(w, `{"Code":201,"Msg":"fail","Data":"fail"}`)
				return
			}
			fmt.Fprint(w, `{"Code":200,"Msg":"请求成功","Data":{"EndTime":0,"Message":"","Times":0,"Token":"new-token","UserId":99}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newZszReloginClient(srv, sessionFile)
	n := newFakeNotifier()
	c.SetNotifier(n)
	refreshed := make(chan struct{})
	c.SetOnCredsRefreshed(func() { close(refreshed) })

	// 1. 失效 parse：错误照常返回给调用方（链上照计失败），续期异步发起。
	if _, err := c.Parse(context.Background(), "https://v.douyin.com/x/"); err == nil || !strings.Contains(err.Error(), "请退出") {
		t.Fatalf("失效 parse 应照常报错: %v", err)
	}
	// 2. 等流程走完：二维码图 → 提示文案 → 成功文案（共 3 条）。
	n.wait(t, 3)
	select { // 凭据就绪回调也应被异步触发（恰好一次）
	case <-refreshed:
	case <-time.After(2 * time.Second):
		t.Error("凭据就绪回调未被触发")
	}
	texts, images := n.snapshot()
	if len(images) != 1 || images[0] != "https://mp.weixin.qq.com/qr.jpg" {
		t.Errorf("应收到的二维码图: %v", images)
	}
	if !containsAny(texts, "请用微信扫码续期") || !containsAny(texts, "扫码续期成功") {
		t.Errorf("通知文案缺失: %v", texts)
	}
	// 3. 接口调用次数：取码 1 次、轮询 3 次（2 次 201 + 1 次 200）。
	mu.Lock()
	gotQR, gotCheck := qrCalls, checkCalls
	mu.Unlock()
	if gotQR != 1 || gotCheck != 3 {
		t.Errorf("qrcode/get=%d (want 1), check=%d (want 3)", gotQR, gotCheck)
	}
	// 4. 落盘：内容与 0600 权限。
	b, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatalf("sessionFile 应已写入: %v", err)
	}
	var s zszSession
	if json.Unmarshal(b, &s) != nil || s.Tokens != "new-token" || s.UserID != "99" {
		t.Errorf("落盘内容不对: %s", b)
	}
	if _, err := time.Parse(time.RFC3339, s.UpdatedAt); err != nil {
		t.Errorf("updated_at 应为 RFC3339: %q", s.UpdatedAt)
	}
	if fi, _ := os.Stat(sessionFile); fi.Mode().Perm() != 0o600 {
		t.Errorf("落盘权限 = %v, want 0600", fi.Mode().Perm())
	}
	// 5. 再 parse：请求头带新 cookie，且正常解析成功。
	res, err := c.Parse(context.Background(), "https://v.douyin.com/x/")
	if err != nil || res.Title != "已续期" {
		t.Fatalf("续期后应正常解析: %v %+v", err, res)
	}
	mu.Lock()
	cookie := lastCookie
	mu.Unlock()
	if cookie != "tokens=new-token; user_id=99" {
		t.Errorf("续期后的 cookie = %q", cookie)
	}
}

// 频控：第一轮卡在 check（release 门）期间连打失效 parse，都不得并发发起第二轮；
// 放行后整轮完成，qrcode/get 全程只被打 1 次。
func TestZhishuzhanReloginFrequencyControl(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	parseCalls, qrCalls := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/parse/index":
			mu.Lock()
			parseCalls++
			mu.Unlock()
			fmt.Fprint(w, `{"Code":201,"Msg":"解析失败","Data":"请退出app重新打开尝试"}`)
		case "/v1/qrcode/get":
			mu.Lock()
			qrCalls++
			mu.Unlock()
			fmt.Fprint(w, `{"Code":200,"Msg":"请求成功","Data":{"Url":"https://mp.weixin.qq.com/qr.jpg","Ticket":"t"}}`)
		case "/v2/qrcode/check":
			select {
			case <-release: // 放行前一直阻塞（模拟用户还没扫码；频控断言在此期间完成）
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(w, `{"Code":200,"Msg":"请求成功","Data":{"EndTime":0,"Message":"","Times":0,"Token":"new-token","UserId":99}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newZszReloginClient(srv, filepath.Join(t.TempDir(), "s.json"))
	n := newFakeNotifier()
	c.SetNotifier(n)

	// 第 1 次失效 parse 发起第一轮（relogining=true 在 parse 返回前就已置位）。
	if _, err := c.Parse(context.Background(), "https://v.douyin.com/x/"); err == nil {
		t.Fatal("第一次 parse 应报失效")
	}
	n.wait(t, 2) // 二维码 + 提示文案已推（check 正阻塞在 release 门上）
	// 频控窗口内再打两次失效 parse：静默跳过，不得发起第二轮。
	for i := 0; i < 2; i++ {
		if _, err := c.Parse(context.Background(), "https://v.douyin.com/x/"); err == nil {
			t.Fatalf("第 %d 次重复 parse 应照常报错", i+2)
		}
	}
	mu.Lock()
	gotQR := qrCalls
	mu.Unlock()
	if gotQR != 1 {
		t.Fatalf("频控失效：qrcode/get 已被调 %d 次（应只 1 次）", gotQR)
	}
	// 放行 → 第一轮完成。
	close(release)
	n.wait(t, 3)
	mu.Lock()
	gotQR, gotParse := qrCalls, parseCalls
	mu.Unlock()
	if gotQR != 1 {
		t.Errorf("全程 qrcode/get=%d, want 1", gotQR)
	}
	if gotParse != 3 {
		t.Errorf("parse=%d, want 3", gotParse)
	}
	if c := n.count(); c != 3 {
		t.Errorf("通知应为 3 条（图+提示+成功），实际 %d", c)
	}
}

// 超时：check 恒 201 → 收到超时文案、凭据未换、文件未写。
func TestZhishuzhanReloginTimeout(t *testing.T) {
	sessionFile := filepath.Join(t.TempDir(), "zhishuzhan-session.json")
	var mu sync.Mutex
	qrCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/parse/index":
			fmt.Fprint(w, `{"Code":201,"Msg":"解析失败","Data":"请退出app重新打开尝试"}`)
		case "/v1/qrcode/get":
			mu.Lock()
			qrCalls++
			mu.Unlock()
			fmt.Fprint(w, `{"Code":200,"Msg":"请求成功","Data":{"Url":"https://mp.weixin.qq.com/qr.jpg","Ticket":"t"}}`)
		case "/v2/qrcode/check":
			fmt.Fprint(w, `{"Code":201,"Msg":"fail","Data":"fail"}`) // 一直没人扫码
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newZszReloginClient(srv, sessionFile)
	c.pollTimeout = 30 * time.Millisecond
	n := newFakeNotifier()
	c.SetNotifier(n)

	if _, err := c.Parse(context.Background(), "https://v.douyin.com/x/"); err == nil {
		t.Fatal("失效 parse 应报错")
	}
	n.wait(t, 3) // 图 + 提示 + 超时文案
	texts, _ := n.snapshot()
	if !containsAny(texts, "扫码超时未续期") {
		t.Errorf("应收到超时文案: %v", texts)
	}
	if tok, uid := c.creds(); tok != "old-token" || uid != "1" {
		t.Errorf("超时后凭据不应变化: %s/%s", tok, uid)
	}
	if _, err := os.Stat(sessionFile); !os.IsNotExist(err) {
		t.Errorf("超时不应落盘凭据: %v", err)
	}
	mu.Lock()
	gotQR := qrCalls
	mu.Unlock()
	if gotQR != 1 {
		t.Errorf("qrcode/get=%d, want 1", gotQR)
	}
}

// autoRelogin=0：parse 失效不触发任何通知与 qrcode 请求（跳过是同步判定，可立即断言）。
func TestZhishuzhanReloginDisabled(t *testing.T) {
	var mu sync.Mutex
	qrCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/parse/index":
			fmt.Fprint(w, `{"Code":201,"Msg":"解析失败","Data":"请退出app重新打开尝试"}`)
		case "/v1/qrcode/get":
			mu.Lock()
			qrCalls++
			mu.Unlock()
			fmt.Fprint(w, `{"Code":200,"Msg":"请求成功","Data":{"Url":"https://mp.weixin.qq.com/qr.jpg","Ticket":"t"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newZszReloginClient(srv, filepath.Join(t.TempDir(), "s.json"))
	c.autoRelogin = false
	n := newFakeNotifier()
	c.SetNotifier(n)

	if _, err := c.Parse(context.Background(), "https://v.douyin.com/x/"); err == nil || !strings.Contains(err.Error(), "请退出") {
		t.Fatalf("关闭续期时 parse 失效仍应报错: %v", err)
	}
	if got := n.count(); got != 0 {
		t.Errorf("不应有任何通知, 实际 %d 条", got)
	}
	mu.Lock()
	gotQR := qrCalls
	mu.Unlock()
	if gotQR != 0 {
		t.Errorf("不应请求二维码, 实际 %d 次", gotQR)
	}
}

// 凭据优先级：sessionFile 合法 → 覆盖 env；非法/缺字段 → 回退 env；都空 → 空凭据客户端（扫码引导）。
func TestZhishuzhanCredentialPriority(t *testing.T) {
	sf := filepath.Join(t.TempDir(), "session.json")
	t.Setenv("ZHISHUZHAN_TOKENS", "env-token")
	t.Setenv("ZHISHUZHAN_USER_ID", "8")
	t.Setenv("ZHISHUZHAN_SESSION_FILE", sf)

	// 1. 落盘合法：覆盖 env。
	if err := os.WriteFile(sf, []byte(`{"tokens":"file-token","user_id":"7","updated_at":"2026-10-26T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewZhishuzhanClientFromEnv()
	if c == nil || c.Tokens != "file-token" || c.UserID != "7" {
		t.Errorf("落盘 session 应覆盖 env: %+v", c)
	}
	// 2. 文件非法 JSON：回退 env。
	if err := os.WriteFile(sf, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c = NewZhishuzhanClientFromEnv(); c == nil || c.Tokens != "env-token" || c.UserID != "8" {
		t.Errorf("非法文件应回退 env: %+v", c)
	}
	// 3. 文件缺字段（tokens 空）：回退 env。
	if err := os.WriteFile(sf, []byte(`{"tokens":"","user_id":"7"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c = NewZhishuzhanClientFromEnv(); c == nil || c.Tokens != "env-token" || c.UserID != "8" {
		t.Errorf("缺字段文件应回退 env: %+v", c)
	}
	// 4. 都空：空凭据客户端（扫码登录引导，源仍默认启用）。
	if err := os.Remove(sf); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZHISHUZHAN_TOKENS", "")
	t.Setenv("ZHISHUZHAN_USER_ID", "")
	if c := NewZhishuzhanClientFromEnv(); c == nil || c.Tokens != "" || c.UserID != "" {
		t.Errorf("都空应返回空凭据客户端: %+v", c)
	}
}
