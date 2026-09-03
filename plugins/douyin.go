// 抖音直连解析：不依赖第三方解析站，直接走抖音官方 web 接口（2026-09 验证）。
//
//  1. 短链 https://v.douyin.com/xxx/ 302 → …/share/video|note|slides/{aweme_id}/…，
//     从 Location 提取作品 ID；
//  2. GET www.douyin.com/aweme/v1/web/aweme/detail/?aweme_id=…
//     需带 ttwid Cookie（无签名 a_bogus，带 ttwid 即可返回完整数据）；
//  3. ttwid 由 ttwid.bytedance.com/union/register 免费签发，缓存复用（ttwidTTL）。
//
// 已知坑：detail 接口对无浏览器指纹的请求有概率回 403「Blocked by ArgusSecurityPlugin」
// （风控抖动，实测约三成且成串），带退避重试即可穿过；作品删除/私密时 aweme_detail
// 为 null，原因在 filter_detail.detail_msg。
package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// dyItemIDRE 从跳转地址提取作品 ID：…/share/video/123/、…/video/123、modal_id=123 均可。
var dyItemIDRE = regexp.MustCompile(`(?:video|note|slides)/(\d+)|modal_id=(\d+)`)

// dyGearRE 从 gear_name（如 normal_1080_0、adapt_720_0）提取清晰度数字。
var dyGearRE = regexp.MustCompile(`_(\d{3,4})_`)

const (
	dyUAWeb   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
	dyUAShare = "Mozilla/5.0 (Linux; Android 13) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Mobile Safari/537.36"

	// dyTtwidTTL ttwid 缓存时长（签发有效期远长于此，保守轮换）。
	dyTtwidTTL = 24 * time.Hour
)

// DouyinClient 抖音直连解析客户端。ttwid 惰性签发、内存缓存，并发安全。
type DouyinClient struct {
	ShortBase string // 短链域名（解析跳转用）
	WebBase   string // web API 域名
	TtwidReg  string // ttwid 签发端点
	HTTP      *http.Client

	mu      sync.Mutex
	ttwid   string
	ttwidAt time.Time
}

// NewDouyinClient 创建客户端。
func NewDouyinClient() *DouyinClient {
	return &DouyinClient{
		ShortBase: "https://v.douyin.com",
		WebBase:   "https://www.douyin.com",
		TtwidReg:  "https://ttwid.bytedance.com/ttwid/union/register/",
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
}

// dyDetailResp detail 接口返回结构（只取用到的字段）。
type dyDetailResp struct {
	StatusCode   int32    `json:"status_code"`
	AwemeDetail  *dyAweme `json:"aweme_detail"`
	FilterDetail struct {
		DetailMsg string `json:"detail_msg"` // 作品删除/私密等原因
	} `json:"filter_detail"`
}

// dyAweme 作品主体。media_type：0/4=视频，2=图文（此时 play_addr 是背景音乐，不能当视频发）。
type dyAweme struct {
	Desc      string `json:"desc"`
	MediaType int    `json:"media_type"`
	Duration  int64  `json:"duration"` // 毫秒
	Author    struct {
		Nickname string `json:"nickname"`
	} `json:"author"`
	Statistics struct {
		DiggCount    int64 `json:"digg_count"`
		CommentCount int64 `json:"comment_count"`
		ShareCount   int64 `json:"share_count"`
	} `json:"statistics"`
	Video struct {
		PlayAddr struct {
			URI      string   `json:"uri"`
			URLList  []string `json:"url_list"`
			DataSize int64    `json:"data_size"`
		} `json:"play_addr"`
		Ratio   string `json:"ratio"`
		BitRate []struct {
			GearName string `json:"gear_name"`
			PlayAddr struct {
				DataSize int64 `json:"data_size"`
			} `json:"play_addr"`
		} `json:"bit_rate"`
	} `json:"video"`
	Images []struct {
		URLList []string `json:"url_list"`
	} `json:"images"`
}

// Parse 解析一条抖音分享短链，返回统一的 VideoResult。
func (c *DouyinClient) Parse(ctx context.Context, link string) (*VideoResult, error) {
	id, err := c.resolveItemID(ctx, link)
	if err != nil {
		return nil, err
	}
	a, err := c.fetchDetail(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("作品 %s: %w", id, err)
	}
	if a == nil {
		return nil, fmt.Errorf("拿不到作品数据")
	}
	return a.toResult(), nil
}

// resolveItemID 跟随短链 302，从 Location 提取作品 ID。
func (c *DouyinClient) resolveItemID(ctx context.Context, link string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", dyUAShare)
	noRedirect := *c.HTTP
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		return "", fmt.Errorf("短链请求失败: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) //nolint:errcheck

	loc := resp.Header.Get("Location")
	// 个别情况下第一跳仍是短域名（如 /share/note/ 再跳一次），再跟一跳。
	if loc != "" && !dyItemIDRE.MatchString(loc) && strings.HasPrefix(loc, "http") {
		req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, loc, nil)
		req2.Header.Set("User-Agent", dyUAShare)
		resp2, err := noRedirect.Do(req2)
		if err == nil {
			loc = resp2.Header.Get("Location")
			resp2.Body.Close()
		}
	}
	m := dyItemIDRE.FindStringSubmatch(loc)
	if m == nil {
		return "", fmt.Errorf("短链跳转异常（HTTP %d，未取到作品 ID）", resp.StatusCode)
	}
	if m[1] == "" {
		m[1] = m[2] // modal_id= 分支
	}
	return m[1], nil
}

// dyMaxAttempts detail 接口对无浏览器指纹的请求有概率回 403（Argus 风控，实测约三成且成串），
// 带退避多试几轮把失败率压到百分位；仍失败还有星之阁兜底。
const dyMaxAttempts = 4

// fetchDetail 拉 detail 接口；aweme_detail 为 null 时返回错误（原因取 filter_detail），
// 403 风控抖动时退避重试（第 2 轮换新 ttwid，避免一直用同一个被标记的值）。
func (c *DouyinClient) fetchDetail(ctx context.Context, id string) (*dyAweme, error) {
	var last403 string
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 800 * time.Millisecond):
			}
		}
		ttwid, err := c.ensureTtwid(ctx, attempt == 1)
		if err != nil {
			return nil, err
		}
		body, code, err := c.get(ctx, c.WebBase+"/aweme/v1/web/aweme/detail/?aweme_id="+id+
			"&aid=6383&version_code=290100&version_name=29.1.0&device_platform=webapp", ttwid)
		if err != nil {
			return nil, err
		}
		if code == http.StatusForbidden {
			last403 = truncStr(string(body), 80)
			if attempt < dyMaxAttempts-1 {
				continue
			}
			return nil, fmt.Errorf("抖音风控拦截（HTTP 403 %s），稍后再试", last403)
		}
		var r dyDetailResp
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, fmt.Errorf("detail 响应异常（HTTP %d）: %s", code, truncStr(string(body), 120))
		}
		if r.StatusCode != 0 {
			return nil, fmt.Errorf("detail code=%d", r.StatusCode)
		}
		if r.AwemeDetail == nil {
			msg := r.FilterDetail.DetailMsg
			if msg == "" {
				msg = "作品已删除或私密"
			}
			return nil, fmt.Errorf("%s", msg)
		}
		return r.AwemeDetail, nil
	}
}

