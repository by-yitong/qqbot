package imgstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Host 是远端图床客户端：把图片字节上传到图床服务，拿回一个公网 URL。
// 配合 QQ 富媒体 URL 模式使用（QQ 自行下载该 URL）。
// BaseURL 为空表示未配置图床（调用方应回退到进程内 Store 的 genimg: 引用）。
type Host struct {
	BaseURL string       // 图床公网地址，如 https://img.example.com（不要带末尾 /）
	Token   string       // 上传鉴权 token（与图床服务的 IMGHOST_TOKEN 一致）
	HTTP    *http.Client // nil 则用默认 30s 客户端
}

// Configured 报告图床是否已配置（BaseURL 非空）。
func (h *Host) Configured() bool {
	return h != nil && strings.TrimSpace(h.BaseURL) != ""
}

// Upload 把图片字节上传到图床，返回公网 URL。ext 可带或不带点（空默认 png）。
func (h *Host) Upload(ctx context.Context, data []byte, ext string) (string, error) {
	if !h.Configured() {
		return "", fmt.Errorf("图床未配置（IMGHOST_BASE_URL 为空）")
	}
	ext = normalizeExt(ext)
	endpoint := strings.TrimRight(h.BaseURL, "/") + "/upload?ext=" + ext

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if h.Token != "" {
		req.Header.Set("Authorization", "Bearer "+h.Token)
	}
	client := h.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("上传图床失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("图床 HTTP %d: %s", resp.StatusCode, truncStr(string(body), 300))
	}
	var res struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return "", fmt.Errorf("解析图床响应失败: %w", err)
	}
	if res.URL == "" {
		return "", fmt.Errorf("图床响应无 url: %s", truncStr(string(body), 300))
	}
	return res.URL, nil
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
