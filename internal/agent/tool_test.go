package agent

import (
	"context"
	"strings"
	"testing"
)

func TestRegistrySchemasSorted(t *testing.T) {
	r := NewRegistry()
	r.Register(&ToolFunc{NameStr: "b", DescStr: "b desc"})
	r.Register(&ToolFunc{NameStr: "a", DescStr: "a desc"})

	schemas := r.OpenAISchemas()
	if len(schemas) != 2 {
		t.Fatalf("len=%d", len(schemas))
	}
	first := schemas[0]["function"].(map[string]any)["name"]
	second := schemas[1]["function"].(map[string]any)["name"]
	if first != "a" || second != "b" {
		t.Errorf("未按名排序: %s, %s", first, second)
	}
}

func TestRegistryNilParamsDefault(t *testing.T) {
	r := NewRegistry()
	r.Register(&ToolFunc{NameStr: "x"}) // Parameters() 返回 nil
	s := r.OpenAISchemas()
	params := s[0]["function"].(map[string]any)["parameters"].(map[string]any)
	if params["type"] != "object" {
		t.Errorf("默认参数应为 object，得到 %v", params)
	}
}

func TestRegistryEmptyReturnsNil(t *testing.T) {
	if got := NewRegistry().OpenAISchemas(); got != nil {
		t.Errorf("空注册表应返回 nil，得到 %v", got)
	}
}

func TestRegistryGet(t *testing.T) {
	r := NewRegistry()
	r.Register(&ToolFunc{NameStr: "a", Run: func(context.Context, string) (string, error) { return "", nil }})
	if _, ok := r.Get("a"); !ok {
		t.Error("应找到 a")
	}
	if _, ok := r.Get("z"); ok {
		t.Error("不应找到 z")
	}
}

func TestToolFuncNilRun(t *testing.T) {
	tf := &ToolFunc{NameStr: "x"}
	out, err := tf.Execute(context.Background(), "")
	if err != nil || out != "" {
		t.Errorf("nil Run 应返回 (\"\", nil)，得到 (%q, %v)", out, err)
	}
}

// guidelineTool 是带 Guidelines 的测试工具（也用来测 firstLine：多行描述只取首行）。
type guidelineTool struct{ name string }

func (t *guidelineTool) Name() string        { return t.name }
func (t *guidelineTool) Description() string { return "multi\nline\ndesc" }
func (t *guidelineTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}
func (t *guidelineTool) Execute(context.Context, string) (string, error) { return "", nil }
func (t *guidelineTool) Guidelines() []string                            { return []string{"beta-要点"} }

func TestToolsPromptBlock(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&stubTool{name: "alpha"})     // 无 Guidelines
	reg.Register(&guidelineTool{name: "beta"}) // 有 Guidelines，多行描述
	block := toolsPromptBlock(reg)

	for _, want := range []string{"Available tools:", "- alpha: stub", "- beta: multi", "Guidelines:"} {
		if !strings.Contains(block, want) {
			t.Errorf("缺 %q:\n%s", want, block)
		}
	}
	if !strings.Contains(block, "beta-要点") {
		t.Errorf("beta 的 guideline 缺失:\n%s", block)
	}
	if !strings.Contains(block, "回答简洁") {
		t.Errorf("恒定 guideline 缺失:\n%s", block)
	}
	// 空注册表 → 空串。
	if s := toolsPromptBlock(NewRegistry()); s != "" {
		t.Errorf("空注册表应返回空串，得到 %q", s)
	}
}
