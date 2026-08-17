package plugins

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestZsz(t *testing.T, h http.Handler) *ZszClient {
	t.Helper()
	dir := t.TempDir()
	return &ZszClient{
		Base:        httptest.NewServer(h).URL,
		HTTP:        &http.Client{Timeout: 5 * time.Second},
		sessionFile: filepath.Join(dir, "zsz_session.json"),
	}
}

func TestZszParseOK(t *testing.T) {
	var gotCookie atomic.Value
	z := newTestZsz(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/parse/index" {
			gotCookie.Store(r.Header.Get("Cookie"))
			w.Write([]byte(`{"Code":200,"Msg":"ok","Data":{"title":"快手视频","videoUrls":"https://ks.example/v.mp4","atlas":["https://c/1.jpg"],"quality":"720p"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	z.token, z.userID = "tok123", "u1"
	res, err := z.Parse(context.Background(), "https://v.kuaishou.com/x/")
	if err != nil {
		t.Fatal(err)
	}
	if res.VideoURL != "https://ks.example/v.mp4" || res.Title != "快手视频" || len(res.Images) != 1 {
		t.Errorf("解析结果: %+v", res)
	}
	if c, _ := gotCookie.Load().(string); !strings.Contains(c, "tokens=tok123") {
		t.Errorf("未带登录 cookie: %q", c)
	}
}

func TestZszParseCodes(t *testing.T) {
	z := newTestZsz(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		code := strings.Split(string(body), "code=")
		if len(code) < 2 {
			w.Write([]byte(`{"Code":201,"Msg":"no code"}`))
			return
		}
		switch strings.TrimSuffix(code[1], `"}`) {
		case "403":
			w.Write([]byte(`{"Code":403,"Msg":"未登录"}`))
		case "202":
			w.Write([]byte(`{"Code":202,"Msg":"付费"}`))
		default:
			w.Write([]byte(`{"Code":201,"Msg":"解析失败"}`))
		}
	}))
	for code, wantErr := range map[string]error{"403": ErrZszLoginRequired, "201": ErrZszLoginRequired, "202": ErrZszPay} {
		if _, err := z.Parse(context.Background(), "https://z/x/?code="+code); err == nil || !strings.Contains(err.Error(), wantErr.Error()) {
			t.Errorf("code=%s 期望 %v, got %v", code, wantErr, err)
		}
	}
}

func TestZszQrFlow(t *testing.T) {
	var scanned atomic.Bool
	z := newTestZsz(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/qrcode/get":
			w.Write([]byte(`{"Code":200,"Msg":"ok","Data":{"Ticket":"T1","Url":"https://qr.example/img.png"}}`))
		case "/v2/qrcode/check":
			if !scanned.Swap(true) {
				w.Write([]byte(`{"Code":404,"Msg":"未扫"}`)) // 第一次：未扫
				return
			}
			w.Write([]byte(`{"Code":200,"Msg":"ok","Data":{"EndTime":0,"Message":"","Times":0,"Token":"NEW","UserId":972628}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	// 换成短间隔轮询避免测试跑 3 秒以上：这里直接调用 QrStart+QrWait（QrWait 是 3s tick，可接受一次 tick）。
	ctx := context.Background()
	imgURL, ticket, err := z.QrStart(ctx)
	if err != nil || imgURL != "https://qr.example/img.png" || ticket != "T1" {
		t.Fatalf("QrStart: %v %s %s", err, imgURL, ticket)
	}
	go func() { time.Sleep(100 * time.Millisecond); scanned.Store(true) }()
	if err := z.QrWait(ctx, ticket); err != nil {
		t.Fatal(err)
	}
	if z.token != "NEW" {
		t.Errorf("token 未更新: %s", z.token)
	}
	// 登录态应已落盘
	b, err := os.ReadFile(z.sessionFile)
	if err != nil || !strings.Contains(string(b), "NEW") {
		t.Errorf("session 未持久化: %v %s", err, string(b))
	}
	// 已有 token 时 EnsureLogin 直接通过
	if err := z.EnsureLogin(ctx, func(string) error { t.Fatal("不应再发二维码"); return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestZszSessionLoad(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "s.json")
	os.WriteFile(f, []byte(`{"token":"T","user_id":"U"}`), 0o600)
	z := &ZszClient{sessionFile: f}
	z.loadSession()
	if z.token != "T" || z.userID != "U" {
		t.Errorf("加载失败: %s %s", z.token, z.userID)
	}
}
