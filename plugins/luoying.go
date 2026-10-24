// luoying.work 视频解析 API 客户端（抖音/快手，2026-10 接入，替代直连/星之阁/zsz 方案）。
//
//	GET {base}/api/douyin/info?keyword=<分享短链>    Authorization: Bearer <key>
//	GET {base}/api/kuaishou/info?keyword=<分享短链>
//
// 统一外层 {code, message, error, data}，code=200 成功。已知失败形态：
// 429 限频（匿名组 1 次/分钟，details.retry_after 给出建议等待秒数）；
// 502 上游解析抖动（实测快手偶发，同链接重试即穿过）。
package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

// LuoyingClient 是 apiserver.luoying.work 解析客户端，并发安全（无状态）。
type LuoyingClient struct {
	Base    string // API 基址
	APIKey  string // Bearer 鉴权 key
	Backoff time.Duration
	HTTP    *http.Client
}

// NewLuoyingClient 从环境变量创建（LUOYING_API_BASE 可覆盖基址，LUOYING_API_KEY 必配）。
func NewLuoyingClient() *LuoyingClient {
	base := os.Getenv("LUOYING_API_BASE")
	if base == "" {
		base = "https://apiserver.luoying.work"
	}
	return &LuoyingClient{
		Base:    base,
		APIKey:  os.Getenv("LUOYING_API_KEY"),
		Backoff: 2 * time.Second,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

// lyResp 统一外层。
type lyResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Error   string `json:"error"`
	Details struct {
		RetryAfter int `json:"retry_after"` // 429 建议等待秒数
	} `json:"details"`
	Data json.RawMessage `json:"data"`
}

// lyItem data 字段（抖音/快手共用；快手无 author/formats，图集无 videos）。
// formats 的 width/height 接口返回的是字符串数字，用 json.Number 兼容两种。
type lyItem struct {
	Title   string   `json:"title"`
	Desc    string   `json:"desc"`
	Author  string   `json:"author"`
	Videos  []string `json:"videos"`
	Images  []string `json:"images"`
	Formats []struct {
		URL    string      `json:"url"`
		Width  json.Number `json:"width"`
		Height json.Number `json:"height"`
	} `json:"formats"`
}

// lyMaxAttempts 429/502 都可能是抖动，多试两轮；仍失败如实报错。
const lyMaxAttempts = 3

// ParseDouyin 解析抖音分享链接，返回统一的 VideoResult。
func (c *LuoyingClient) ParseDouyin(ctx context.Context, link string) (*VideoResult, error) {
	return c.parse(ctx, "/api/douyin/info", link)
}

// ParseKuaishou 解析快手分享链接。
func (c *LuoyingClient) ParseKuaishou(ctx context.Context, link string) (*VideoResult, error) {
	return c.parse(ctx, "/api/kuaishou/info", link)
}

func (c *LuoyingClient) parse(ctx context.Context, path, link string) (*VideoResult, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("未配置 LUOYING_API_KEY，解析接口不可用")
	}
	var lastErr error
	for attempt := 1; ; attempt++ {
		item, retryAfter, err := c.fetch(ctx, path, link)
		if err == nil {
			return item.toResult(), nil
		}
		lastErr = err
		if attempt >= lyMaxAttempts {
			break
		}
		wait := time.Duration(attempt) * c.Backoff
		if retryAfter > 0 { // 429：按接口建议等，封顶 30s 免得回复拖太久
			wait = min(time.Duration(retryAfter)*time.Second, 30*time.Second)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil, lastErr
}

// fetch 调一次接口。返回 (结果, 429 建议等待秒数, 错误)。
func (c *LuoyingClient) fetch(ctx context.Context, path, link string) (*lyItem, int, error) {
	u := c.Base + path + "?keyword=" + url.QueryEscape(link)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("请求解析接口失败: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, 0, err
	}
	var r lyResp
	if json.Unmarshal(b, &r) != nil {
		return nil, 0, fmt.Errorf("解析接口响应异常（HTTP %d）: %s", resp.StatusCode, truncStr(string(b), 120))
	}
	if r.Code != 200 {
		msg := r.Error
		if msg == "" {
			msg = r.Message
		}
		if r.Code == http.StatusTooManyRequests {
			return nil, r.Details.RetryAfter, fmt.Errorf("解析接口限频: %s", truncStr(msg, 80))
		}
		return nil, 0, fmt.Errorf("解析失败（code=%d）: %s", r.Code, truncStr(msg, 120))
	}
	if len(r.Data) == 0 {
		return nil, 0, fmt.Errorf("解析接口未返回数据")
	}
	var item lyItem
	if err := json.Unmarshal(r.Data, &item); err != nil {
		return nil, 0, fmt.Errorf("解析结果格式异常: %w", err)
	}
	return &item, 0, nil
}

// toResult 映射为统一的 VideoResult。图集（images 非空）时清掉视频地址：
// 抖音图集的 videos 是背景音乐，不能当视频发。
func (it *lyItem) toResult() *VideoResult {
	res := &VideoResult{Title: it.Title, Author: it.Author, Images: it.Images}
	if res.Title == "" {
		res.Title = it.Desc
	}
	if len(it.Images) > 0 {
		return res
	}
	if len(it.Videos) > 0 {
		res.VideoURL = it.Videos[0]
	} else if len(it.Formats) > 0 {
		res.VideoURL = it.Formats[0].URL
	}
	if len(it.Formats) > 0 {
		if h, err := it.Formats[0].Height.Int64(); err == nil && h > 0 {
			res.Quality = strconv.FormatInt(h, 10) + "p"
		}
	}
	return res
}
