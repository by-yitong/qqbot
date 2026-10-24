package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// stubLLM 是 Completer 的测试替身：依次返回 seq，用完则重复最后一个。
type stubLLM struct {
	seq   []*LLMResult
	calls int
}

func (s *stubLLM) Complete(context.Context, string, []Message, []map[string]any) (*LLMResult, error) {
	s.calls++
	if len(s.seq) == 0 {
		return &LLMResult{Content: "empty", FinishReason: "stop"}, nil
	}
	if s.calls <= len(s.seq) {
		return s.seq[s.calls-1], nil
	}
	return s.seq[len(s.seq)-1], nil
}

// stubTool 记录调用次数与最后参数。
type stubTool struct {
	name     string
	calls    int
	lastArgs string
}

func (t *stubTool) Name() string        { return t.name }
func (t *stubTool) Description() string { return "stub" }
func (t *stubTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *stubTool) Execute(_ context.Context, args string) (string, error) {
	t.calls++
	t.lastArgs = args
	return "result:" + args, nil
}

func TestAgentLoopRunsTool(t *testing.T) {
	llm := &stubLLM{seq: []*LLMResult{
		{ToolCalls: []ToolCall{{ID: "1", Name: "echo", Arguments: `{"x":"hi"}`}}, FinishReason: "tool_calls"},
		{Content: "done", FinishReason: "stop"},
	}}
	reg := NewRegistry()
	st := &stubTool{name: "echo"}
	reg.Register(st)

	a := New(Config{LLM: llm, Tools: reg})
	out, err := a.Run(context.Background(), "u1", "请调 echo")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out != "done" {
		t.Fatalf("out=%q", out)
	}
	if st.calls != 1 {
		t.Fatalf("工具应被调用 1 次，实际 %d", st.calls)
	}
	if st.lastArgs != `{"x":"hi"}` {
		t.Fatalf("参数=%q", st.lastArgs)
	}
}

func TestAgentMaxIterationsStops(t *testing.T) {
	// LLM 一直返回工具调用 → 应被 MaxIterations 截断，且无文本 → 返回 error。
	llm := &stubLLM{seq: []*LLMResult{
		{ToolCalls: []ToolCall{{ID: "1", Name: "loop", Arguments: "{}"}}, FinishReason: "tool_calls"},
	}}
	reg := NewRegistry()
	st := &stubTool{name: "loop"}
	reg.Register(st)

	a := New(Config{LLM: llm, Tools: reg, MaxIterations: 3})
	_, err := a.Run(context.Background(), "u1", "go")
	if err == nil {
		t.Fatal("达到迭代上限应返回 error")
	}
	if st.calls != 3 {
		t.Fatalf("工具应被调用 3 次（= MaxIterations），实际 %d", st.calls)
	}
}

func TestAgentUnknownTool(t *testing.T) {
	// 调用不存在的工具：返回错误文本喂回模型，loop 继续，最终拿到文本回复。
	llm := &stubLLM{seq: []*LLMResult{
		{ToolCalls: []ToolCall{{ID: "1", Name: "nope", Arguments: "{}"}}, FinishReason: "tool_calls"},
		{Content: "ok", FinishReason: "stop"},
	}}
	a := New(Config{LLM: llm, Tools: NewRegistry()})
	out, err := a.Run(context.Background(), "u1", "go")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out != "ok" {
		t.Fatalf("out=%q", out)
	}
}

func TestAgentStatelessSingleTurn(t *testing.T) {
	// Tools=nil → 退化为单轮；LLM 直接给文本。
	llm := &stubLLM{seq: []*LLMResult{{Content: "hi", FinishReason: "stop"}}}
	a := New(Config{LLM: llm})
	out, err := a.Run(context.Background(), "u1", "hello")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out != "hi" {
		t.Fatalf("out=%q", out)
	}
	if llm.calls != 1 {
		t.Fatalf("应只调用 1 次 LLM，实际 %d", llm.calls)
	}
}

func TestAgentReturnsLastReplyOnLLMError(t *testing.T) {
	// 工具跑完后 LLM 出错：已有中间回复就先返回它。
	errLLM := &errCompleter{}
	reg := NewRegistry()
	reg.Register(&stubTool{name: "echo"})

	a := New(Config{LLM: errLLM, Tools: reg})
	out, err := a.Run(context.Background(), "u1", "go")
	// errCompleter 第一次就错 → 无中间回复 → 返回 error
	if err == nil {
		t.Fatal("应返回 error")
	}
	if out != "" {
		t.Fatalf("out 应为空，得到 %q", out)
	}
}

// errCompleter 总是返回 error。
type errCompleter struct{}

func (e *errCompleter) Complete(context.Context, string, []Message, []map[string]any) (*LLMResult, error) {
	return nil, context.DeadlineExceeded
}

// recSysLLM 记录每次收到的 system prompt 与消息。
type recSysLLM struct {
	lastSys  string
	lastMsgs []Message
}

func (r *recSysLLM) Complete(_ context.Context, sys string, msgs []Message, _ []map[string]any) (*LLMResult, error) {
	r.lastSys = sys
	r.lastMsgs = append([]Message{}, msgs...)
	return &LLMResult{Content: "ok", FinishReason: "stop"}, nil
}

