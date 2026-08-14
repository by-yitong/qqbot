package plugins

import (
	"strings"
	"testing"
	"time"

	"qqbot/internal/bot"
	"qqbot/internal/memory"
)

func TestAppendImagesToQuestion(t *testing.T) {
	// 单张：内联一行
	out := appendImagesToQuestion("画只猫", []bot.Image{{URL: "https://x/a.png"}})
	if !strings.Contains(out, "画只猫") {
		t.Errorf("应保留原文本: %q", out)
	}
	if !strings.Contains(out, "[用户发送了1张图片，URL：https://x/a.png]") {
		t.Errorf("单张内联格式错: %q", out)
	}

	// 多张：编号列表
	out = appendImagesToQuestion("", []bot.Image{
		{URL: "https://x/a.png"},
		{URL: "https://x/b.jpg"},
	})
	if !strings.Contains(out, "2张图片") {
		t.Errorf("应标注2张: %q", out)
	}
	if !strings.Contains(out, "1. https://x/a.png") || !strings.Contains(out, "2. https://x/b.jpg") {
		t.Errorf("多张编号格式错: %q", out)
	}
	// 空文本多张：不应有前导换行
	if strings.HasPrefix(out, "\n") {
		t.Errorf("空文本不应前导换行: %q", out)
	}

	// 原文本 + 多张：中间用空行隔开
	out = appendImagesToQuestion("把这两张合并", []bot.Image{
		{URL: "https://x/a.png"},
		{URL: "https://x/b.jpg"},
	})
	if !strings.HasPrefix(out, "把这两张合并") {
		t.Errorf("应以原文本开头: %q", out)
	}
	if !strings.Contains(out, "\n\n[用户发送了") {
		t.Errorf("原文本与图片段间应有空行: %q", out)
	}
}

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
