package imgstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Host 是图床客户端：把文件字节变成一个 QQ 可拉取的公网 URL。两种模式：
//   - 本地直存（Dir 非空）：写文件到 Dir/<id>.<ext>，返回 BaseURL/<文件名>。
//     适合 bot 与 web 服务同机部署（如公网服务器上 nginx 直接映射该目录），
//     此时 Token 不需要，BaseURL 填该目录的公网访问地址。
//   - 远端上传（Dir 为空）：HTTP POST 到图床服务（cmd/imghost），向后兼容。
//
// BaseURL 为空表示未配置图床（调用方应回退到进程内 Store 的 genimg: 引用）。
type Host struct {
	BaseURL string       // 公网基础 URL，如 https://img.example.com（不要带末尾 /）
	Token   string       // 远端模式上传鉴权 token；本地直存模式不需要
	Dir     string       // 本地直存目录（如 ./data/files）；非空启用本地直存模式
	HTTP    *http.Client // 远端模式用；nil 则用默认 30s 客户端
}

// Configured 报告图床是否已配置（BaseURL 非空）。
func (h *Host) Configured() bool {
	return h != nil && strings.TrimSpace(h.BaseURL) != ""
}

// Upload 把文件字节变成公网 URL：本地直存模式写盘，远端模式 POST 图床。
// ext 可带或不带点（空默认 png）。文件永久保留（QQ 拉取时间不定），不做 TTL。
func (h *Host) Upload(ctx context.Context, data []byte, ext string) (string, error) {
	if !h.Configured() {
		return "", fmt.Errorf("图床未配置（IMGHOST_BASE_URL 为空）")
	}
	ext = normalizeExt(ext)
	if h.Dir != "" {
		return h.saveLocal(data, ext)
	}
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

// saveLocal 本地直存：写 Dir/<id>.<ext>，返回 BaseURL/<文件名>。
func (h *Host) saveLocal(data []byte, ext string) (string, error) {
	if err := os.MkdirAll(h.Dir, 0o755); err != nil {
		return "", fmt.Errorf("创建直存目录 %s 失败: %w", h.Dir, err)
	}
	name := randID() + "." + ext
	if err := os.WriteFile(filepath.Join(h.Dir, name), data, 0o644); err != nil {
		return "", fmt.Errorf("直存写盘失败: %w", err)
	}
	return strings.TrimRight(h.BaseURL, "/") + "/" + name, nil
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
