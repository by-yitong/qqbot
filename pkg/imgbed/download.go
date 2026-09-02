package imgbed

// 下载与文件名推断的共用 helper：「/图床」插件与 agent 的 upload_image 工具都用，
// 收口在这里避免两边各养一份限流/推断逻辑。

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultMaxBytes 是单图的默认大小上限（10MB）：QQ 聊天图几乎都小于它，
// 也远低于图床 50MB 硬上限（公开 bot 上任意人可传，收紧些防滥用）。
const DefaultMaxBytes = 10 << 20

// DownloadImage 下载一张图片的字节（QQ CDN 附件等短时效 URL，bot 侧直接拉最稳，
// 不能交给图床的 URL 转存——第三方服务器拉腾讯 CDN 可能被防盗链拒）。
// LimitReader 硬限 maxBytes+1：超限直接报错，不把超大文件整个读进内存；
// maxBytes<=0 用 DefaultMaxBytes；client 为 nil 用默认 30s。
func DownloadImage(ctx context.Context, imageURL string, maxBytes int64, client *http.Client) ([]byte, string, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("非法图片 URL: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("下载图片失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("下载图片 HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("下载图片失败: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, "", fmt.Errorf("图片超过 %s 上限", HumanBytes(maxBytes))
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// FilenameFor 推断上传文件名：hint（如 QQ 附件的 filename）非空直接用；
// 否则按 contentType 推扩展名拼成 image.<ext>，全空兜底 image.png。
func FilenameFor(hint, contentType string) string {
	if n := strings.TrimSpace(hint); n != "" {
		return n
	}
	ext := "png"
	switch ct := strings.ToLower(contentType); {
	case strings.Contains(ct, "jpeg"), strings.Contains(ct, "jpg"):
		ext = "jpg"
	case strings.Contains(ct, "gif"):
		ext = "gif"
	case strings.Contains(ct, "webp"):
		ext = "webp"
	case strings.Contains(ct, "bmp"):
		ext = "bmp"
	}
	return "image." + ext
}

// HumanBytes 1048576 → 1MB（上限提示语用）。
func HumanBytes(n int64) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%.0fMB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.0fKB", float64(n)/(1<<10))
}
