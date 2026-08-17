package agent

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	msgs := []Message{{Role: RoleUser, Content: strings.Repeat("a", 400)}} // 400 ASCII → 100 token
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

func TestEstimateTokensCJK(t *testing.T) {
	// 中文每字按 1 token（字节/4 会把 100 个汉字估成 75，实际约 100+，曾低估致撑爆上下文）
	msgs := []Message{{Role: RoleUser, Content: strings.Repeat("中", 100)}}
	if got := EstimateTokens(msgs); got != 100 {
		t.Errorf("100 个汉字应估 100, got %d", got)
	}
	// 混合：50 汉字 + 200 ASCII（50 + 50 = 100）
	msgs = []Message{{Role: RoleUser, Content: strings.Repeat("中", 50) + strings.Repeat("a", 200)}}
	if got := EstimateTokens(msgs); got != 100 {
		t.Errorf("混合文本应估 100, got %d", got)
	}
	// 工具参数也计入
	msgs = []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{Name: "web_search", Arguments: strings.Repeat("查", 30)}}}}
	if got := EstimateTokens(msgs); got < 30 {
		t.Errorf("工具参数应计入估算, got %d", got)
	}
}

func TestSplitConvChunks(t *testing.T) {
	// 短文本单块
	if got := splitConvChunks("abc", 100); len(got) != 1 || got[0] != "abc" {
		t.Errorf("短文本应单块: %q", got)
	}
	if got := splitConvChunks("", 100); got != nil {
		t.Errorf("空文本应返回 nil: %v", got)
	}
	// 长文本多块：块内不超预算、拼接还原原文、优先换行处断开
	line := strings.Repeat("一", 40) + "\n" // 每行 41 字符
	conv := strings.Repeat(line, 20)       // 820 字符
	chunks := splitConvChunks(conv, 100)
	if len(chunks) < 2 {
		t.Fatalf("820 字符按预算 100 应至少 2 块, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len([]rune(c)) > 100 {
			t.Errorf("第 %d 块超预算: %d", i, len([]rune(c)))
		}
	}
	if strings.Join(chunks, "") != conv {
		t.Error("分块拼接应还原原文")
	}
	// 有换行时应在换行处断
	for _, c := range chunks[:len(chunks)-1] {
		if !strings.HasSuffix(c, "\n") {
			t.Errorf("块尾应是换行（预算内存在换行时）: %q", c[len(c)-10:])
		}
	}
}

// chunkLLM 记录每次摘要调用收到的 user prompt（用于断言分块大小与链式增量）。
type chunkLLM struct {
	prompts []string
}

func (c *chunkLLM) Complete(_ context.Context, _ string, msgs []Message, _ []map[string]any) (*LLMResult, error) {
	c.prompts = append(c.prompts, msgs[len(msgs)-1].Content)
	return &LLMResult{Content: "摘要-" + strconv.Itoa(len(c.prompts)), FinishReason: "stop"}, nil
}

func TestSummarizeChunked(t *testing.T) {
	// 对话远超预算 → 多次调用；每次 <conversation> 部分不超预算；后续调用带上一轮摘要
	big := []Message{NewUser(strings.Repeat("这句话很长。", 20000))} // 序列化后 ~12 万字符
	fake := &chunkLLM{}
	got, err := summarize(context.Background(), fake, big, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "摘要-") {
		t.Fatalf("应返回最后一次摘要结果: %q", got)
	}
	if len(fake.prompts) < 2 {
		t.Fatalf("长对话应分块多次摘要, got %d 次", len(fake.prompts))
	}
	for i, p := range fake.prompts {
		// 每次调用里 <conversation> 段不超预算
		start := strings.Index(p, "<conversation>\n") + len("<conversation>\n")
		end := strings.LastIndex(p, "\n</conversation>")
		if start < len("<conversation>\n") || end <= start {
			t.Fatalf("第 %d 次 prompt 缺 conversation 段", i)
		}
		if n := len([]rune(p[start:end])); n > summarizeChunkChars {
			t.Errorf("第 %d 次对话段 %d 字符超预算 %d", i, n, summarizeChunkChars)
		}
		if i > 0 && !strings.Contains(p, "摘要-"+strconv.Itoa(i)) {
			t.Errorf("第 %d 次应携带上一轮摘要（链式增量）", i)
		}
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
