package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// sseServer 起一个 /chat/completions 端点：第 i 次请求原样回 rounds[i-1] 的 SSE 文本
// （超出脚本长度则重复最后一段）。hook 记录每次请求供断言。
func sseServer(t *testing.T, rounds []string, hook func(n int, r *http.Request, body string)) *httptest.Server {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1))
		var body string
		if r.Body != nil {
			buf := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
			body = string(buf)
		}
		if hook != nil {
			hook(i, r, body)
		}
		round := rounds[len(rounds)-1]
		if i <= len(rounds) {
			round = rounds[i-1]
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, line := range strings.Split(round, "\n") {
			_, _ = fmt.Fprintln(w, line)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCompleteStreamTextChunks(t *testing.T) {
	// (a)(c) 纯文本多 chunk：onDelta 收递增累积串；空行/注释行/无空格 data:/事件行均跳过；
	// [DONE] 结束（其后的 GARBAGE 不再解析）；末 chunk 携带 usage。
	sse := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"你"}}]}

: keep-alive

data:{"choices":[{"delta":{"content":"好"}}]}

event: ping
data: {"choices":[{"delta":{"content":"!"}}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13,"prompt_tokens_details":{"cached_tokens":4}}}

data: [DONE]

data: {"choices":[{"delta":{"content":"GARBAGE"}}]}
`
	var got []string
	srv := sseServer(t, []string{sse}, func(n int, r *http.Request, body string) {
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("Authorization 头错: %q", r.Header.Get("Authorization"))
		}
		if !strings.Contains(body, `"stream":true`) {
			t.Errorf("请求体应含 stream:true: %s", body)
		}
	})
	c := NewLLMClient(srv.URL, "k", "glm-5-turbo")
	res, err := c.CompleteStream(context.Background(), "sys", []Message{NewUser("hi")}, nil, func(accum string) {
		got = append(got, accum)
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"你", "你好", "你好!"}
	if len(got) != len(want) {
		t.Fatalf("onDelta 序列=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("onDelta[%d]=%q want %q", i, got[i], want[i])
		}
	}
	if res.Content != "你好!" || strings.Contains(res.Content, "GARBAGE") {
		t.Fatalf("Content=%q", res.Content)
	}
	if res.FinishReason != "stop" {
		t.Fatalf("FinishReason=%q", res.FinishReason)
	}
	if res.ToolCalls != nil {
		t.Fatalf("纯文本流不应有 ToolCalls: %+v", res.ToolCalls)
	}
	if res.Usage == nil || res.Usage.PromptTokens != 10 || res.Usage.CompletionTokens != 3 ||
		res.Usage.TotalTokens != 13 || !res.Usage.CacheSupported || res.Usage.CachedTokens != 4 {
		t.Fatalf("usage 解析错: %+v", res.Usage)
	}
}

func TestCompleteStreamToolCallsAggregation(t *testing.T) {
	// (b) content + tool_calls 混合：arguments 分片拼接；index 乱序到达仍按 index 排序。
	sse := `data: {"choices":[{"delta":{"content":"让我查查"}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"echo","arguments":""}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","function":{"name":"ping","arguments":"{\"p\":1}"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"x\":"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"hi\"}"}}]}}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]
