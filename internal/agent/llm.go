package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"resty.dev/v3"
)

// ErrEmptyContent 在 LLM 返回空内容且无工具调用时返回。
var ErrEmptyContent = errors.New("agent: LLM 返回空内容")

// ErrContextOverflow 表示 LLM 因上下文超长被拒（HTTP 413，或 400 + context_length 类错误）。
// agent 据此触发一次压缩后重试（见 Agent.Run）。
var ErrContextOverflow = errors.New("agent: 上下文超长")

// isOverflowResponse 判断一次失败响应是否属于上下文超长。
// 兼容 OpenAI 的 context_length_exceeded 与各家变体措辞。
func isOverflowResponse(statusCode int, body string) bool {
	if statusCode == http.StatusRequestEntityTooLarge {
		return true
	}
	if statusCode != http.StatusBadRequest {
		return false
	}
	b := strings.ToLower(body)
	for _, kw := range []string{
		"context_length_exceeded", "context length", "context_window",
		"maximum context", "too many tokens", "reduce the length", "input is too long",
		// 智谱 bigmodel 的措辞：{"error":{"code":"1261","message":"Prompt exceeds max length"}}
		"exceeds max length", "prompt too long", "prompt is too long", "exceeds the context",
	} {
		if strings.Contains(b, kw) {
			return true
		}
	}
	return false
}

// Completer 是 LLM 客户端的抽象（便于测试替身）。
// 真实实现 *LLMClient 满足该接口。
type Completer interface {
	Complete(ctx context.Context, system string, msgs []Message, tools []map[string]any) (*LLMResult, error)
}

// LLMResult 是一次 Complete 的解析结果。
type LLMResult struct {
	Content      string     // assistant 文本（纯文本回复时非空）
	ToolCalls    []ToolCall // 工具调用（可能为空）
	FinishReason string     // stop / tool_calls / length 等
	Usage        *Usage     // token 用量（响应未带 usage 字段时为 nil）
}

// Usage 是一次 LLM 调用的用量统计：响应的 usage 字段 + 客户端计时（速率由此算出）。
type Usage struct {
	Model            string
	PromptTokens     int64 // 输入 token
	CompletionTokens int64 // 输出 token
	TotalTokens      int64
	CachedTokens     int64 // 缓存命中的输入 token（网关不支持缓存统计时无意义）
	CacheSupported   bool  // 网关是否报告缓存命中（OpenAI 风格 cached_tokens / DeepSeek 风格 hit_tokens）
	DurationMs       int64 // 本次调用耗时（含网络）
}

// String 格式化成单行日志（缓存部分仅网关支持时展示）。
func (u Usage) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s 输入%d 输出%d", u.Model, u.PromptTokens, u.CompletionTokens)
	if u.CacheSupported && u.PromptTokens > 0 {
		fmt.Fprintf(&b, " 缓存命中%d(%d%%)", u.CachedTokens, u.CachedTokens*100/u.PromptTokens)
	}
	fmt.Fprintf(&b, " %.1fs", float64(u.DurationMs)/1000)
	if u.DurationMs > 0 {
		fmt.Fprintf(&b, " %dtok/s", u.CompletionTokens*1000/u.DurationMs)
	}
	return b.String()
}

// LLMClient 是 OpenAI 兼容（Chat Completions）的客户端。
// OpenAI / DeepSeek / Moonshot / OpenRouter 等都适用。
type LLMClient struct {
	BaseURL string // 如 "https://api.deepseek.com/v1"，不含 /chat/completions
	APIKey  string
	Model   string
	Timeout time.Duration // 默认 120s（AI 较慢）
	HTTP    *resty.Client // 为 nil 则每次用默认客户端

	// OnUsage 在每次成功调用后回调用量统计（含工具循环内的多次调用与压缩摘要调用）。
	// 回调里做轻量事（记日志/入队），不要阻塞。
	OnUsage func(Usage)
}

// NewLLMClient 创建客户端，baseURL 去掉末尾斜杠。
func NewLLMClient(baseURL, apiKey, model string) *LLMClient {
	return &LLMClient{
		BaseURL: trimRightSlash(baseURL),
		APIKey:  apiKey,
		Model:   model,
		Timeout: 120 * time.Second,
	}
}

