// Package imgbed 是 imgbed 图床服务（如 https://imgbed.jmwl.dpdns.org）的 API 客户端：
// 上传（multipart / URL 转存）、列表、删除。所有接口需 Bearer token 鉴权；
// 外链 GET /i/{id} 无需鉴权，直接引用返回的 URL 即可（支持 <img> 外链）。
package imgbed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

// File 是图床上一个文件的元信息（/api/upload 返回与 /api/images 的数组元素）。
type File struct {
	ID       string `json:"id"`
	URL      string `json:"url"`      // 公网外链，如 https://imgbed.jmwl.dpdns.org/i/abc123
	Filename string `json:"filename"` // 上传时的文件名
	Size     int64  `json:"size"`     // 字节（/api/upload/url 转存可能不带）
}

// Client 是图床 API 客户端（无状态，并发安全）。
type Client struct {
	BaseURL string       // 服务地址，如 https://imgbed.jmwl.dpdns.org（末尾 / 可有可无）
	Token   string       // 访问令牌（Authorization: Bearer <token>）
	HTTP    *http.Client // nil 用默认 60s（上传大文件要留足）
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// Upload 以 multipart/form-data 上传文件字节（POST /api/upload，字段名 file）。
// filename 为空默认 image.png；contentType 为空默认 application/octet-stream。
func (c *Client) Upload(ctx context.Context, filename, contentType string, data []byte) (*File, error) {
	if filename == "" {
		filename = "image.png"
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	// CreateFormFile 不支持自定义 part 的 Content-Type，手工建 part header。
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition",
		fmt.Sprintf(`form-data; name="file"; filename="%s"`, strings.ReplaceAll(filename, `"`, "'")))
	h.Set("Content-Type", contentType)
	part, err := w.CreatePart(h)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	var out File
	if err := c.do(ctx, http.MethodPost, "/api/upload", w.FormDataContentType(), &buf, &out); err != nil {
		return nil, err
	}
	if out.URL == "" {
		return nil, fmt.Errorf("图床响应无 url（id=%s）", out.ID)
	}
	return &out, nil
}

// UploadURL 把一个公网 URL 的文件转存到图床（POST /api/upload/url，由图床侧拉取）。
func (c *Client) UploadURL(ctx context.Context, fileURL string) (*File, error) {
	payload, err := json.Marshal(map[string]string{"url": fileURL})
	if err != nil {
		return nil, err
	}
	var out File
	if err := c.do(ctx, http.MethodPost, "/api/upload/url", "application/json", bytes.NewReader(payload), &out); err != nil {
		return nil, err
	}
	if out.URL == "" {
		return nil, fmt.Errorf("图床响应无 url（id=%s）", out.ID)
	}
	return &out, nil
}

// List 列出图床上的文件（GET /api/images）。
func (c *Client) List(ctx context.Context) ([]File, error) {
	var out []File
	if err := c.do(ctx, http.MethodGet, "/api/images", "", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Delete 按 id 删除文件（DELETE /api/images/{id}）。
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/images/"+url.PathEscape(id), "", nil, nil)
}

// do 发一次带鉴权的 API 请求：method+path、可选 body 与其 Content-Type，
// 2xx 时把响应 JSON 解析到 out（out 为 nil 则丢弃响应体）。
func (c *Client) do(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("请求图床失败: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("图床 HTTP %d: %s", resp.StatusCode, truncStr(string(data)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("解析图床响应失败: %w（%s）", err, truncStr(string(data)))
	}
	return nil
}

func truncStr(s string) string {
	if len(s) > 300 {
		return s[:300]
	}
	return s
}