`
	srv := sseServer(t, []string{sse}, nil)
	c := NewLLMClient(srv.URL, "k", "m")
	var deltas []string
	res, err := c.CompleteStream(context.Background(), "", []Message{NewUser("q")}, nil, func(accum string) {
		deltas = append(deltas, accum)
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Content != "让我查查" || len(deltas) != 1 || deltas[0] != "让我查查" {
		t.Fatalf("Content=%q deltas=%v", res.Content, deltas)
	}
	if res.FinishReason != "tool_calls" {
		t.Fatalf("FinishReason=%q", res.FinishReason)
	}
	if res.Usage != nil {
		t.Fatalf("未带 usage 时 Usage 应为 nil: %+v", res.Usage)
	}
	want := []ToolCall{
		{ID: "call_1", Name: "echo", Arguments: `{"x":"hi"}`},
		{ID: "call_2", Name: "ping", Arguments: `{"p":1}`},
	}
	if len(res.ToolCalls) != len(want) {
		t.Fatalf("ToolCalls=%+v", res.ToolCalls)
	}
	for i := range want {
		if res.ToolCalls[i] != want[i] {
			t.Fatalf("ToolCalls[%d]=%+v want %+v", i, res.ToolCalls[i], want[i])
		}
	}
}

func TestCompleteStreamNilOnDelta(t *testing.T) {
	// (e) onDelta=nil 不 panic，照常累积。
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"abc\"}}]}\n\ndata: [DONE]\n"
	srv := sseServer(t, []string{sse}, nil)
	c := NewLLMClient(srv.URL, "k", "m")
	res, err := c.CompleteStream(context.Background(), "", []Message{NewUser("q")}, nil, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Content != "abc" {
		t.Fatalf("Content=%q", res.Content)
	}
}

func TestCompleteStreamHTTPError(t *testing.T) {
	// (d) 非 2xx：报错且不重试（SSE 语义，只打一次）；溢出错误照旧透出 ErrContextOverflow。
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	t.Cleanup(srv.Close)
	c := NewLLMClient(srv.URL, "k", "m")
	_, err := c.CompleteStream(context.Background(), "", []Message{NewUser("q")}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err=%v，应含状态码与 body", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("流式请求不应重试，实际 %d 次", hits.Load())
	}

	ovf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write([]byte("too large"))
	}))
	t.Cleanup(ovf.Close)
	c2 := NewLLMClient(ovf.URL, "k", "m")
	if _, err := c2.CompleteStream(context.Background(), "", []Message{NewUser("q")}, nil, nil); !errors.Is(err, ErrContextOverflow) {
		t.Fatalf("413 应透出 ErrContextOverflow，得到 %v", err)
	}

	ovf400 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 8192 tokens"}}`))
	}))
	t.Cleanup(ovf400.Close)
	c3 := NewLLMClient(ovf400.URL, "k", "m")
	if _, err := c3.CompleteStream(context.Background(), "", []Message{NewUser("q")}, nil, nil); !errors.Is(err, ErrContextOverflow) {
		t.Fatalf("400+context length 应透出 ErrContextOverflow，得到 %v", err)
	}
}

// streamFake 在 fakeCompleter 之上补一个流式方法：结果同源，content 经 onDelta 一次性透出。
type streamFake struct {
	*fakeCompleter
	streamCalls int
}

func (s *streamFake) CompleteStream(ctx context.Context, system string, msgs []Message, tools []map[string]any, onDelta func(string)) (*LLMResult, error) {
	s.streamCalls++
	res, err := s.Complete(ctx, system, msgs, tools)
	if err != nil {
		return nil, err
	}
	if onDelta != nil && res.Content != "" {
		onDelta(res.Content)
	}
	return res, nil
}

func TestFailoverStreamUsesActiveBackend(t *testing.T) {
	// 主备均可流式：未熔断走主模型；熔断后流式调用也走备用。
	primary := &streamFake{fakeCompleter: &fakeCompleter{errs: []error{
		errors.New("HTTP 502"), errors.New("HTTP 502"),
	}}}
	fallback := &streamFake{fakeCompleter: &fakeCompleter{results: []*LLMResult{{Content: "备用"}}}}
	fo := NewFailover(primary, fallback, WithBreakerThreshold(2), WithProbeInterval(10*time.Second))
	defer fo.Close()

	var deltas []string
	_, err := fo.CompleteStream(context.Background(), "", []Message{NewUser("q")}, nil, func(a string) { deltas = append(deltas, a) })
	if err == nil {
		t.Fatal("主模型失败应返回错误")
	}
	if primary.streamCalls != 1 || fallback.streamCalls != 0 {
		t.Fatalf("第 1 次应走主模型流式: primary=%d fallback=%d", primary.streamCalls, fallback.streamCalls)
	}
	got, err := fo.CompleteStream(context.Background(), "", []Message{NewUser("q")}, nil, func(a string) { deltas = append(deltas, a) })
	if err != nil || got.Content != "备用" {
		t.Fatalf("熔断后流式应走备用: err=%v got=%+v", err, got)
	}
	if fallback.streamCalls != 1 {
		t.Fatalf("备用流式应被调用 1 次，实际 %d", fallback.streamCalls)
	}
	if len(deltas) != 1 || deltas[0] != "备用" {
		t.Fatalf("deltas=%v", deltas)
	}
	if !fo.UsingFallback() {
		t.Fatal("应处于熔断状态")
	}
}

func TestFailoverStreamUnsupported(t *testing.T) {
	// 后端未实现 StreamCompleter：返回 ErrStreamUnsupported，不计熔断、不落地 Complete。
	primary := &fakeCompleter{}
	fo := NewFailover(primary, nil, WithProbeInterval(10*time.Second))
	defer fo.Close()
	_, err := fo.CompleteStream(context.Background(), "", []Message{NewUser("q")}, nil, nil)
	if !errors.Is(err, ErrStreamUnsupported) {
		t.Fatalf("err=%v，应为 ErrStreamUnsupported", err)
	}
	if primary.calls != 0 {
		t.Fatalf("不应回退调 Complete，实际 %d 次", primary.calls)
	}
	if fo.UsingFallback() {
		t.Fatal("ErrStreamUnsupported 不应触发熔断")
	}
}

