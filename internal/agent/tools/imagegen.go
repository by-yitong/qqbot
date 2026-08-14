package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"qqbot/internal/imgstore"

	"github.com/tidwall/gjson"
)

// 默认端点（OpenAI 官方）。第三方 OpenAI 兼容网关用 BaseURL 覆盖。
const defaultImageBaseURL = "https://api.openai.com/v1"

// ImageGen 是 OpenAI 协议的生图客户端：文生图（/images/generations）+ 图生图（/images/edits）。
// 兼容只回 b64_json 的网关：base64 解码后——若配了 ImageHost 则上传图床拿公网 URL，
// 否则存进程内图床返回短引用 genimg:..（仅本地，QQ 发不出）。
type ImageGen struct {
	APIKey    string
	BaseURL   string         // 默认 https://api.openai.com/v1
	Model     string         // 如 gpt-image-2 / gpt-image-1
	HTTP      *http.Client   // nil 则用默认 60s 客户端
	Timeout   time.Duration  // 默认 60s（生图比普通请求慢）
	ImageHost *imgstore.Host // 可选：上传图床拿公网 URL（QQ 发图必需）
}

func (g *ImageGen) base() string {
	if g.BaseURL != "" {
		return strings.TrimRight(g.BaseURL, "/")
	}
	return defaultImageBaseURL
}

func (g *ImageGen) model() string {
	if g.Model != "" {
		return g.Model
	}
	return "gpt-image-1"
}

func (g *ImageGen) timeout() time.Duration {
	if g.Timeout > 0 {
		return g.Timeout
	}
	return 120 * time.Second // 生图常要 30~90s（实测 31s/65s），60s 太紧会误报超时
}

func (g *ImageGen) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return &http.Client{Timeout: g.timeout()}
}

// Generate 文生图。返回 (图片引用, revisedPrompt, err)。
// 引用可能是 genimg:<id>.<ext>（网关回 base64 时）或 http(s) URL（网关回 url 时）。
func (g *ImageGen) Generate(ctx context.Context, prompt, size string) (string, string, error) {
	body := map[string]any{"model": g.model(), "prompt": prompt, "n": 1}
	if size != "" {
		body["size"] = size
	}
	raw, err := g.postJSON(ctx, g.base()+"/images/generations", body)
	if err != nil {
		return "", "", err
	}
	return g.extractImage(ctx, raw)
}

// Edit 图生图。inputURL 是输入图（http(s) URL 或 genimg: 引用）。
func (g *ImageGen) Edit(ctx context.Context, prompt, size, inputURL string) (string, string, error) {
	data, _, err := fetchImageBytes(ctx, inputURL)
	if err != nil {
		return "", "", fmt.Errorf("读取输入图失败: %w", err)
	}
	raw, err := g.postMultipart(ctx, g.base()+"/images/edits", prompt, size, data)
	if err != nil {
		return "", "", err
	}
	return g.extractImage(ctx, raw)
}

// extractImage 解析生图响应：
//   - 网关回 url：直接返回（若已是公网 URL，QQ 可下载）。
//   - 网关回 b64_json：解码 → 本地归档（imgstore.SaveDir）→ 若配了 ImageHost 上传拿公网 URL，
//     否则返回进程内 genimg: 引用（仅本地，QQ 发不出）。
func (g *ImageGen) extractImage(ctx context.Context, raw []byte) (string, string, error) {
	if msg := gjson.GetBytes(raw, "error.message").String(); msg != "" {
		return "", "", fmt.Errorf("生图 API 错误: %s", msg)
	}
	d0 := gjson.GetBytes(raw, "data.0")
	if !d0.Exists() {
		return "", "", fmt.Errorf("生图响应无 data: %s", truncateBody(raw))
	}
	revised := d0.Get("revised_prompt").String()
	if u := d0.Get("url").String(); u != "" {
		return u, revised, nil
	}
	if b64 := d0.Get("b64_json").String(); b64 != "" {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return "", "", fmt.Errorf("解码图片失败: %w", err)
		}
		ext := strings.ToLower(gjson.GetBytes(raw, "output_format").String())
		// 本地归档（best-effort；SaveDir 配了就落盘，ref 仅在无图床时用）。
		localRef := imgstore.Default().Put(data, ext)
		if g.ImageHost != nil && g.ImageHost.Configured() {
			url, uerr := g.ImageHost.Upload(ctx, data, ext)
			if uerr != nil {
				return "", "", fmt.Errorf("上传图床失败: %w", uerr)
			}
			return url, revised, nil
		}
		return localRef, revised, nil
	}
	return "", "", fmt.Errorf("生图响应无 url/b64_json: %s", truncateBody(raw))
}

