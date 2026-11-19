package bot

// C2C 流式消息 writer 测试：经 testBot 打到 httptest 假 server（真实 openapi 实例），
// 校验懒开流/续片/节流/收尾/分段/首片失败降级与 Ctx.MessageID。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/by-yitong/botgo/dto"
)

// capturedStreamReq 是一条被捕获的流式请求（路径 + 解析后的 body）。
type capturedStreamReq struct {
	path string
	body dto.StreamMessageReq
}

// streamRecorder 记录 stream_messages 收到的请求。
type streamRecorder struct {
	mu   sync.Mutex
	reqs []capturedStreamReq
}

func (r *streamRecorder) add(path string, body dto.StreamMessageReq) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, capturedStreamReq{path, body})
}

func (r *streamRecorder) all() []capturedStreamReq {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]capturedStreamReq(nil), r.reqs...)
}

func (r *streamRecorder) n() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

// streamSrv 记录请求并统一回 {"id":"S1"}（首片响应 id，续片回传校验用）。
func streamSrv(t *testing.T) (*httptest.Server, *streamRecorder) {
	t.Helper()
	rec := &streamRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var parsed dto.StreamMessageReq
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Errorf("stream_messages 请求体非 JSON: %v", err)
		}
		rec.add(req.URL.Path, parsed)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"S1"}`)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// setThrottle 临时改节流间隔，测试结束还原。
func setThrottle(t *testing.T, d time.Duration) {
	t.Helper()
	old := c2cStreamThrottle
	c2cStreamThrottle = d
	t.Cleanup(func() { c2cStreamThrottle = old })
}

// (a) 首片：懒开流、路径、组包（state=1/index=0/锚点/replace/markdown/seq=1）、响应 id 存流。
func TestC2CStreamFirstDelta(t *testing.T) {
	srv, rec := streamSrv(t)
	b := testBot(t, srv)
	w := NewC2CStreamWriter(b, "U1", "ANCHOR")
	if w.Delivered() {
		t.Fatal("开流前 Delivered() 应为 false")
	}
	w.OnDelta("你好")
	reqs := rec.all()
	if len(reqs) != 1 {
		t.Fatalf("want 1 request, got %d", len(reqs))
	}
	if reqs[0].path != "/v2/users/U1/stream_messages" {
		t.Errorf("path=%q", reqs[0].path)
	}
	got := reqs[0].body
	if got.InputState != 1 || got.Index != 0 {
		t.Errorf("首片 state/index 错: %+v", got)
	}
	if got.MsgID != "ANCHOR" || got.InputMode != "replace" || got.ContentType != "markdown" {
		t.Errorf("首片锚点/模式/类型错: %+v", got)
	}
	if got.ContentRaw != "你好" || got.StreamMsgID != "" || got.MsgSeq != 1 {
		t.Errorf("首片内容/流id/seq 错: %+v", got)
	}
	if !w.Delivered() {
		t.Error("首片成功后 Delivered() 应为 true")
	}
}

// (b) 续片：stream_msg_id 回传、index/msg_seq 递增；节流：默认间隔内快速两次只推一次，
// 被跳过的内容不丢（Finish 全量带上）。
func TestC2CStreamContinuationAndThrottle(t *testing.T) {
	srv, rec := streamSrv(t)
	b := testBot(t, srv)
	setThrottle(t, time.Millisecond)

	w := NewC2CStreamWriter(b, "U1", "ANCHOR")
	w.OnDelta("a")
	time.Sleep(2 * time.Millisecond)
	w.OnDelta("ab")
	reqs := rec.all()
	if len(reqs) != 2 {
		t.Fatalf("want 2 requests, got %d", len(reqs))
	}
	second := reqs[1].body
	if second.StreamMsgID != "S1" || second.Index != 1 || second.MsgSeq != 2 {
		t.Errorf("续片 stream_id/index/seq 错: %+v", second)
	}
	if second.InputState != 1 || second.ContentRaw != "ab" || second.MsgID != "" {
		t.Errorf("续片 state/内容/锚点错（续片不带 msg_id）: %+v", second)
	}

	c2cStreamThrottle = 500 * time.Millisecond
	w2 := NewC2CStreamWriter(b, "U1", "ANCHOR")
	w2.OnDelta("x")
	w2.OnDelta("xy")
	if rec.n() != 3 {
		t.Fatalf("节流后应仍为 3 次请求, got %d", rec.n())
	}
	if err := w2.Finish("xy"); err != nil {
		t.Fatal(err)
	}
	fin := rec.all()[3].body
	if fin.InputState != 10 || fin.ContentRaw != "xy" || fin.Index != 1 || fin.StreamMsgID != "S1" {
		t.Errorf("收尾片应推最终全文: %+v", fin)
	}
}

// (c) Finish：state=10 + 最终全文全量；未开流 Finish 返回 nil 且不发请求；收尾后全部 no-op。
func TestC2CStreamFinish(t *testing.T) {
	srv, rec := streamSrv(t)
	b := testBot(t, srv)
	w := NewC2CStreamWriter(b, "U1", "ANCHOR")
	if err := w.Finish("从未开流"); err != nil {
		t.Errorf("未开流 Finish 应返回 nil: %v", err)
	}
	if rec.n() != 0 {
		t.Fatalf("未开流不应发请求, got %d", rec.n())
	}
	w.OnDelta("部分")
	if err := w.Finish("最终全文"); err != nil {
		t.Fatal(err)
	}
	fin := rec.all()[1].body
	if fin.InputState != 10 || fin.ContentRaw != "最终全文" ||
		fin.Index != 1 || fin.StreamMsgID != "S1" || fin.MsgSeq != 2 {
		t.Errorf("收尾片错: %+v", fin)
	}
	w.OnDelta("更多")
	_ = w.Finish("再次")
	w.OnSegmentBreak()
	w.Abort()
	if rec.n() != 2 {
		t.Errorf("Finish 成功后不应再发请求, got %d", rec.n())
	}
}

// (d) OnSegmentBreak：收尾片带已推送文本；之后再 OnDelta 开新消息（index/msg_seq 重置、
// 不带 stream_msg_id）；无流时 Abort/OnSegmentBreak no-op。
func TestC2CStreamSegmentBreak(t *testing.T) {
	srv, rec := streamSrv(t)
	b := testBot(t, srv)
	setThrottle(t, time.Millisecond)

	w := NewC2CStreamWriter(b, "U1", "ANCHOR")
	w.OnDelta("seg1")
	w.OnSegmentBreak()
	brk := rec.all()[1].body
	if brk.InputState != 10 || brk.ContentRaw != "seg1" || brk.StreamMsgID != "S1" {
		t.Errorf("分段收尾片错: %+v", brk)
	}
	w.Abort()
	if rec.n() != 2 {
		t.Fatalf("无流时 Abort 应 no-op, got %d 次请求", rec.n())
	}
	time.Sleep(2 * time.Millisecond)
	w.OnDelta("seg2")
	reqs := rec.all()
	if len(reqs) != 3 {
		t.Fatalf("want 3 requests, got %d", len(reqs))
	}
	third := reqs[2].body
	if third.StreamMsgID != "" || third.Index != 0 || third.MsgSeq != 1 {
		t.Errorf("新段首片应重置 index/seq 且不带 stream_msg_id: %+v", third)
	}
	if third.InputState != 1 || third.MsgID != "ANCHOR" || third.ContentRaw != "seg2" {
		t.Errorf("新段首片组包错: %+v", third)
	}
}

// (e) 首片失败（502）：failed 降级——Delivered()=false，此后一切调用不再发请求，Finish 返回 nil。
func TestC2CStreamFirstFailureDegrades(t *testing.T) {
	calls := 0
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `{"message":"upstream","code":-1}`)
	}))
	t.Cleanup(srv.Close)
	b := testBot(t, srv)
	w := NewC2CStreamWriter(b, "U1", "ANCHOR")

	w.OnDelta("x")
	mu.Lock()
	n := calls
	mu.Unlock()
	if n != 1 {
		t.Fatalf("want 1 request, got %d", n)
	}
	if w.Delivered() {
		t.Error("首片失败 Delivered() 应为 false")
	}
	w.OnDelta("y")
	w.OnSegmentBreak()
	w.Abort()
	if err := w.Finish("final"); err != nil {
		t.Errorf("failed 后 Finish 应返回 nil: %v", err)
	}
	mu.Lock()
	n = calls
	mu.Unlock()
	if n != 1 {
		t.Errorf("首片失败后不应再发请求, got %d 次", n)
	}
}

// (f) Ctx.MessageID() 返回构造时的 messageID（事件构造与 fromMessage 两条路径）。
func TestCtxMessageID(t *testing.T) {
	b := newTestBot()
	c := NewEventCtx(b, "c2c", "hi", "u1", "nick", "", "M123", "", false)
	if got := c.MessageID(); got != "M123" {
		t.Errorf("NewEventCtx MessageID()=%q want M123", got)
	}
	m := &dto.Message{ID: "M456", Content: "x", Author: &dto.User{ID: "u2"}}
	if got := b.fromMessage(context.Background(), "c2c", m, false).MessageID(); got != "M456" {
		t.Errorf("fromMessage MessageID()=%q want M456", got)
	}
}