// recSink 记录流式回调的 sink 替身。
type recSink struct {
	mu     sync.Mutex
	deltas []string
	breaks int
}

func (s *recSink) OnDelta(accum string) {
	s.mu.Lock()
	s.deltas = append(s.deltas, accum)
	s.mu.Unlock()
}

func (s *recSink) OnSegmentBreak() {
	s.mu.Lock()
	s.breaks++
	s.mu.Unlock()
}

func (s *recSink) snapshot() ([]string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.deltas...), s.breaks
}

func TestAgentStreamSinkSegments(t *testing.T) {
	// 第 1 轮：流式回 content+tool_calls；第 2 轮：纯文本流。
	// 断言：分段信号恰一次（第 2 轮发起前）、最终 lastReply 正确、Run 返回值不变。
	round1 := `data: {"choices":[{"delta":{"content":"让我查查"}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"echo","arguments":"{\"q\":\"猫\"}"}}]}}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]
`
	round2 := `data: {"choices":[{"delta":{"content":"画好了"}}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}]}

data: [DONE]
`
	srv := sseServer(t, []string{round1, round2}, nil)
	reg := NewRegistry()
	tool := &stubTool{name: "echo"}
	reg.Register(tool)
	a := New(Config{LLM: NewLLMClient(srv.URL, "k", "m"), Tools: reg})

	sink := &recSink{}
	out, err := a.Run(context.Background(), "u1", "画只猫", WithStreamSink(sink))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out != "画好了" {
		t.Fatalf("Run 返回=%q", out)
	}
	deltas, breaks := sink.snapshot()
	if breaks != 1 {
		t.Fatalf("OnSegmentBreak 应恰好 1 次，实际 %d", breaks)
	}
	if len(deltas) != 2 || deltas[0] != "让我查查" || deltas[1] != "画好了" {
		t.Fatalf("deltas=%v", deltas)
	}
	if tool.calls != 1 || tool.lastArgs != `{"q":"猫"}` {
		t.Fatalf("工具执行错: calls=%d args=%q", tool.calls, tool.lastArgs)
	}
}

// unsupportedStreamLLM 声称支持流式、实际返回 ErrStreamUnsupported 的替身（验证 agent 降级与锁存）。
type unsupportedStreamLLM struct {
	stubLLM
	streamCalls int
}

func (u *unsupportedStreamLLM) CompleteStream(context.Context, string, []Message, []map[string]any, func(string)) (*LLMResult, error) {
	u.streamCalls++
	return nil, ErrStreamUnsupported
}

func TestAgentStreamUnsupportedFallsBackOnce(t *testing.T) {
	// ErrStreamUnsupported：本轮立即改走 Complete 并锁存——后续轮次不再探测流式。
	llm := &unsupportedStreamLLM{stubLLM: stubLLM{seq: []*LLMResult{
		{ToolCalls: []ToolCall{{ID: "1", Name: "echo", Arguments: "{}"}}, FinishReason: "tool_calls"},
		{Content: "done", FinishReason: "stop"},
	}}}
	reg := NewRegistry()
	reg.Register(&stubTool{name: "echo"})
	a := New(Config{LLM: llm, Tools: reg})

	sink := &recSink{}
	out, err := a.Run(context.Background(), "u1", "go", WithStreamSink(sink))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out != "done" {
		t.Fatalf("out=%q", out)
	}
	if llm.streamCalls != 1 {
		t.Fatalf("流式只应探测 1 次，实际 %d", llm.streamCalls)
	}
	if llm.calls != 2 {
		t.Fatalf("两轮都应走 Complete，实际 %d 次", llm.calls)
	}
	deltas, breaks := sink.snapshot()
	if len(deltas) != 0 || breaks != 0 {
		t.Fatalf("降级后 sink 不应收到回调: deltas=%v breaks=%d", deltas, breaks)
	}
}

func TestAgentStreamSinkWithNonStreamLLM(t *testing.T) {
	// LLM 不支持流式 + 带 sink：安静走 Complete，sink 零回调。
	llm := &stubLLM{seq: []*LLMResult{{Content: "hi", FinishReason: "stop"}}}
	a := New(Config{LLM: llm})
	sink := &recSink{}
	out, err := a.Run(context.Background(), "u1", "hello", WithStreamSink(sink))
	if err != nil || out != "hi" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	deltas, breaks := sink.snapshot()
	if len(deltas) != 0 || breaks != 0 {
		t.Fatalf("sink 不应收到回调: deltas=%v breaks=%d", deltas, breaks)
	}
}