// postJSON 发 JSON 请求并返回响应体。
func (g *ImageGen) postJSON(ctx context.Context, endpoint string, body map[string]any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.APIKey)
	return g.do(req)
}

// postMultipart 发 multipart 请求（图生图：image 字段为文件）。
func (g *ImageGen) postMultipart(ctx context.Context, endpoint, prompt, size string, imageData []byte) ([]byte, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("model", g.model())
	_ = mw.WriteField("prompt", prompt)
	_ = mw.WriteField("n", "1")
	if size != "" {
		_ = mw.WriteField("size", size)
	}
	fw, err := mw.CreateFormFile("image", "input.png")
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(imageData); err != nil {
		return nil, err
	}
	mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.APIKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return g.do(req)
}

func (g *ImageGen) do(req *http.Request) ([]byte, error) {
	resp, err := g.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求生图失败: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("生图 HTTP %d: %s", resp.StatusCode, truncateBody(data))
	}
	return data, nil
}

// fetchImageBytes 取图片字节：genimg: 引用从图床取，否则 HTTP GET。
func fetchImageBytes(ctx context.Context, url string) ([]byte, string, error) {
	if data, ct, _, ok := imgstore.Default().Resolve(url); ok {
		return data, ct, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("非法 URL: %w", err)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("下载输入图失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("下载输入图 HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// --- generate_image 工具 ---

// GenerateImage 是 AI 可调的生图工具（文生图/图生图）。
type GenerateImage struct{ Owner *ImageGen }

// Name 返回工具名。
func (t *GenerateImage) Name() string { return "generate_image" }

// Description 返回工具描述。
func (t *GenerateImage) Description() string {
	return "用 AI 生成图片（文生图 / 图生图）。文生图：给 prompt；图生图：再给 image_url（输入图 URL 或 genimg: 引用）。" +
		"返回图片引用（genimg:.. 或 URL）与模型改写的 prompt；需再调 send_image 发给用户。"
}

// Parameters 返回参数 JSON Schema。
func (t *GenerateImage) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt":    map[string]any{"type": "string", "description": "图片描述，越具体越好（主体、风格、细节）"},
			"image_url": map[string]any{"type": "string", "description": "图生图时的输入图（http(s) URL 或 genimg: 引用）；不填=文生图"},
			"size":      map[string]any{"type": "string", "description": "尺寸：1024x1024 / 1024x1536 / 1536x1024，默认 1024x1024"},
		},
		"required": []string{"prompt"},
	}
}

// Guidelines 贡献使用要点（教模型组合 生图→发图 这条链）。
func (t *GenerateImage) Guidelines() []string {
	return []string{
		"用户要生成/画/做图时：调 generate_image(prompt)（基于已有图改再传 image_url），拿到图片引用后调 send_image(url=引用) 发给用户",
	}
}

// Execute 执行生图。
func (t *GenerateImage) Execute(ctx context.Context, arguments string) (string, error) {
	prompt := gjson.Get(arguments, "prompt").String()
	if prompt == "" {
		return "", fmt.Errorf("缺少 prompt")
	}
	size := gjson.Get(arguments, "size").String()
	if size == "" {
		size = "1024x1024"
	}
	inputURL := gjson.Get(arguments, "image_url").String()

	var ref, revised string
	var err error
	if inputURL != "" {
		ref, revised, err = t.Owner.Edit(ctx, prompt, size, inputURL)
	} else {
		ref, revised, err = t.Owner.Generate(ctx, prompt, size)
	}
	if err != nil {
		return "", err
	}
	out := "已生成图片，引用：" + ref + "。用 send_image(url=\"" + ref + "\") 发送给用户"
	if revised != "" && revised != prompt {
		out += "（模型改写 prompt：" + revised + "）"
	}
	return out, nil
}
