// zhishuzhan.com（兔子牙解析）客户端：快手解析 + 微信扫码登录。
//
// 接口（从站点内联 JS 逆向，2026-08 验证）：
//
//	POST /v2/parse/index        body {"PageUrl":"https://v.kuaishou.com/xxx"}
//	                            cookie: tokens=<登录 token>
//	                            Code: 200 成功 / 403 需登录 / 202,204 需付费 / 201 解析失败
//	GET  /v1/qrcode/get         → Data{Url(微信二维码图), Ticket}
//	GET  /v2/qrcode/check?ticket=… → Code 200 时 Data{Token, UserId}
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// zszLoginTimeout 二维码等待扫码的总时长（被动回复窗口 5 分钟，留余量）。
const zszLoginTimeout = 4 * time.Minute

// ErrZszLoginRequired 需要扫码登录（token 失效或没有）。
var ErrZszLoginRequired = errors.New("需要扫码登录（二维码已发出，请在 4 分钟内用微信扫码）")

// ErrZszPay 站点要求付费/次数用尽。
var ErrZszPay = errors.New("zhishuzhan 要求付费或解析次数用尽")

// ZszClient 是 zhishuzhan 客户端。登录 token 持久化到 sessionFile，跨重启复用。
type ZszClient struct {
	Base        string
	HTTP        *http.Client
	sessionFile string

	mu        sync.Mutex
	token     string
	userID    string
	loginBusy bool // 一个登录流程进行中：后来者不再重复发二维码
}

// NewZszClient 创建客户端；sessionFile 默认 ./data/zsz_session.json（可用 ZSZ_SESSION_FILE 覆盖）。
func NewZszClient() *ZszClient {
	f := os.Getenv("ZSZ_SESSION_FILE")
	if f == "" {
		f = "./data/zsz_session.json"
	}
	c := &ZszClient{Base: "https://zhishuzhan.com", HTTP: &http.Client{Timeout: 30 * time.Second}, sessionFile: f}
	c.loadSession()
	return c
}

type zszSession struct {
	Token   string    `json:"token"`
	UserID  string    `json:"user_id"`
	SavedAt time.Time `json:"saved_at"`
}

func (z *ZszClient) loadSession() {
	b, err := os.ReadFile(z.sessionFile)
	if err != nil {
		return
	}
	var s zszSession
	if json.Unmarshal(b, &s) == nil && s.Token != "" {
		z.token, z.userID = s.Token, s.UserID
	}
}

func (z *ZszClient) saveSessionLocked() {
	s := zszSession{Token: z.token, UserID: z.userID, SavedAt: time.Now()}
	b, _ := json.Marshal(s)
	_ = os.MkdirAll(filepath.Dir(z.sessionFile), 0o755)
	_ = os.WriteFile(z.sessionFile, b, 0o600)
}

// zszResp 统一外层：{Code, Msg, Data}。Data 结构随端点不同，延迟到调用方解析。
type zszResp struct {
	Code int             `json:"Code"`
	Msg  string          `json:"Msg"`
	Data json.RawMessage `json:"Data"`
}

func (z *ZszClient) do(ctx context.Context, method, path string, body []byte, withToken bool) (*zszResp, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, z.Base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/126 Safari/537.36")
	req.Header.Set("Referer", z.Base+"/")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	z.mu.Lock()
	tok := z.token
	z.mu.Unlock()
	if withToken && tok != "" {
		req.Header.Set("Cookie", "tokens="+tok+"; user_id="+z.userID)
	}
	resp, err := z.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 zhishuzhan 失败: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("zhishuzhan HTTP %d: %s", resp.StatusCode, truncStr(string(b), 200))
	}
	var r zszResp
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("zhishuzhan 响应异常: %s", truncStr(string(b), 200))
	}
	return &r, nil
}

// zszParseData 是 /v2/parse/index 成功时 Data 的结构（字段名照站点 JS 使用）。
type zszParseData struct {
	Title     string          `json:"title"`
	URL       string          `json:"url"`
	VideoURLs string          `json:"videoUrls"`
	Videos    []string        `json:"videos"`
	Audio     json.RawMessage `json:"Audio"` // 站点有时返回数组，插件不用此字段，原样忽略
	Cover     string          `json:"cover"`
	CoverURLs string          `json:"coverUrls"`
	Atlas     []string        `json:"atlas"`
	Quality   string          `json:"quality"`
}

