package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"qqbot/internal/imgstore"
	"qqbot/pkg/imgbed"
)

// Responder 让 agent 工具在对话中产生「文本回复以外」的副作用（如发图片）。
// 由调用方（plugin）绑定到当前会话目标实现，经 WithResponder 传入本次 Run。
type Responder interface {
	// SendImage 向当前会话发送一张图片（由 url 指定）。
	SendImage(ctx context.Context, url string) error
}

// RunOption 配置单次 Run。
type RunOption func(*runOptions)

type runOptions struct {
	responder  Responder
	ctxBlock   string   // 动态上下文块：临时消息插在问题前，不落 session
	persistBlk string   // 持久补充块（如增量群聊记录）：普通 user 消息插在问题前，照常落盘
	images     []string // 本次问题携带的图片 URL：以 image_url 形式发给多模态模型
}

// WithResponder 为本次 Run 绑定一个 responder（工具可经 ResponderFrom 取用）。
func WithResponder(r Responder) RunOption {
	return func(o *runOptions) { o.responder = r }
}

// WithContextBlock 注入动态上下文块：作为临时 user 消息插在本次问题紧前面发给
// LLM（不落 session）。放请求尾部而非 system prompt——system+历史 前缀保持稳定，
// LLM 上下文缓存才能整段命中。
func WithContextBlock(s string) RunOption {
	return func(o *runOptions) { o.ctxBlock = s }
}

// WithPersistBlock 注入一段持久补充块（如增量群聊记录）：作为普通 user 消息插在
// 本次问题紧前面，**随 session 落盘**——下轮自动随历史加载，不必重复注入。
// 前缀照样单调增长（缓存不受影响）；session 因此长得更快，由上下文压缩兜底。
func WithPersistBlock(s string) RunOption {
	return func(o *runOptions) { o.persistBlk = s }
}

// WithImages 为本次问题附加图片（多模态输入）：以 OpenAI image_url content 形式
// 随问题消息发给 LLM（需 AI_MODEL 支持视觉）。图片 URL 随 session 落盘。
func WithImages(urls []string) RunOption {
	return func(o *runOptions) { o.images = urls }
}

type responderKey struct{}

// ResponderFrom 从 ctx 取 responder（未绑定返回 nil）。
func ResponderFrom(ctx context.Context) Responder {
	if r, ok := ctx.Value(responderKey{}).(Responder); ok {
		return r
	}
	return nil
}

// sendImageTool 让 AI 自主发图片：给图 URL，经 responder 发到当前会话。
type sendImageTool struct{}

// Name 返回工具名。
func (t *sendImageTool) Name() string { return "send_image" }

// Description 返回工具描述。
func (t *sendImageTool) Description() string {
	return "向当前会话发送一张图片。参数可以是图片直链 URL（http/https），也可以是本地文件路径（如浏览器截图工具返回的保存路径，会自动上传图床后发送）。"
}

// Parameters 返回参数 JSON Schema。
func (t *sendImageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{"type": "string", "description": "图片直链 URL（http/https）或本地文件路径（如截图工具返回的 /tmp/xxx.png）"},
		},
		"required": []string{"url"},
	}
}

// Guidelines 贡献使用要点（教模型组合 search→发图这条链）。
func (t *sendImageTool) Guidelines() []string {
	return []string{
		"用户想要图片/表情图时：先用 web_search 搜到图片直链 URL（jpg/png/gif/webp），再调 send_image 发送，不要自己编 URL",
		"截图类工具（如 mcp_agent-browser_screenshot）返回保存路径后，可直接把路径传给 send_image 发给用户",
	}
}

// Execute 经 responder 发图；无 responder 则返回提示（不报错，让模型知晓）。
func (t *sendImageTool) Execute(ctx context.Context, arguments string) (string, error) {
	var p struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(arguments), &p); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	if p.URL == "" {
		return "", fmt.Errorf("缺少 url 参数")
	}
	r := ResponderFrom(ctx)
	if r == nil {
		return "当前会话不支持发图", nil
	}
	if err := r.SendImage(ctx, p.URL); err != nil {
		return fmt.Sprintf("发图失败: %v", err), nil // 失败也作为信息回给模型，不中断循环
	}
	return "图片已发送", nil
}

