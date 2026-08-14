package agent

import (
	"context"
	"encoding/json"
	"fmt"
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
	ctxBlock   string // 动态上下文块：临时消息插在问题前，不落 session
	persistBlk string // 持久补充块（如增量群聊记录）：普通 user 消息插在问题前，照常落盘
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
