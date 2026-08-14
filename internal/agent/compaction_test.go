package agent

import (
	"context"
	"strings"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	msgs := []Message{{Role: RoleUser, Content: strings.Repeat("a", 400)}} // 400 字符 → ~100 token
	if got := EstimateTokens(msgs); got != 100 {
		t.Errorf("EstimateTokens=%d want 100", got)
	}
	if !ShouldCompact(msgs, 100) {
		t.Error("阈值 100 应触发压缩")
	}
	if ShouldCompact(msgs, 101) {
		t.Error("阈值 101 不应触发")
	}
	if ShouldCompact(msgs, 0) {
		t.Error("阈值 0 表示关闭，不应触发")
	}
}

func TestFindCutPointAlignment(t *testing.T) {
	// user, assistant(TC), tool, tool, user, assistant
	msgs := []Message{
		{Role: RoleUser, Content: "u1"},
		{Role: RoleAssistant, Content: "a1", ToolCalls: []ToolCall{{ID: "1"}}},
		{Role: RoleTool, ToolCallID: "1"},
		{Role: RoleTool, ToolCallID: "1"},
		{Role: RoleUser, Content: "u2"},
		{Role: RoleAssistant, Content: "a2"},
	}
	// keepRecent=2 → 朴素 cut=4（user），非 tool，保持 4。
	if cut := findCutPoint(msgs, 2); cut != 4 {
		t.Errorf("cut=%d want 4", cut)
	}
	// keepRecent=3 → 朴素 cut=3（tool），应前移到 1（拥有这些工具的 assistant）。
	if cut := findCutPoint(msgs, 3); cut != 1 {
		t.Errorf("cut=%d want 1", cut)
	}
	// keepRecent 覆盖全部 → 0（不压缩）。
	if cut := findCutPoint(msgs, 10); cut != 0 {
		t.Errorf("cut=%d want 0", cut)
	}
}

func TestCompact(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "旧问题"},
		{Role: RoleAssistant, Content: "旧回答"},
		{Role: RoleUser, Content: "新问题"},
	}
	llm := &stubLLM{seq: []*LLMResult{{Content: "这是摘要"}}}
	out, err := Compact(context.Background(), llm, msgs, 1)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(out) != 2 { // [摘要, 新问题]
		t.Fatalf("len=%d want 2", len(out))
	}
	if out[0].Role != RoleAssistant || !strings.Contains(out[0].Content, "这是摘要") {
		t.Errorf("摘要消息: %v", out[0])
	}
	if out[1].Content != "新问题" {
		t.Errorf("保留消息: %v", out[1])
	}
}

func TestCompactNoOpWhenKeepRecentCoversAll(t *testing.T) {
	msgs := []Message{{Role: RoleUser, Content: "x"}}
	llm := &stubLLM{}
	out, err := Compact(context.Background(), llm, msgs, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || &out[0] != &msgs[0] {
		t.Errorf("应原样返回，得到 %v", out)
	}
}

// recLLM 记录最后一次摘要调用的 system/user prompt，便于断言是否走了「增量更新」分支。
type recLLM struct {
	lastSys  string
	lastUser string
	reply    string
}

func (r *recLLM) Complete(_ context.Context, sys string, msgs []Message, _ []map[string]any) (*LLMResult, error) {
	r.lastSys = sys
	if len(msgs) > 0 {
		r.lastUser = msgs[0].Content
	}
	return &LLMResult{Content: r.reply, FinishReason: "stop"}, nil
}

func TestCompactIncremental(t *testing.T) {
	llm := &recLLM{reply: "摘要"}
	msgs := []Message{
		{Role: RoleUser, Content: "q1"},
		{Role: RoleAssistant, Content: "a1"},
		{Role: RoleUser, Content: "q2"},
	}
	// 第一次压缩：旧前缀=[q1,a1]，从头生成（user 里无 previous-summary，但含结构化模板）。
	out1, err := Compact(context.Background(), llm, msgs, 1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(llm.lastUser, "previous-summary") {
		t.Errorf("首次压缩不应带 previous-summary: %q", llm.lastUser)
	}
	if !strings.Contains(llm.lastUser, "## 目标") {
		t.Errorf("首次压缩应带结构化模板: %q", llm.lastUser)
	}
	if len(out1) != 2 || out1[0].Role != RoleAssistant || !strings.HasPrefix(out1[0].Content, summaryPrefix) {
		t.Fatalf("out1=%v", out1)
	}

	// 第二次压缩：旧前缀以「上一轮摘要消息」开头 → 应走增量更新（user 里带 previous-summary）。
	msgs2 := append([]Message{}, out1...)
	msgs2 = append(msgs2, NewUser("q3"), Message{Role: RoleAssistant, Content: "a3"})
	out2, err := Compact(context.Background(), llm, msgs2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(llm.lastUser, "previous-summary") {
		t.Errorf("二次压缩应带 previous-summary 做增量更新: %q", llm.lastUser)
	}
	if len(out2) != 2 || out2[0].Role != RoleAssistant {
		t.Fatalf("out2=%v", out2)
	}
}
