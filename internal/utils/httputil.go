package utils

import (
	"context"
	"fmt"
	"time"

	"resty.dev/v3"
)

// HttpClient 是共享的 HTTP 客户端：复用连接池，统一超时与 User-Agent。
// 简单 GET 直接用 GetJSON/GetText；需要参数/headers/POST 等复杂请求时，
// 用 HttpClient.R()... 拿到全部 resty 能力。
//
//	var w weatherResp
//	_, err := utils.HttpClient.R().
//	    SetContext(ctx).
//	    SetQueryParam("city", "北京").
//	    SetResult(&w).
//	    Get("https://api.example.com/weather")
var HttpClient = resty.New().
	SetTimeout(10*time.Second).
	SetHeader("User-Agent", "qqbot/1.0")

// GetJSON 发起 GET 请求，并把响应 JSON 自动解析进 out（out 须为指针）。
// 网络错误或 HTTP 状态码 >= 400 时返回 error。
//
//	var h SomeResp
//	if err := utils.GetJSON(ctx, "https://x.com/api", &h); err != nil { ... }
func GetJSON(ctx context.Context, url string, out any) error {
	resp, err := HttpClient.R().
		SetContext(ctx).
		SetResult(out).
		Get(url)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	if resp.IsStatusFailure() {
		return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode())
	}
	return nil
}

// GetText 发起 GET 请求，返回响应体文本（适合纯文本接口）。
func GetText(ctx context.Context, url string) (string, error) {
	resp, err := HttpClient.R().SetContext(ctx).Get(url)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", url, err)
	}
	if resp.IsStatusFailure() {
		return "", fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode())
	}
	return resp.String(), nil
}