func TestAgentContextBlockInjectedNotPersisted(t *testing.T) {
	// WithContextBlock 注入的动态块：作为临时 user 消息插在问题紧前面发给 LLM，
	// 不进 system（保前缀稳定命中缓存）、不落 session（不重复堆积）。
	llm := &recSysLLM{}
	dir := t.TempDir()
	sessions, err := NewSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := New(Config{LLM: llm, Sessions: sessions})

	blk := "[群聊补充动态（自上次处理以来的新消息，自动注入，供了解上下文）]\n[08-14 12:00] 小明: 闲聊"
	if _, err := a.Run(context.Background(), "g:g1", "[小明] 你好", WithContextBlock(blk)); err != nil {
		t.Fatal(err)
	}

	// 不进 system prompt
	if strings.Contains(llm.lastSys, "群聊补充动态") {
		t.Fatalf("动态块不应进 system prompt:\n%s", llm.lastSys)
	}
	// 作为 user 消息、位置在问题紧前面
	n := len(llm.lastMsgs)
	if n < 2 || llm.lastMsgs[n-2].Content != blk || llm.lastMsgs[n-2].Role != RoleUser {
		t.Fatalf("动态块应是问题前的倒数第二条 user 消息: %+v", llm.lastMsgs)
	}
	if llm.lastMsgs[n-1].Content != "[小明] 你好" {
		t.Fatalf("最后一条应是问题: %+v", llm.lastMsgs[n-1])
	}

	// 落盘的 session 不应含动态块
	sess := sessions.Get("g:g1")
	sess.Lock()
	hist := append([]Message{}, sess.History()...)
	sess.Unlock()
	for _, m := range hist {
		if strings.Contains(m.Content, "群聊补充动态") {
			t.Fatalf("动态块不应落进 session 历史: %+v", m)
		}
	}
	if len(hist) != 2 { // 只有 user 问题 + assistant 回复
		t.Fatalf("session 应只有 2 条，实际 %d: %+v", len(hist), hist)
	}

	// 下一轮加载历史：请求中不应再出现上一轮的动态块
	llm.lastMsgs = nil
	if _, err := a.Run(context.Background(), "g:g1", "[小明] 再见"); err != nil {
		t.Fatal(err)
	}
	for _, m := range llm.lastMsgs {
		if strings.Contains(m.Content, "群聊补充动态") {
			t.Fatalf("下一轮请求不应残留上一轮动态块: %+v", m)
		}
	}
}

func TestAgentPersistBlockInSession(t *testing.T) {
	// WithPersistBlock 注入的补充块：普通 user 消息插在问题紧前面，随 session 落盘
	// ——下轮随历史自动加载，不必重复注入。
	llm := &recSysLLM{}
	sessions, err := NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := New(Config{LLM: llm, Sessions: sessions})

	blk := "[群聊记录（上次对话以来的新消息，系统自动记录）]\n[08-14 12:00] 小明: 闲聊"
	if _, err := a.Run(context.Background(), "g:g1", "[小红] 你好", WithPersistBlock(blk)); err != nil {
		t.Fatal(err)
	}

	// 位置：问题紧前面；角色 user；不进 system
	if strings.Contains(llm.lastSys, "群聊记录") {
		t.Fatalf("补充块不应进 system prompt:\n%s", llm.lastSys)
	}
	n := len(llm.lastMsgs)
	if n < 2 || llm.lastMsgs[n-2].Content != blk || llm.lastMsgs[n-2].Role != RoleUser {
		t.Fatalf("补充块应是问题前的倒数第二条 user 消息: %+v", llm.lastMsgs)
	}

	// 落盘的 session 应包含补充块（块 + 问题 + 回复 = 3 条）
	sess := sessions.Get("g:g1")
	sess.Lock()
	hist := append([]Message{}, sess.History()...)
	sess.Unlock()
	if len(hist) != 3 || hist[0].Content != blk {
		t.Fatalf("session 应含补充块共 3 条，实际 %d: %+v", len(hist), hist)
	}

	// 下一轮不再注入（模拟插件判定无增量）：历史里应自动带着上一轮的补充块
	llm.lastMsgs = nil
	if _, err := a.Run(context.Background(), "g:g1", "[小红] 再见"); err != nil {
		t.Fatal(err)
	}
	if len(llm.lastMsgs) == 0 || !strings.Contains(llm.lastMsgs[0].Content, "小明: 闲聊") {
		t.Fatalf("下一轮请求应自动携带上一轮补充块: %+v", llm.lastMsgs)
	}
}

