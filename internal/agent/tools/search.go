package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// 默认端点与客户端标识（与 anysearch 官方 skill 一致）。
const (
	anySearchEndpoint  = "https://api.anysearch.com/mcp"
	anySearchClientHdr = "skill/3.0.1"
)

// AnySearch 调用 anysearch 的 MCP JSON-RPC 端点：文本搜索 + 网页正文抓取（不搜图）。
// APIKey 可选（匿名访问额度较低）。
type AnySearch struct {
	APIKey  string        // 可选，提升限额
	BaseURL string        // 默认 https://api.anysearch.com/mcp
	HTTP    *http.Client  // 为 nil 则用默认 30s 客户端
	Timeout time.Duration // 默认 30s
}

// --- JSON-RPC 结构 ---

type rpcRequest struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      int       `json:"id"`
	Method  string    `json:"method"`
	Params  rpcParams `json:"params"`
}

type rpcParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type rpcResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
	Result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
}

// call 发送 tools/call JSON-RPC，返回 result.content 的文本拼接（直接喂给 LLM，不解析内部结构）。
func (a *AnySearch) call(ctx context.Context, tool string, args map[string]any) (string, error) {
	base := a.BaseURL
	if base == "" {
		base = anySearchEndpoint
	}
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client := a.HTTP
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	body, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0", ID: 1, Method: "tools/call",
		Params: rpcParams{Name: tool, Arguments: args},
	})
	if err != nil {
		return "", fmt.Errorf("构造请求失败: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, base, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Anysearch-Client", anySearchClientHdr)
	if a.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.APIKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 anysearch 失败: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("anysearch HTTP %d: %s", resp.StatusCode, truncateBody(data))
	}

	var r rpcResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return "", fmt.Errorf("解析响应失败: %w", err)
	}
	if r.Error != nil {
		return "", fmt.Errorf("anysearch 错误: %s", r.Error.Message)
	}
	var sb strings.Builder
	for _, c := range r.Result.Content {
		if c.Type == "text" && c.Text != "" {
			sb.WriteString(c.Text)
			sb.WriteByte('\n')
		}
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return "(无结果)", nil
	}
	return text, nil
}

// truncateBody 截断错误响应体便于日志。
func truncateBody(b []byte) string {
	s := string(b)
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

// --- web_search 工具（anysearch search） ---

// WebSearch 是联网搜索工具。
type WebSearch struct{ Owner *AnySearch }

// Name 返回工具名。
func (t *WebSearch) Name() string { return "web_search" }

// Description 返回工具描述。
func (t *WebSearch) Description() string {
	return "联网搜索（anysearch），返回搜索结果（标题/摘要/链接）。需要查最新信息、查事实、找资料、找图片直链 URL 等时使用。"
}

// Parameters 返回参数 JSON Schema。
func (t *WebSearch) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query":       map[string]any{"type": "string", "description": "搜索关键词"},
			"max_results": map[string]any{"type": "integer", "description": "结果数量 1-10，默认 5", "minimum": 1, "maximum": 10},
			"domain":      map[string]any{"type": "string", "description": "垂直领域（可选，如 finance/academic/code 等）"},
		},
		"required": []string{"query"},
	}
}

// Guidelines 贡献使用要点。
func (t *WebSearch) Guidelines() []string {
	return []string{
		"查最新信息、查事实、找资料用 web_search",
		"要抓某个网页的正文内容用 web_extract",
		"搜索结果里若含图片直链 URL（jpg/png/gif/webp），可用 send_image 发给用户",
	}
}

// Execute 执行搜索。
func (t *WebSearch) Execute(ctx context.Context, arguments string) (string, error) {
	q := gjson.Get(arguments, "query").String()
	if q == "" {
		return "", fmt.Errorf("缺少 query")
	}
	args := map[string]any{"query": q}
	if mr := gjson.Get(arguments, "max_results").Int(); mr > 0 {
		args["max_results"] = mr
	} else {
		args["max_results"] = 5
	}
	if d := gjson.Get(arguments, "domain").String(); d != "" {
		args["domain"] = d
	}
	return t.Owner.call(ctx, "search", args)
}

// --- web_extract 工具（anysearch extract） ---

// WebExtract 抓取网页正文。
type WebExtract struct{ Owner *AnySearch }

// Name 返回工具名。
func (t *WebExtract) Name() string { return "web_extract" }

// Description 返回工具描述。
func (t *WebExtract) Description() string {
	return "抓取指定网页的正文内容（返回 Markdown）。需要读某个网页/文章全文时使用。"
}

// Parameters 返回参数 JSON Schema。
func (t *WebExtract) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{"type": "string", "description": "要抓取的网页 URL"},
		},
		"required": []string{"url"},
	}
}

// Execute 抓取网页。
func (t *WebExtract) Execute(ctx context.Context, arguments string) (string, error) {
	u := gjson.Get(arguments, "url").String()
	if u == "" {
		return "", fmt.Errorf("缺少 url")
	}
	return t.Owner.call(ctx, "extract", map[string]any{"url": u})
}
