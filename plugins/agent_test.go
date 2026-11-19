package plugins

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qqbot/internal/agent"
	"qqbot/internal/bot"
	"qqbot/internal/memory"
)

func TestItoa(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"}, {1, "1"}, {9, "9"}, {10, "10"}, {42, "42"}, {1254, "1254"},
	}
	for _, tc := range cases {
		if got := itoa(tc.in); got != tc.want {
			t.Errorf("itoa(%d)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestScopeOf(t *testing.T) {
	p := &AgentPlugin{}
	c := bot.NewEventCtx(nil, "group", "hi", "u1", "小明", "g9", "m1", "", true)
	if sc := p.scopeOf(c); sc.Source != "group" || sc.GroupID != "g9" {
		t.Errorf("群 scope 错: %+v", sc)
	}
	c2 := bot.NewEventCtx(nil, "c2c", "hi", "u1", "", "", "m2", "", false)
	if sc := p.scopeOf(c2); sc.Source != "c2c" || sc.UserID != "u1" {
		t.Errorf("私聊 scope 错: %+v", sc)
	}
}

func TestCtxBlockOfIncremental(t *testing.T) {
	s, err := memory.Open(t.TempDir() + "/rb.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := NewAgent(nil, WithMemory(s, 60*time.Minute, 30))
	mkCtx := func(msgID string, at time.Time) *bot.Ctx {
		return bot.NewEventCtx(nil, "group", "hi", "u1", "小明", "gx", msgID, at.Format(time.RFC3339), true)
	}
	rec := func(id, user, name, content string, at time.Time) {
		s.Record(memory.Message{MsgID: id, Ts: at, Source: "group", GroupID: "gx", UserID: user, Username: name, Content: content})
	}

	// 无记录 → 不注入
	if out := p.ctxBlockOf(mkCtx("m0", time.Now())); out != "" {
		t.Errorf("无记录应返回空, got %q", out)
	}

	// 时间线：m1 闲聊 → m2 bot 回复 → m3 第一次 @bot（当前消息）。
	// 时间戳统一秒级精度（生产中两侧都来自 QQ 事件的 RFC3339，同精度才可比较）。
	now := time.Now().Truncate(time.Second)
	t1 := now.Add(-8 * time.Minute)
	t2 := now.Add(-7 * time.Minute)
	t3 := now.Add(-6 * time.Minute)
	rec("m1", "u1", "小明", "刚聊的事", t1)
	rec("m2", "bot", "机器人", "bot 的回复", t2)
	rec("m3", "u2", "小红", "当前这条", t3)
	waitRecords(t, s, memory.Scope{Source: "group", GroupID: "gx"}, 3)

	// 首次（无标记）→ 全量窗口回退：排除 bot 与当前消息
	out := p.ctxBlockOf(mkCtx("m3", t3))
	if !strings.Contains(out, "最近 1小时") || !strings.Contains(out, "小明: 刚聊的事") {
		t.Errorf("首次应全量注入: %q", out)
	}
	if strings.Contains(out, "bot 的回复") || strings.Contains(out, "小红: 当前这条") {
		t.Errorf("应排除 bot 回复与当前消息: %q", out)
	}

	// 第二次 @（t4）：t3→t4 之间没人说话 → 不注入
	t4 := now.Add(-4 * time.Minute)
	if out := p.ctxBlockOf(mkCtx("m4", t4)); out != "" {
		t.Errorf("无增量应返回空, got %q", out)
	}

	// t4 之后有人说话（t5），第三次 @（t6）→ 只注入增量，不重复旧内容
	t5 := now.Add(-2 * time.Minute)
	t6 := now.Add(-1 * time.Minute)
	rec("m5", "u2", "小红", "新增的闲聊", t5)
	waitRecords(t, s, memory.Scope{Source: "group", GroupID: "gx"}, 4)
	out = p.ctxBlockOf(mkCtx("m6", t6))
	if !strings.Contains(out, "上次对话以来的新消息") || !strings.Contains(out, "小红: 新增的闲聊") {
		t.Errorf("应增量注入: %q", out)
	}
	if strings.Contains(out, "刚聊的事") || strings.Contains(out, "当前这条") {
		t.Errorf("增量不应重复旧内容或上一轮问题: %q", out)
	}

	// 私聊 → 不注入（每条消息本就走 agent，都在 session 里）
	c2c := bot.NewEventCtx(nil, "c2c", "hi", "u1", "", "", "m7", now.Format(time.RFC3339), false)
	if out := p.ctxBlockOf(c2c); out != "" {
		t.Errorf("私聊不应注入, got %q", out)
	}

	// 无记忆库 → 不注入
	p2 := &AgentPlugin{}
	if out := p2.ctxBlockOf(mkCtx("m8", now)); out != "" {
		t.Errorf("无记忆库应返回空, got %q", out)
	}
}

// TestGapMarkerPersistence marker 落盘后重建插件（模拟重启）：增量无缝续上，
// 不回退全量窗口、不重复注入已记录的内容；ResetMarker 清掉后回到全量铺底。
func TestGapMarkerPersistence(t *testing.T) {
	s, err := memory.Open(t.TempDir() + "/rb.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	gapFile := t.TempDir() + "/markers.json"
	mkCtx := func(msgID string, at time.Time) *bot.Ctx {
		return bot.NewEventCtx(nil, "group", "hi", "u1", "小明", "gx", msgID, at.Format(time.RFC3339), true)
	}
	rec := func(id, user, name, content string, at time.Time) {
		s.Record(memory.Message{MsgID: id, Ts: at, Source: "group", GroupID: "gx", UserID: user, Username: name, Content: content})
	}

	now := time.Now().Truncate(time.Second)
	rec("m1", "u1", "小明", "铺底消息", now.Add(-5*time.Minute))
	rec("m2", "u2", "小红", "第一条@的问题", now.Add(-2*time.Minute))
	waitRecords(t, s, memory.Scope{Source: "group", GroupID: "gx"}, 2)

	// 第一次：全量铺底（排除当前消息），marker 落盘
	p1 := NewAgent(nil, WithMemory(s, 60*time.Minute, 30), WithGapFile(gapFile))
	if out := p1.ctxBlockOf(mkCtx("m2", now.Add(-2*time.Minute))); !strings.Contains(out, "铺底消息") {
		t.Fatalf("首次应全量铺底: %q", out)
	}

	// 重建插件（同文件）：marker 应已加载——只注入此后的新消息，不重复旧内容。
	// m3 是两次 @ 之间的新闲聊，m4 是当前这条 @。
	rec("m3", "u1", "小明", "重启后的新消息", now.Add(-1*time.Minute))
	rec("m4", "u2", "小红", "第二条@的问题", now.Add(-30*time.Second))
	waitRecords(t, s, memory.Scope{Source: "group", GroupID: "gx"}, 4)
	p2 := NewAgent(nil, WithMemory(s, 60*time.Minute, 30), WithGapFile(gapFile))
	out := p2.ctxBlockOf(mkCtx("m4", now.Add(-30*time.Second)))
	if !strings.Contains(out, "重启后的新消息") {
		t.Errorf("重启后应只注入新消息: %q", out)
	}
	if strings.Contains(out, "铺底消息") || strings.Contains(out, "第一条@的问题") {
		t.Errorf("重启后不应重复旧内容: %q", out)
	}

	// ResetMarker（g: 前缀的群 key）：清掉后回到全量铺底
	p2.ResetMarker("g:gx")
	if out := p2.ctxBlockOf(mkCtx("m4", now.Add(-30*time.Second))); !strings.Contains(out, "最近 1小时") || !strings.Contains(out, "铺底消息") {
		t.Errorf("清 marker 后应重新全量铺底: %q", out)
	}
	// 非群 key no-op
	p2.ResetMarker("some-openid")
}

func TestIsRemoteImage(t *testing.T) {
	remote := []string{"http://x/a.png", "https://x/a.jpg", "genimg:abc123"}
	for _, s := range remote {
		if !isRemoteImage(s) {
			t.Errorf("%q 应视为远程图片", s)
		}
	}
	local := []string{"/tmp/screenshot-1.png", "~/.agent-browser/tmp/screenshots/s.png", "data/images/x.jpg", "a.png"}
	for _, s := range local {
		if isRemoteImage(s) {
			t.Errorf("%q 应视为本地路径", s)
		}
	}
}

func TestShortenDur(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Minute, "30分钟"},
		{60 * time.Minute, "1小时"},
		{90 * time.Minute, "1小时30分钟"},
	}
	for _, tc := range cases {
		if got := shortenDur(tc.in); got != tc.want {
			t.Errorf("shortenDur(%v)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestWithStreamingOption(t *testing.T) {
	if p := NewAgent(nil); !p.streaming {
		t.Error("NewAgent 默认应开启单聊流式回复")
	}
	if p := NewAgent(nil, WithStreaming(false)); p.streaming {
		t.Error("WithStreaming(false) 应关闭流式")
	}
	if p := NewAgent(nil, WithStreaming(true)); !p.streaming {
		t.Error("WithStreaming(true) 应开启流式")
	}
}

func TestUseStream(t *testing.T) {
	p := NewAgent(nil)
	if !p.useStream(bot.NewEventCtx(nil, "c2c", "hi", "u1", "", "", "m1", "", false)) {
		t.Error("私聊且开关开应走流式")
	}
	for _, src := range []string{"group", "channel"} {
		if p.useStream(bot.NewEventCtx(nil, src, "hi", "u1", "", "g9", "m1", "", true)) {
			t.Errorf("%s 不应走流式", src)
		}
	}
	off := NewAgent(nil, WithStreaming(false))
	if off.useStream(bot.NewEventCtx(nil, "c2c", "hi", "u1", "", "", "m1", "", false)) {
		t.Error("开关关时私聊也不应走流式")
	}
}

// sseLLMServer 起一个回 SSE 流式补全的假 LLM（单轮流，回固定文本）。
func sseLLMServer(t *testing.T, text string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, line := range []string{
			`data: {"choices":[{"delta":{"content":"` + text + `"}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
			"",
		} {
			_, _ = fmt.Fprintln(w, line)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// captureLog 临时接管标准 log 输出，返回缓冲区（测试结束自动还原）。
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return &buf
}

// TestStreamingSinkPath handle 接线冒烟：单聊+开关开时 Run 挂流式 sink（OnDelta 触发
// 首片推送；Bot 未连接使首片失败，打降级日志后走静态路径，不 panic）；群聊与关流式
// 不构造 writer、不发流式请求（无降级日志）。
func TestStreamingSinkPath(t *testing.T) {
	llmURL := sseLLMServer(t, "你好呀").URL
	mkPlugin := func(streaming bool) *AgentPlugin {
		ag := agent.New(agent.Config{LLM: agent.NewLLMClient(llmURL, "k", "m"), Tools: agent.NewRegistry()})
		return NewAgent(ag, WithStreaming(streaming))
	}
	degradeLog := "流式消息首片发送失败"

	t.Run("私聊+开关开", func(t *testing.T) {
		p := mkPlugin(true)
		buf := captureLog(t)
		c := bot.NewEventCtx(bot.New(bot.Config{}), "c2c", "hi", "u1", "", "", "m1", "", false)
		p.handle(c) // 不 panic 即接线正确：首片失败降级、静态发送 api 未连接静默失败
		if !strings.Contains(buf.String(), degradeLog) {
			t.Errorf("单聊流式应触发 sink 首片推送（未打降级日志）: %q", buf.String())
		}
	})
	t.Run("群聊不走流式", func(t *testing.T) {
		p := mkPlugin(true)
		buf := captureLog(t)
		c := bot.NewEventCtx(bot.New(bot.Config{}), "group", "hi", "u1", "小明", "g9", "m1", "", true)
		p.handle(c)
		if strings.Contains(buf.String(), degradeLog) {
			t.Error("群聊不应构造流式 writer")
		}
	})
	t.Run("关流式不走流式", func(t *testing.T) {
		p := mkPlugin(false)
		buf := captureLog(t)
		c := bot.NewEventCtx(bot.New(bot.Config{}), "c2c", "hi", "u1", "", "", "m1", "", false)
		p.handle(c)
		if strings.Contains(buf.String(), degradeLog) {
			t.Error("关流式后不应构造流式 writer")
		}
	})
}
