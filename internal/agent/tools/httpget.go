// Package tools 提供 agent 的内置工具。
//
// 这里都是「安全」工具（http_get 带 SSRF 守卫、current_time）。
// 危险工具（bash / 文件读写）默认不在此提供，需按 env 开关 + 白名单另行启用。
package tools

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"resty.dev/v3"
)

// HTTPGet 是带 SSRF 守卫的 HTTP GET 工具。
//
// 仅允许 http/https；默认拒绝 loopback / 私网 / 链路本地等内网地址
// （通过 Allow 可显式放行指定内网 host）。
type HTTPGet struct {
	HTTP    *resty.Client // 为 nil 则用默认 10s 客户端
	Timeout time.Duration // 默认 10s
	Allow   []string      // 额外允许的 host（如内网服务），命中即跳过私网检查
}

// Name 返回工具名。
func (h *HTTPGet) Name() string { return "http_get" }

// Description 返回工具描述。
func (h *HTTPGet) Description() string {
	return "发起 HTTP GET 请求并返回响应体文本（仅 http/https；默认拒绝内网/回环地址）。适合查天气、查公开 API 等。"
}

// Parameters 返回参数 JSON Schema。
func (h *HTTPGet) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "要请求的完整 http(s) URL",
			},
		},
		"required": []string{"url"},
	}
}

// Guidelines 贡献使用要点。
func (h *HTTPGet) Guidelines() []string {
	return []string{
		"http_get 默认拒绝内网/回环地址（SSRF 守卫），只能请求公网 http(s)",
	}
}

// Execute 执行请求。HTTP 4xx/5xx 不算错误（返回状态码+截断正文供模型判断），仅网络/解析错误返回 err。
func (h *HTTPGet) Execute(ctx context.Context, arguments string) (string, error) {
	u := gjson.Get(arguments, "url").String()
	if u == "" {
		return "", fmt.Errorf("缺少 url 参数")
	}
	if err := assertSafeURL(u, h.Allow); err != nil {
		return "", err
	}
	client := h.HTTP
	if client == nil {
		client = resty.New().SetTimeout(10*time.Second).SetHeader("User-Agent", "qqbot/1.0")
	}
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := client.R().SetContext(reqCtx).Get(u)
	if err != nil {
		return "", fmt.Errorf("请求失败: %w", err)
	}
	if resp.IsStatusFailure() {
		return fmt.Sprintf("HTTP %d: %s", resp.StatusCode(), truncate(resp.String(), 500)), nil
	}
	return truncate(resp.String(), 2000), nil
}

// assertSafeURL 校验 url 只允许 http/https，且主机不在内网/回环（除非在 allow 里）。
func assertSafeURL(raw string, allow []string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("非法 URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("只允许 http/https，得到 %q", u.Scheme)
	}
	host := u.Hostname()
	for _, a := range allow {
		if strings.EqualFold(host, a) {
			return nil
		}
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("域名解析失败: %w", err)
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
			return fmt.Errorf("拒绝访问内网/回环地址 %s", ip)
		}
	}
	return nil
}

// truncate 把字符串按 rune 数截断，超出则追加提示。
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + fmt.Sprintf("\n...(已截断，共 %d 字符)", len(r))
}
