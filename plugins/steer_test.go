package plugins

import (
	"context"
	"sync"
	"testing"
	"time"

	"qqbot/internal/agent"
	"qqbot/internal/bot"
)

// plugLLM 是 plugins 侧的 LLM 测试替身：先发 started 信号，等 release close 后按 seq 返回。
type plugLLM struct {
	mu      sync.Mutex
	calls   int
	msgs    [][]agent.Message
	seq     []*agent.LLMResult
	started chan struct{}
	release chan struct{}
}

func newPlugLLM(seq ...*agent.LLMResult) *plugLLM {
	return &plugLLM{seq: seq, started: make(chan struct{}, 8), release: make(chan struct{})}
}

func (g *plugLLM) Complete(_ context.Context, _ string, msgs []agent.Message, _ []map[string]any) (*agent.LLMResult, error) {
	g.mu.Lock()
	g.calls++
	n := g.calls
	g.msgs = append(g.msgs, append([]agent.Message{}, msgs...))
	g.mu.Unlock()
	g.started <- struct{}{}
	<-g.release
	if n <= len(g.seq) {
		return g.seq[n-1], nil
	}
	return g.seq[len(g.seq)-1], nil
}

func (g *plugLLM) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func (g *plugLLM) lastMsgs() []agent.Message {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.msgs) == 0 {
		return nil
	}
	return g.msgs[len(g.msgs)-1]
}

func TestSessionKey(t *testing.T) {
	b := bot.New(bot.Config{})
	g := bot.NewEventCtx(b, "group", "hi", "u1", "小明", "G1", "m1", "", true)
	if got := SessionKey(g); got != "g:G1" {
		t.Fatalf("群聊 key=%q，应 g:G1", got)
	}
	p := bot.NewEventCtx(b, "c2c", "hi", "u1", "", "", "m1", "", false)
	if got := SessionKey(p); got != "u1" {
		t.Fatalf("私聊 key=%q，应 u1", got)
	}
}

func TestAgentPluginSteerRouting(t *testing.T) {
	// 第一条消息处理中，同群第二条 @ 消息应走 Steer 注入当前 Run，
	// 而不是再起一个 Run 排队（否则 LLM 会被调用 3 次）。
	llm := newPlugLLM(
		&agent.LLMResult{Content: "先这样", FinishReason: "stop"},
		&agent.LLMResult{Content: "画好了", FinishReason: "stop"},
	)
	ag := agent.New(agent.Config{LLM: llm})
	p := NewAgent(ag)

	b := bot.New(bot.Config{})
	ts := time.Now().Format(time.RFC3339)
	c1 := bot.NewEventCtx(b, "group", "画只猫", "u1", "小明", "G1", "m1", ts, true)
	c2 := bot.NewEventCtx(b, "group", "加个蝴蝶结", "u2", "小红", "G1", "m2", ts, true)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.handle(c1) // 第一条：无活跃 Run → 正常 Run（阻塞在 LLM）
	}()

	select {
	case <-llm.started:
	case <-time.After(5 * time.Second):
		t.Fatal("等待第一条消息的 LLM 调用超时")
	}

	p.handle(c2) // 第二条：应 Steer 入队后立即返回

	close(llm.release)
	wg.Wait()

	if got := llm.callCount(); got != 2 {
		t.Fatalf("LLM 应调用 2 次（第二条走 Steer 而非新 Run），实际 %d", got)
	}
	found := false
	for _, m := range llm.lastMsgs() {
		if m.Role == agent.RoleUser && m.Content == "[小红] 加个蝴蝶结" {
			found = true
		}
	}
	if !found {
		t.Fatalf("第 2 次 LLM 调用应包含带昵称前缀的补充消息，实际: %+v", llm.lastMsgs())
	}
}

func TestAgentPluginIdleMessageStartsNewRun(t *testing.T) {
	// 无活跃 Run 时消息正常走 Run（Steer 返回 false 的回退路径）。
	llm := newPlugLLM(&agent.LLMResult{Content: "你好呀", FinishReason: "stop"})
	ag := agent.New(agent.Config{LLM: llm})
	p := NewAgent(ag)

	b := bot.New(bot.Config{})
	ts := time.Now().Format(time.RFC3339)
	c := bot.NewEventCtx(b, "c2c", "你好", "u1", "", "", "m1", ts, false)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.handle(c)
	}()
	select {
	case <-llm.started:
	case <-time.After(5 * time.Second):
		t.Fatal("等待 LLM 调用超时")
	}
	close(llm.release)
	wg.Wait()

	if got := llm.callCount(); got != 1 {
		t.Fatalf("LLM 应调用 1 次，实际 %d", got)
	}
}