// Parse 解析一条分享链接（须已登录）。返回统一的 VideoResult。
func (z *ZszClient) Parse(ctx context.Context, pageURL string) (*VideoResult, error) {
	body, _ := json.Marshal(map[string]string{"PageUrl": pageURL})
	r, err := z.do(ctx, http.MethodPost, "/v2/parse/index", body, true)
	if err != nil {
		return nil, err
	}
	switch r.Code {
	case 200:
	case 403, 201:
		// 403=站点明确的未登录；实测未登录时也常回 201「解析失败，请联系微信…」。
		// 两种都按未登录处理（走扫码后重试），真死链会在重试后再次失败并如实报错。
		return nil, ErrZszLoginRequired
	case 202, 204:
		return nil, ErrZszPay
	default:
		return nil, fmt.Errorf("解析失败(code=%d): %s", r.Code, truncStr(r.Msg, 120))
	}
	var d zszParseData
	if err := json.Unmarshal(r.Data, &d); err != nil {
		log.Printf("[video] 解析结果字段类型异常，原始 Data: %s", truncStr(string(r.Data), 1000))
		return nil, fmt.Errorf("解析结果格式异常: %w", err)
	}
	video := d.VideoURLs
	if video == "" && len(d.Videos) > 0 {
		video = d.Videos[0]
	}
	if video == "" {
		video = d.URL
	}
	return &VideoResult{
		Title:    d.Title,
		VideoURL: video,
		Images:   d.Atlas,
		Quality:  d.Quality,
	}, nil
}

// zszQrcodeData 是 /v1/qrcode/get 成功时 Data 的结构。
type zszQrcodeData struct {
	Ticket string `json:"Ticket"`
	URL    string `json:"Url"` // 微信 showqrcode 图片直链
}

// QrStart 取一张登录二维码（图片 URL + 轮询 ticket）。
func (z *ZszClient) QrStart(ctx context.Context) (imgURL, ticket string, err error) {
	r, err := z.do(ctx, http.MethodGet, "/v1/qrcode/get", nil, false)
	if err != nil {
		return "", "", err
	}
	if r.Code != 200 {
		return "", "", fmt.Errorf("取二维码失败: %s", truncStr(r.Msg, 120))
	}
	var d zszQrcodeData
	if err := json.Unmarshal(r.Data, &d); err != nil || d.Ticket == "" {
		return "", "", fmt.Errorf("二维码响应异常")
	}
	return d.URL, d.Ticket, nil
}

// zszCheckData 是 /v2/qrcode/check 成功时 Data 的结构。
type zszCheckData struct {
	Token  string      `json:"Token"`
	UserID json.Number `json:"UserId"` // 站点返回的是数字
}

// QrWait 轮询扫码状态直到成功或超时；成功即保存登录态并返回。
func (z *ZszClient) QrWait(ctx context.Context, ticket string) error {
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	deadline := time.After(zszLoginTimeout)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("等待扫码超时（%s）", zszLoginTimeout)
		case <-tick.C:
		}
		r, err := z.do(ctx, http.MethodGet, "/v2/qrcode/check?ticket="+ticket, nil, false)
		if err != nil {
			continue // 轮询中的网络抖动忽略
		}
		if r.Code != 200 {
			continue // 未扫/未确认
		}
		var d zszCheckData
		if err := json.Unmarshal(r.Data, &d); err != nil || d.Token == "" {
			log.Printf("[video] 扫码 check 成功但解析不到 Token，原始 Data: %s", truncStr(string(r.Data), 500))
			return fmt.Errorf("扫码成功但响应缺 Token（原始数据已记录日志）")
		}
		z.mu.Lock()
		z.token, z.userID = d.Token, d.UserID.String()
		z.loginBusy = false
		z.saveSessionLocked()
		z.mu.Unlock()
		return nil
	}
}

// EnsureLogin 确保有登录态：没有则通过 sendQR 把二维码发到会话，等用户扫码。
// sendQR 返回 error 表示二维码发不出去。同一时刻只允许一个登录流程，
// 后来者等待该流程结束后复用其结果。
func (z *ZszClient) EnsureLogin(ctx context.Context, sendQR func(imgURL string) error) error {
	z.mu.Lock()
	has := z.token != ""
	if !has && !z.loginBusy {
		z.loginBusy = true
	}
	started := !has && z.loginBusy // 本调用是否是发起者（上面刚置位）
	z.mu.Unlock()
	if has {
		return nil
	}

	if !started {
		// 已有登录流程在进行：等它结束（成功后 token 即可复用）。
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(zszLoginTimeout):
		}
		z.mu.Lock()
		ok := z.token != ""
		z.mu.Unlock()
		if ok {
			return nil
		}
		return fmt.Errorf("等待扫码超时")
	}

	defer func() {
		z.mu.Lock()
		z.loginBusy = false
		z.mu.Unlock()
	}()

	imgURL, ticket, err := z.QrStart(ctx)
	if err != nil {
		return err
	}
	if err := sendQR(imgURL); err != nil {
		return fmt.Errorf("二维码发送失败: %w", err)
	}
	if err := z.QrWait(ctx, ticket); err != nil {
		return err
	}
	return nil
}
