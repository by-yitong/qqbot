package agent

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// Tool 是一个可被 LLM 调用的工具。
//
// Execute 收到的 arguments 是原始 JSON 字符串（即 LLM 返回的 function.arguments），
// 返回的内容会作为 tool result 喂回 LLM。出错时也应返回人类可读的错误文本，
// 由 Agent 负责包装成 tool result（不中断 loop），让模型自己处理失败。
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any // JSON Schema（参数描述）；nil 表示无参数对象
	Execute(ctx context.Context, arguments string) (string, error)
}

// ToolGuideline 是工具的可选能力：实现它的工具可向 system prompt 贡献若干条
// 「使用要点」（类似 pi 的 promptGuidelines）。不实现该接口的工具不贡献要点。
// Go 的鸭子类型让 tools 子包里的工具无需 import 本包即可实现它。
type ToolGuideline interface {
	Guidelines() []string
}

// ToolFunc 把一个普通函数快速包成 Tool，省去手写一个类型。
type ToolFunc struct {
	NameStr string
	DescStr string
	Params  map[string]any
	Run     func(ctx context.Context, arguments string) (string, error)
}

// Name 返回工具名。
func (t *ToolFunc) Name() string { return t.NameStr }

// Description 返回工具描述。
func (t *ToolFunc) Description() string { return t.DescStr }

// Parameters 返回参数 JSON Schema。
func (t *ToolFunc) Parameters() map[string]any { return t.Params }

// Execute 执行工具。
func (t *ToolFunc) Execute(ctx context.Context, arguments string) (string, error) {
	if t.Run == nil {
		return "", nil
	}
	return t.Run(ctx, arguments)
}

// Registry 是工具集合，线程安全（工具一般在启动时注册、运行时只读）。
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry { return &Registry{tools: map[string]Tool{}} }

// Register 注册一个工具（同名覆盖）。
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
}

// Get 按名取工具。
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Names 返回所有工具名（有序，便于稳定输出）。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// OpenAISchemas 生成 OpenAI Chat Completions 请求里的 tools 字段（按名有序）。
// 空注册表返回 nil（调用方可据此决定是否传 tools）。
func (r *Registry) OpenAISchemas() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.tools) == 0 {
		return nil
	}
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		t := r.tools[n]
		params := t.Parameters()
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name(),
				"description": t.Description(),
				"parameters":  params,
			},
		})
	}
	return out
}

// toolsPromptBlock 渲染 system prompt 里的工具区：
//   - Available tools：每个工具一行（name: 描述首行，类似 pi 的 promptSnippet）。
//   - Guidelines：来自实现了 ToolGuideline 的工具的使用要点 + 恒定的简洁/诚实提示。
//
// 无工具时返回空串。按名有序、去重。
func toolsPromptBlock(reg *Registry) string {
	names := reg.Names()
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Available tools:\n")
	for _, n := range names {
		t, _ := reg.Get(n)
		b.WriteString("- ")
		b.WriteString(n)
		b.WriteString(": ")
		b.WriteString(firstLine(t.Description()))
		b.WriteString("\n")
	}

	// 收集 guidelines（工具贡献的 + 恒定的），按出现顺序去重。
	var guidelines []string
	seen := map[string]bool{}
	add := func(g string) {
		g = strings.TrimSpace(g)
		if g == "" || seen[g] {
			return
		}
		seen[g] = true
		guidelines = append(guidelines, g)
	}
	for _, n := range names {
		t, _ := reg.Get(n)
		if g, ok := t.(ToolGuideline); ok {
			for _, line := range g.Guidelines() {
				add(line)
			}
		}
	}
	add("回答简洁，不啰嗦")
	add("不确定时直说不确定，不要编造")

	b.WriteString("\nGuidelines:\n")
	for _, g := range guidelines {
		b.WriteString("- ")
		b.WriteString(g)
		b.WriteString("\n")
	}
	return b.String()
}

// firstLine 取描述的首行（多行描述只取第一行做 snippet）。
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
