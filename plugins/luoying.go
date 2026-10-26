// luoying.work 视频解析 API 客户端（抖音/快手，2026-10 接入，替代直连/星之阁/zsz 方案），
// 以 Parser 身份接入 VideoPlugin 的解析链（plugins/videoparser.go）。
//
//	GET {base}/api/douyin/info?keyword=<分享短链>    Authorization: Bearer <key>
//	GET {base}/api/kuaishou/info?keyword=<分享短链>
//
// 统一外层 {code, message, error, data}，code=200 成功。已知失败形态：
// 429 限频（匿名组 1 次/分钟）；502 上游解析抖动（实测快手偶发）。
// 多源时代快速失败：fetch 一次失败直接返回错误，抖动重试与故障转移交给 ParserChain。
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
	"strings"
	"time"
)

// LuoyingClient 是 apiserver.luoying.work 解析客户端，并发安全（无状态）。
type LuoyingClient struct {
	Base   string // API 基址
	APIKey string // Bearer 鉴权 key
	HTTP   *http.Client
}

// NewLuoyingClient 从环境变量创建（LUOYING_API_BASE 可覆盖基址，LUOYING_API_KEY 必配）。
func NewLuoyingClient() *LuoyingClient {
	base := os.Getenv("LUOYING_API_BASE")
	if base == "" {
		base = "https://apiserver.luoying.work"
	}
	return &LuoyingClient{
		Base:   base,
		APIKey: os.Getenv("LUOYING_API_KEY"),
		HTTP:   &http.Client{Timeout: 60 * time.Second},
	}
}

// Name 实现 Parser 接口。
func (c *LuoyingClient) Name() string { return "luoying" }

// Parse 按链接域名分流平台，实现 Parser 接口；不认识的域名返回
// ErrUnsupportedPlatform，把机会让给链上的下一个源。
func (c *LuoyingClient) Parse(ctx context.Context, link string) (*VideoResult, error) {
	switch {
	case strings.Contains(link, "douyin.com"):
		return c.ParseDouyin(ctx, link)
	case strings.Contains(link, "kuaishou.com"):
		return c.ParseKuaishou(ctx, link)
	default:
		return nil, ErrUnsupportedPlatform
	}
}

// NewLuoyingParserFromEnv 从环境变量装配 luoying 解析源；未配 key 返回 nil
// （orderParsers 会剔除 nil，装配处无需判空）。
func NewLuoyingParserFromEnv() Parser {
	if os.Getenv("LUOYING_API_KEY") == "" {
		return nil
	}
	return NewLuoyingClient()
}

// lyResp 统一外层。
type lyResp struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
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

// ParseDouyin 解析抖音分享链接，返回统一的 VideoResult。
func (c *LuoyingClient) ParseDouyin(ctx context.Context, link string) (*VideoResult, error) {
	return c.parse(ctx, "/api/douyin/info", link)
}

// ParseKuaishou 解析快手分享链接。
func (c *LuoyingClient) ParseKuaishou(ctx context.Context, link string) (*VideoResult, error) {
	return c.parse(ctx, "/api/kuaishou/info", link)
}

// parse 只 fetch 一次（快速失败）；失败转移由 ParserChain 处理，不在源内重试。
func (c *LuoyingClient) parse(ctx context.Context, path, link string) (*VideoResult, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("未配置 LUOYING_API_KEY，解析接口不可用")
	}
	item, err := c.fetch(ctx, path, link)
	if err != nil {
		return nil, err
	}
	return item.toResult(), nil
}

// fetch 调一次接口，任何失败立即返回错误（不重试）。
func (c *LuoyingClient) fetch(ctx context.Context, path, link string) (*lyItem, error) {
	u := c.Base + path + "?keyword=" + url.QueryEscape(link)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求解析接口失败: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var r lyResp
	if json.Unmarshal(b, &r) != nil {
		return nil, fmt.Errorf("解析接口响应异常（HTTP %d）: %s", resp.StatusCode, truncStr(string(b), 120))
	}
	if r.Code != 200 {
		msg := r.Error
		if msg == "" {
			msg = r.Message
		}
		if r.Code == http.StatusTooManyRequests {
			return nil, fmt.Errorf("解析接口限频: %s", truncStr(msg, 80))
		}
		return nil, fmt.Errorf("解析失败（code=%d）: %s", r.Code, truncStr(msg, 120))
	}
	if len(r.Data) == 0 {
		return nil, fmt.Errorf("解析接口未返回数据")
	}
	var item lyItem
	if err := json.Unmarshal(r.Data, &item); err != nil {
		return nil, fmt.Errorf("解析结果格式异常: %w", err)
	}
	return &item, nil
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