func TestAgentSessionTTLDropsStaleHistory(t *testing.T) {
	// session 里预置「两天前的旧对话 + 十分钟前的新对话」，TTL=1 小时：
	// 旧对话不应进入请求上下文、也不留在落盘历史；新对话保留，
	// 且本次追加的消息带 Ts 落盘（下次衰减判断的依据）。
	llm := &recSysLLM{}
	sessions, err := NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess := sessions.Get("g:g1")
	sess.Lock()
	sess.ReplaceHistory([]Message{
		{Role: RoleUser, Content: "两天前的问题", Ts: time.Now().Add(-48 * time.Hour)},
		{Role: RoleAssistant, Content: "两天前的回答", Ts: time.Now().Add(-48 * time.Hour)},
		{Role: RoleUser, Content: "十分钟前的问题", Ts: time.Now().Add(-10 * time.Minute)},
		{Role: RoleAssistant, Content: "十分钟前的回答", Ts: time.Now().Add(-10 * time.Minute)},
	})
	sess.Unlock()

	a := New(Config{LLM: llm, Sessions: sessions, SessionTTL: time.Hour})
	if _, err := a.Run(context.Background(), "g:g1", "新问题"); err != nil {
		t.Fatal(err)
	}

	// 请求：近历史 + 新问题，旧对话不出现。
	for _, m := range llm.lastMsgs {
		if strings.Contains(m.Content, "两天前") {
			t.Fatalf("旧历史不应进入上下文: %+v", llm.lastMsgs)
		}
	}
	if len(llm.lastMsgs) != 3 || llm.lastMsgs[0].Content != "十分钟前的问题" {
		t.Fatalf("应携带近历史 + 新问题，实际: %+v", llm.lastMsgs)
	}

	// 落盘历史被瘦身（旧对话移除），且所有消息带非零 Ts。
	sess.Lock()
	hist := append([]Message{}, sess.History()...)
	sess.Unlock()
	if len(hist) != 4 { // 近历史 2 条 + 新问 + 新答
		t.Fatalf("落盘应剩 4 条，实际 %d: %+v", len(hist), hist)
	}
	for i, m := range hist {
		if m.Ts.IsZero() {
			t.Errorf("hist[%d] 应带 Ts（否则下次衰减误判为陈旧）: %+v", i, m)
		}
	}

	// 零值 Ts（升级前存量数据）同样被视为陈旧剔除。
	sess.Lock()
	sess.ReplaceHistory([]Message{
		{Role: RoleUser, Content: "存量旧消息"},
	})
	sess.Unlock()
	llm.lastMsgs = nil
	if _, err := a.Run(context.Background(), "g:g1", "又一条"); err != nil {
		t.Fatal(err)
	}
	for _, m := range llm.lastMsgs {
		if strings.Contains(m.Content, "存量旧消息") {
			t.Fatalf("零值 Ts 的存量消息应被视为陈旧: %+v", llm.lastMsgs)
		}
	}
}

func TestAgentSessionTTLOffKeepsHistory(t *testing.T) {
	// SessionTTL=0（默认）：时间衰减关闭，旧历史照常携带（向后兼容）。
	llm := &recSysLLM{}
	sessions, _ := NewSessionStore(t.TempDir())
	sess := sessions.Get("u1")
	sess.Lock()
	sess.ReplaceHistory([]Message{
		{Role: RoleUser, Content: "一周前的问题", Ts: time.Now().AddDate(0, 0, -7)},
		{Role: RoleAssistant, Content: "一周前的回答", Ts: time.Now().AddDate(0, 0, -7)},
	})
	sess.Unlock()

	a := New(Config{LLM: llm, Sessions: sessions})
	if _, err := a.Run(context.Background(), "u1", "新问题"); err != nil {
		t.Fatal(err)
	}
	if len(llm.lastMsgs) != 3 || llm.lastMsgs[0].Content != "一周前的问题" {
		t.Fatalf("TTL 关闭时历史应原样携带: %+v", llm.lastMsgs)
	}
}

// overflowThenOK 第一次返回上下文超长错误，之后返回正常回复（模拟压缩后重试成功）。
type overflowThenOK struct {
	n int
}

func (o *overflowThenOK) Complete(_ context.Context, _ string, _ []Message, _ []map[string]any) (*LLMResult, error) {
	o.n++
	if o.n == 1 {
		return nil, fmt.Errorf("%w: 模拟上下文超长", ErrContextOverflow)
	}
	return &LLMResult{Content: "recovered", FinishReason: "stop"}, nil
}

func TestAgentOverflowRecovery(t *testing.T) {
	// 预置足够长的历史，使溢出后的强制压缩能真正缩短历史（findCutPoint > 0）。
	store, _ := NewSessionStore("")
	sess := store.Get("u1")
	sess.Lock()
	for i := 0; i < 20; i++ {
		sess.Append(NewUser("历史问题"))
		sess.Append(Message{Role: RoleAssistant, Content: "历史回答"})
	}
	sess.Unlock()

	llm := &overflowThenOK{}
	a := New(Config{LLM: llm, Sessions: store, KeepRecent: 4})
	out, err := a.Run(context.Background(), "u1", "继续")
	if err != nil {
		t.Fatalf("应压缩后重试成功，err: %v", err)
	}
	if out != "recovered" {
		t.Fatalf("out=%q", out)
	}
	// 调用序列：① 溢出 → ② 压缩摘要 → ③ 重试成功，共 3 次。
	if llm.n != 3 {
		t.Fatalf("LLM 调用次数=%d want 3", llm.n)
	}
}