// defaultLLMHTTP 返回 AI 专用 HTTP 客户端（120s 超时）。
func defaultLLMHTTP() *resty.Client {
	return resty.New().
		SetTimeout(120*time.Second).
		SetHeader("User-Agent", "qqbot/1.0")
}

func trimRightSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// Complete 调用 Chat Completions。system 作为 messages[0]；tools 为空则不传工具。
func (c *LLMClient) Complete(ctx context.Context, system string, msgs []Message, tools []map[string]any) (*LLMResult, error) {
	http := c.HTTP
	if http == nil {
		http = defaultLLMHTTP()
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body := map[string]any{
		"model":    c.Model,
		"messages": buildBodyMessages(system, msgs),
	}
	if len(tools) > 0 {
		body["tools"] = tools
		body["tool_choice"] = "auto"
	}

	start := time.Now()
	resp, err := http.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+c.APIKey).
		SetHeader("Content-Type", "application/json").
		SetBody(body).
		Post(c.BaseURL + "/chat/completions")
	if err != nil {
		return nil, fmt.Errorf("LLM 请求失败: %w", err)
	}
	if resp.IsStatusFailure() {
		if isOverflowResponse(resp.StatusCode(), resp.String()) {
			return nil, fmt.Errorf("%w: HTTP %d: %s", ErrContextOverflow, resp.StatusCode(), resp.String())
		}
		return nil, fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode(), resp.String())
	}
	result, err := parseLLMResponse(resp.Bytes())
	if err != nil {
		return nil, err
	}
	// 用量统计：usage 字段 + 客户端计时（输出速率由此可得）。
	u := parseUsage(resp.Bytes(), c.Model)
	u.DurationMs = time.Since(start).Milliseconds()
	result.Usage = &u
	if c.OnUsage != nil {
		c.OnUsage(u)
	}
	return result, nil
}

// parseUsage 解析响应 usage 字段。缓存命中兼容两种报法：
// OpenAI 风格 prompt_tokens_details.cached_tokens（bigmodel 等）与
// DeepSeek 风格 prompt_cache_hit_tokens；都没有则 CacheSupported=false。
func parseUsage(b []byte, model string) Usage {
	u := gjson.GetBytes(b, "usage")
	p := u.Get("prompt_tokens").Int()
	compl := u.Get("completion_tokens").Int()
	total := u.Get("total_tokens").Int()
	if total == 0 {
		total = p + compl
	}
	var cached int64
	supported := false
	if d := u.Get("prompt_tokens_details.cached_tokens"); d.Exists() {
		cached, supported = d.Int(), true
	} else if h := u.Get("prompt_cache_hit_tokens"); h.Exists() {
		cached, supported = h.Int(), true
	}
	return Usage{
		Model:            model,
		PromptTokens:     p,
		CompletionTokens: compl,
		TotalTokens:      total,
		CachedTokens:     cached,
		CacheSupported:   supported,
	}
}

// buildBodyMessages 在内部消息前插入 system 消息。
func buildBodyMessages(system string, msgs []Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs)+1)
	if system != "" {
		out = append(out, map[string]any{"role": "system", "content": system})
	}
	out = append(out, ToOpenAIMessages(msgs)...)
	return out
}

// parseLLMResponse 解析 Chat Completions 响应（choices.0.message）。
func parseLLMResponse(b []byte) (*LLMResult, error) {
	msg := gjson.GetBytes(b, "choices.0.message")
	content := msg.Get("content").String()

	var toolCalls []ToolCall
	if tcs := msg.Get("tool_calls"); tcs.Exists() && tcs.IsArray() {
		tcs.ForEach(func(_, tc gjson.Result) bool {
			toolCalls = append(toolCalls, ToolCall{
				ID:        tc.Get("id").String(),
				Name:      tc.Get("function.name").String(),
				Arguments: tc.Get("function.arguments").String(),
			})
			return true
		})
	}

	finish := gjson.GetBytes(b, "choices.0.finish_reason").String()
	if content == "" && len(toolCalls) == 0 {
		return nil, ErrEmptyContent
	}
	return &LLMResult{Content: content, ToolCalls: toolCalls, FinishReason: finish}, nil
}