// get 发带 ttwid Cookie 的 GET，返回（body, HTTP 状态码, 错误）。
func (c *DouyinClient) get(ctx context.Context, url, ttwid string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", dyUAWeb)
	req.Header.Set("Referer", c.WebBase+"/")
	req.Header.Set("Cookie", "ttwid="+ttwid)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("请求抖音失败: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode >= 500 {
		return nil, resp.StatusCode, fmt.Errorf("抖音 HTTP %d", resp.StatusCode)
	}
	return b, resp.StatusCode, nil
}

// ensureTtwid 取缓存 ttwid；过期或 force 时重新签发。
func (c *DouyinClient) ensureTtwid(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	if !force && c.ttwid != "" && time.Since(c.ttwidAt) < dyTtwidTTL {
		t := c.ttwid
		c.mu.Unlock()
		return t, nil
	}
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TtwidReg, strings.NewReader(
		`{"region":"cn","aid":1768,"needFid":false,"service":"www.ixigua.com","migrate_info":{"ticket":"","source":"node"},"cbUrlProtocol":"https","union":true}`))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", dyUAWeb)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("签发 ttwid 失败: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) //nolint:errcheck
	for _, ck := range resp.Cookies() {
		if ck.Name == "ttwid" && ck.Value != "" {
			c.mu.Lock()
			c.ttwid, c.ttwidAt = ck.Value, time.Now()
			c.mu.Unlock()
			return ck.Value, nil
		}
	}
	return "", fmt.Errorf("ttwid 签发响应缺 Cookie（HTTP %d）", resp.StatusCode)
}

// toResult 把 aweme 映射为统一的 VideoResult。
func (a *dyAweme) toResult() *VideoResult {
	res := &VideoResult{
		Title:    a.Desc,
		Author:   a.Author.Nickname,
		Like:     a.Statistics.DiggCount,
		Comment:  a.Statistics.CommentCount,
		Share:    a.Statistics.ShareCount,
		Duration: float64(a.Duration) / 1000, // ms → s
	}
	// 图文：play_addr 是背景音乐，绝不能当视频发。
	if len(a.Images) > 0 {
		for _, img := range a.Images {
			if len(img.URLList) > 0 {
				res.Images = append(res.Images, img.URLList[0])
			}
		}
		return res
	}
	if len(a.Video.PlayAddr.URLList) > 0 {
		res.VideoURL = a.Video.PlayAddr.URLList[0]
	}
	size := a.Video.PlayAddr.DataSize
	if size == 0 && len(a.Video.BitRate) > 0 {
		size = a.Video.BitRate[0].PlayAddr.DataSize
	}
	if size > 0 {
		res.SizeStr = fmt.Sprintf("%.1fMB", float64(size)/1e6)
	}
	if len(a.Video.BitRate) > 0 {
		if m := dyGearRE.FindStringSubmatch(a.Video.BitRate[0].GearName); m != nil {
			res.Quality = m[1] + "p"
		}
	}
	if res.Quality == "" && a.Video.Ratio != "" && a.Video.Ratio != "default" {
		res.Quality = a.Video.Ratio
	}
	return res
}