// --- 图床上传（upload_image）---

type imagesKey struct{}

// ImagesFromContext 从 ctx 取本次用户输入携带的图片 URL 列表（未注入返回 nil）。
// Run 会把 WithImages 传入的 URL 注入工具执行的 ctx——多模态请求里模型只「看图」
// 看不到 URL 字符串，所以 upload_image 靠它拿到「用户刚发的那张图」，无需模型传参。
func ImagesFromContext(ctx context.Context) []string {
	if imgs, ok := ctx.Value(imagesKey{}).([]string); ok {
		return imgs
	}
	return nil
}

// UploadImageTool 让 AI 把图片上传到图床换永久公网外链。
// 与 plugins 的「/图床」命令共用同一个 imgbed.Client（main.go 装配）。
type UploadImageTool struct {
	Cli      *imgbed.Client // 图床客户端；nil = 未配置（Execute 返回提示）
	MaxBytes int64          // 单图上限；<=0 用 imgbed.DefaultMaxBytes
	HTTP     *http.Client   // 下载图用；nil 用 imgbed 包默认
}

// Name 返回工具名。
func (t *UploadImageTool) Name() string { return "upload_image" }

// Description 返回工具描述。
func (t *UploadImageTool) Description() string {
	return "把图片上传到图床，返回永久公网外链（可直接在群/网页/Markdown 里引用）。" +
		"用户发图片让你『传图床/要外链/存成链接』时调用；不传 image_url 则上传用户当前消息里的图片。"
}

// Parameters 返回参数 JSON Schema。
func (t *UploadImageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"image_url": map[string]any{"type": "string", "description": "要上传的图片 URL 或 genimg: 引用（generate_image 的产物）；不填=上传用户当前消息里的全部图片"},
		},
	}
}

// Guidelines 贡献使用要点（教模型「用户发图要外链」这条链）。
func (t *UploadImageTool) Guidelines() []string {
	return []string{
		"用户发图并要求『传图床 / 给外链 / 存成链接』时：直接调 upload_image（可不带参数，默认上传用户刚发的图），把返回的外链原样发给用户",
		"generate_image 生成的图（genimg: 引用）也可以传 upload_image 换公网外链",
	}
}

// Execute 上传图片并返回外链；结果作为文本回给模型（失败不中断循环）。
func (t *UploadImageTool) Execute(ctx context.Context, arguments string) (string, error) {
	if t.Cli == nil {
		return "图床未配置（IMGBED_TOKEN 为空），无法上传", nil
	}
	var p struct {
		ImageURL string `json:"image_url"`
	}
	if strings.TrimSpace(arguments) != "" {
		if err := json.Unmarshal([]byte(arguments), &p); err != nil {
			return "", fmt.Errorf("参数解析失败: %w", err)
		}
	}
	urls := []string{p.ImageURL}
	if p.ImageURL == "" {
		if urls = ImagesFromContext(ctx); len(urls) == 0 {
			return "用户当前消息没有图片（也未指定 image_url）", nil
		}
	}
	var sb strings.Builder
	ok := 0
	for i, u := range urls {
		link, err := t.uploadOne(ctx, u)
		if err != nil {
			fmt.Fprintf(&sb, "%d. 失败：%v\n", i+1, err)
			continue
		}
		ok++
		fmt.Fprintf(&sb, "%d. %s\n", i+1, link)
	}
	fmt.Fprintf(&sb, "已上传 %d/%d 张", ok, len(urls))
	return sb.String(), nil
}

// uploadOne 取一张图的字节（genimg: 引用从进程内图床取，否则 HTTP 下载）并上传。
func (t *UploadImageTool) uploadOne(ctx context.Context, ref string) (string, error) {
	var data []byte
	var ct, filename string
	if d, c, fn, isRef := imgstore.Default().Resolve(ref); isRef {
		data, ct, filename = d, c, fn
	} else {
		d, c, err := imgbed.DownloadImage(ctx, ref, t.MaxBytes, t.HTTP)
		if err != nil {
			return "", err
		}
		data, ct, filename = d, c, imgbed.FilenameFor("", c)
	}
	f, err := t.Cli.Upload(ctx, filename, ct, data)
	if err != nil {
		return "", err
	}
	return f.URL, nil
}
