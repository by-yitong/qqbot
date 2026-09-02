package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// gateLLM 是可编排的 LLM 测试替身：每次调用先在 started 上发信号，
// 若 release 非 nil 则等它 close（模拟慢调用，给测试留出 Steer/Interrupt 的窗口），
// 然后按 seq 顺序返回结果；cancelOnCtx 时 ctx 取消则返回 ctx.Err()（模拟真实客户端）。
type gateLLM struct {
	mu       sync.Mutex
	calls    int
	msgs     [][]Message // 每次调用收到的消息快照
	seq      []*LLMResult
	started  chan struct{}
	release  chan struct{}
	cancelCh bool // ctx 取消时返回 ctx.Err()
}

func newGateLLM(seq ...*LLMResult) *gateLLM {
	return &gateLLM{
		seq:      seq,
		started:  make(chan struct{}, 16),
		release:  make(chan struct{}),
		cancelCh: true,
	}
}

func (g *gateLLM) Complete(ctx context.Context, _ string, msgs []Message, _ []map[string]any) (*LLMResult, error) {
	g.mu.Lock()
	g.calls++
	n := g.calls
	g.msgs = append(g.msgs, append([]Message{}, msgs...))
	g.mu.Unlock()
	if g.started != nil {
		g.started <- struct{}{}
	}
	if g.release != nil {
		select {
		case <-g.release:
		case <-ctx.Done():
		}
	}
	if g.cancelCh {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if n <= len(g.seq) {
		return g.seq[n-1], nil
	}
	return g.seq[len(g.seq)-1], nil
}

func (g *gateLLM) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

// msgsOf 返回第 n 次（1 起）调用收到的消息。
func (g *gateLLM) msgsOf(n int) []Message {
	g.mu.Lock()
	defer g.mu.Unlock()
	if n < 1 || n > len(g.msgs) {
		return nil
	}
	return g.msgs[n-1]
}

// containsUserText 报告第 n 次调用的消息里是否有内容为 text 的 user 消息。
func (g *gateLLM) containsUserText(n int, text string) bool {
	for _, m := range g.msgsOf(n) {
		if m.Role == RoleUser && m.Content == text {
			return true
		}
	}
	return false
}

// gateTool 是可阻塞的工具测试替身。
type gateTool struct {
	name    string
	started chan struct{}
	release chan struct{}
	calls   int
}

func (t *gateTool) Name() string        { return t.name }
func (t *gateTool) Description() string { return "gate" }
func (t *gateTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (t *gateTool) Execute(ctx context.Context, _ string) (string, error) {
	t.calls++
	t.started <- struct{}{}
	select {
	case <-t.release:
		return "tool ok", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// runResult 收集一次后台 Run 的结果。
type runResult struct {
	reply string
	err   error
	done  chan struct{}
}

func startRun(a *Agent, key, text string) *runResult {
	r := &runResult{done: make(chan struct{})}
	go func() {
		defer close(r.done)
		r.reply, r.err = a.Run(context.Background(), key, text)
	}()
	return r
}

func waitSignal(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("等待 %s 超时", what)
	}
}

func TestSteerInjectsIntoRunningRun(t *testing.T) {
	// 第 1 次调用返回工具调用拉住 Run；补充发生在工具执行期间；
	// 第 2 次调用必须看到补充的 user 消息。
	llm := newGateLLM(
		&LLMResult{Content: "", ToolCalls: []ToolCall{{ID: "c1", Name: "gate", Arguments: "{}"}}, FinishReason: "tool_calls"},
		&LLMResult{Content: "最终回复", FinishReason: "stop"},
	)
	tool := &gateTool{name: "gate", started: make(chan struct{}, 4), release: make(chan struct{})}
	reg := NewRegistry()
	reg.Register(tool)
	a := New(Config{LLM: llm, Tools: reg})
	close(llm.release) // LLM 不阻塞

	r := startRun(a, "u1", "画只猫")
	waitSignal(t, tool.started, "工具开始执行")

	if !a.Steer("u1", NewUser("[小红] 加个红色蝴蝶结")) {
		t.Fatal("运行中 Steer 应返回 true")
	}
	close(tool.release)
	waitSignal(t, r.done, "Run 结束")

	if r.err != nil {
		t.Fatalf("err: %v", r.err)
	}
	if r.reply != "最终回复" {
		t.Fatalf("reply=%q", r.reply)
	}
	if got := llm.callCount(); got != 2 {
		t.Fatalf("LLM 应调用 2 次，实际 %d", got)
	}
	if !llm.containsUserText(2, "[小红] 加个红色蝴蝶结") {
		t.Fatalf("第 2 次调用应包含补充消息，实际: %+v", llm.msgsOf(2))
	}
}

func TestSteerKeepsRunAliveAfterFinalText(t *testing.T) {
	// 第 1 次调用期间补充已入队：即便它返回纯文本（本该结束），
	// Run 也必须继续消费补充再答一轮。
	llm := newGateLLM(
		&LLMResult{Content: "先这样", FinishReason: "stop"},
		&LLMResult{Content: "画好了", FinishReason: "stop"},
	)
	a := New(Config{LLM: llm})

	r := startRun(a, "u1", "画只猫")
	waitSignal(t, llm.started, "LLM 第 1 次调用")

	if !a.Steer("u1", NewUser("加个蝴蝶结")) {
		t.Fatal("运行中 Steer 应返回 true")
	}
	close(llm.release)
	waitSignal(t, r.done, "Run 结束")

	if r.reply != "画好了" {
		t.Fatalf("reply=%q，应为补充后的最终回复", r.reply)
	}
	if got := llm.callCount(); got != 2 {
		t.Fatalf("LLM 应调用 2 次，实际 %d", got)
	}
	if !llm.containsUserText(2, "加个蝴蝶结") {
		t.Fatalf("第 2 次调用应包含补充消息，实际: %+v", llm.msgsOf(2))
	}
}

func TestSteerNoActiveRunReturnsFalse(t *testing.T) {
	a := New(Config{LLM: newGateLLM(&LLMResult{Content: "x", FinishReason: "stop"})})
	if a.Steer("nobody", NewUser("补充")) {
		t.Fatal("无活跃 Run 时 Steer 应返回 false")
	}
}

func TestSteerAfterRunEndsReturnsFalse(t *testing.T) {
	llm := newGateLLM(&LLMResult{Content: "hi", FinishReason: "stop"})
	a := New(Config{LLM: llm})
	close(llm.release)
	if _, err := a.Run(context.Background(), "u1", "hello"); err != nil {
		t.Fatalf("err: %v", err)
	}
	if a.Steer("u1", NewUser("补充")) {
		t.Fatal("Run 结束后 Steer 应返回 false")
	}
}

func TestInterruptCancelsRun(t *testing.T) {
	llm := newGateLLM(&LLMResult{Content: "不该到这", FinishReason: "stop"})
	a := New(Config{LLM: llm})

	r := startRun(a, "u1", "画只猫")
	waitSignal(t, llm.started, "LLM 调用开始")

	if !a.Interrupt("u1") {
		t.Fatal("运行中 Interrupt 应返回 true")
	}
	waitSignal(t, r.done, "Run 结束")

	if !errors.Is(r.err, ErrInterrupted) {
		t.Fatalf("err=%v，应为 ErrInterrupted", r.err)
	}
	if r.reply != "" {
		t.Fatalf("reply=%q，应为空", r.reply)
	}
	if a.Interrupt("u1") {
		t.Fatal("Run 结束后 Interrupt 应返回 false")
	}
}

func TestInterruptIdleReturnsFalse(t *testing.T) {
	a := New(Config{LLM: newGateLLM(&LLMResult{Content: "x", FinishReason: "stop"})})
	if a.Interrupt("nobody") {
		t.Fatal("无 Run 时 Interrupt 应返回 false")
	}
}

func TestInterruptDuringToolsLeavesNoOrphan(t *testing.T) {
	// 工具执行中打断：assistant 的每个 tool_call 都必须有对应 tool result（OpenAI 协议硬性要求）。
	llm := newGateLLM(&LLMResult{
		ToolCalls: []ToolCall{
			{ID: "c1", Name: "gate", Arguments: "{}"},
			{ID: "c2", Name: "gate", Arguments: "{}"},
		},
		FinishReason: "tool_calls",
	})
	tool := &gateTool{name: "gate", started: make(chan struct{}, 4), release: make(chan struct{})}
	reg := NewRegistry()
	reg.Register(tool)
	sessions, err := NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := New(Config{LLM: llm, Tools: reg, Sessions: sessions})
	close(llm.release)

	r := startRun(a, "u1", "跑两个工具")
	waitSignal(t, tool.started, "第 1 个工具开始")

	a.Interrupt("u1")
	close(tool.release)
	waitSignal(t, r.done, "Run 结束")

	if !errors.Is(r.err, ErrInterrupted) {
		t.Fatalf("err=%v，应为 ErrInterrupted", r.err)
	}

	// 历史一致性：assistant(tool_calls=N) 后必须跟 N 条 tool 结果。
	sess := sessions.Get("u1")
	sess.Lock()
	history := append([]Message{}, sess.history...)
	sess.Unlock()
	var toolCalls, toolResults int
	for _, m := range history {
		toolCalls += len(m.ToolCalls)
		if m.Role == RoleTool {
			toolResults++
		}
	}
	if toolCalls == 0 || toolCalls != toolResults {
		t.Fatalf("tool_calls=%d tool_results=%d，应一一配对", toolCalls, toolResults)
	}
}

func TestInterruptedRunKeepsPartialHistory(t *testing.T) {
	// 被打断的 Run 保留部分历史（用户消息 + assistant 工具轮）。
	llm := newGateLLM(&LLMResult{
		ToolCalls:    []ToolCall{{ID: "c1", Name: "gate", Arguments: "{}"}},
		FinishReason: "tool_calls",
	})
	tool := &gateTool{name: "gate", started: make(chan struct{}, 4), release: make(chan struct{})}
	reg := NewRegistry()
	reg.Register(tool)
	sessions, err := NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := New(Config{LLM: llm, Tools: reg, Sessions: sessions})
	close(llm.release)

	r := startRun(a, "u1", "画只猫")
	waitSignal(t, tool.started, "工具开始")
	a.Interrupt("u1")
	close(tool.release)
	waitSignal(t, r.done, "Run 结束")

	sess := sessions.Get("u1")
	sess.Lock()
	history := sess.history
	sess.Unlock()
	if len(history) == 0 || history[0].Role != RoleUser || history[0].Content != "画只猫" {
		t.Fatalf("历史应保留用户消息，实际: %+v", history)
	}
}

func TestResetSessionClearsHistory(t *testing.T) {
	llm := newGateLLM(&LLMResult{Content: "你好", FinishReason: "stop"})
	sessions, err := NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := New(Config{LLM: llm, Sessions: sessions})
	close(llm.release)

	if _, err := a.Run(context.Background(), "u1", "hello"); err != nil {
		t.Fatal(err)
	}
	if n := len(sessions.Get("u1").History()); n != 2 {
		t.Fatalf("清空前历史应 2 条，实际 %d", n)
	}
	if err := a.ResetSession("u1"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if n := len(sessions.Get("u1").History()); n != 0 {
		t.Fatalf("清空后历史应为 0，实际 %d", n)
	}

	// 新对话不应看到旧历史。
	llm2 := newGateLLM(&LLMResult{Content: "第二句", FinishReason: "stop"})
	close(llm2.release)
	a2 := New(Config{LLM: llm2, Sessions: sessions})
	if _, err := a2.Run(context.Background(), "u1", "第二句"); err != nil {
		t.Fatal(err)
	}
	msgs := llm2.msgsOf(1)
	if len(msgs) != 1 || msgs[0].Content != "第二句" {
		t.Fatalf("reset 后新 Run 只应看到新消息，实际: %+v", msgs)
	}
}

func TestStatusAndWaitIdle(t *testing.T) {
	llm := newGateLLM(&LLMResult{Content: "ok", FinishReason: "stop"})
	a := New(Config{LLM: llm})

	if info := a.Status("u1"); info.Running {
		t.Fatal("空闲时 Running 应为 false")
	}
	if !a.WaitIdle("u1", time.Second) {
		t.Fatal("无 Run 时 WaitIdle 应立即 true")
	}

	r := startRun(a, "u1", "hi")
	waitSignal(t, llm.started, "LLM 调用开始")

	info := a.Status("u1")
	if !info.Running {
		t.Fatal("运行中 Running 应为 true")
	}
	if a.Steer("u1", NewUser("补充一")) {
		if info2 := a.Status("u1"); info2.Pending != 1 {
			t.Fatalf("Pending 应为 1，实际 %d", info2.Pending)
		}
	}
	if a.WaitIdle("u1", 50*time.Millisecond) {
		t.Fatal("Run 未结束时 WaitIdle(50ms) 应超时 false")
	}

	close(llm.release)
	waitSignal(t, r.done, "Run 结束")
	if info := a.Status("u1"); info.Running {
		t.Fatal("结束后 Running 应为 false")
	}
	if !a.WaitIdle("u1", time.Second) {
		t.Fatal("结束后 WaitIdle 应 true")
	}
}
