package bot

import (
	"context"
	"strings"
	"testing"
)

func newTestBot() *Bot {
	return New(Config{}, WithPrefixes("/"))
}

// newCtx 构造测试用 *Ctx。
func newCtx(b *Bot, source, content string) *Ctx {
	return &Ctx{Bot: b, Context: context.Background(), Source: source, Content: content, userID: "u1"}
}

// newCtxAt 构造「@了bot」的测试 *Ctx（群@消息路径）。
func newCtxAt(b *Bot, source, content string) *Ctx {
	c := newCtx(b, source, content)
	c.atBot = true
	return c
}

// nop 是空命令处理器，测试用。
var nop = func(c *Ctx) {}

// runCmd 用 matchCommand 命中后执行（测试辅助）；未命中则跳过。
func runCmd(b *Bot, c *Ctx) {
	cmd, args, _ := b.matchCommand(c)
	if cmd == nil {
		return
	}
	c.Args = args
	b.runCommand(c, cmd)
}

// dispatchOne 走完整分发（不经过中间件），测试关键词/监听器用。
func dispatchOne(b *Bot, c *Ctx) { b.core(c) }

func TestStripPrefix(t *testing.T) {
	b := newTestBot() // prefixes = ["/"]
	c := &Ctx{Bot: b, Content: "/ping x"}
	if stripped, hit := c.StripPrefix(); !hit || stripped != "ping x" {
		t.Errorf("带前缀: stripped=%q hit=%v", stripped, hit)
	}
	c2 := &Ctx{Bot: b, Content: "hello"}
	if s, hit := c2.StripPrefix(); hit || s != "hello" {
		t.Errorf("无前缀应返回原文+false: s=%q hit=%v", s, hit)
	}
}

func TestAtBot(t *testing.T) {
	c := &Ctx{atBot: true}
	if !c.AtBot() {
		t.Error("atBot=true 应返回 true")
	}
	c2 := &Ctx{}
	if c2.AtBot() {
		t.Error("默认 atBot 应为 false")
	}
}

func TestExtractQuote(t *testing.T) {
	// 引用回复：msg_elements 带被引用内容
	withQuote := []byte(`{"d":{"content":"翻译成英文","msg_elements":[{"content":"你好～","message_type":103}]}}`)
	if q := extractQuote(withQuote); q != "你好～" {
		t.Errorf("引用内容=%q want 你好～", q)
	}
	// 普通消息：无 msg_elements
	plain := []byte(`{"d":{"content":"一言"}}`)
	if q := extractQuote(plain); q != "" {
		t.Errorf("普通消息应无引用，得到 %q", q)
	}
}

func TestAbortStopsListeners(t *testing.T) {
	b := newTestBot()
	var first, second bool
	// 两个私聊监听器：第一个 Abort，第二个不应执行。
	b.OnPrivateMessage(func(c *Ctx) { first = true; c.Abort() })
	b.OnPrivateMessage(func(c *Ctx) { second = true })
	dispatchOne(b, newCtx(b, "c2c", "hi"))
	if !first {
		t.Error("第一个监听器应执行")
	}
	if second {
		t.Error("Abort 后第二个监听器不应执行")
	}
}

func TestAbortStopsKeywordThenListener(t *testing.T) {
	b := newTestBot()
	var kw, lis bool
	// 关键词触发后 Abort，监听器不应再执行。
	b.OnKeyword("早", func(c *Ctx) { kw = true; c.Abort() })
	b.OnPrivateMessage(func(c *Ctx) { lis = true })
	dispatchOne(b, newCtx(b, "c2c", "早安"))
	if !kw {
		t.Error("关键词应执行")
	}
	if lis {
		t.Error("关键词 Abort 后监听器不应执行")
	}
}

func TestNoAbortRunsAll(t *testing.T) {
	b := newTestBot()
	var n int
	b.OnPrivateMessage(func(c *Ctx) { n++ })
	b.OnPrivateMessage(func(c *Ctx) { n++ })
	dispatchOne(b, newCtx(b, "c2c", "hi"))
	if n != 2 {
		t.Errorf("不 Abort 时两个监听器都应执行，n=%d", n)
	}
}

func TestMatchCommand(t *testing.T) {
	b := newTestBot()
	b.Command("ping", nop).Alias("p")
	b.Command("echo", nop)

	cases := []struct {
		name    string
		c       *Ctx
		wantOK  bool
		wantCmd string
		raw     string
	}{
		{"群非@无前缀整条等于别名", newCtx(b, "group", "p"), true, "p", ""}, // 面板填入场景：放行
		{"群非@无前缀带参不放行", newCtx(b, "group", "ping hi"), false, "", ""},
		{"群非@子串不放行", newCtx(b, "group", "xxpingxx"), false, "", ""},
		{"群@无前缀", newCtxAt(b, "group", "ping"), true, "ping", ""},
		{"群带前缀", newCtx(b, "group", "/ping"), true, "ping", ""},
		{"私聊无前缀", newCtx(b, "c2c", "p"), true, "p", ""},
		{"群带参", newCtx(b, "group", "/echo hi there"), true, "echo", "hi there"},
		{"未知命令", newCtx(b, "group", "/nope"), false, "", ""},
		{"空内容", newCtx(b, "group", ""), false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, args, ok := b.matchCommand(tc.c)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if args.Name != tc.wantCmd {
				t.Errorf("name = %q, want %q", args.Name, tc.wantCmd)
			}
			if args.Raw != tc.raw {
				t.Errorf("raw = %q, want %q", args.Raw, tc.raw)
			}
			if cmd == nil {
				t.Error("command is nil")
			}
		})
	}
}

func TestMatchCommandMultiPrefix(t *testing.T) {
	b := New(Config{}, WithPrefixes("/", "!", "."))
	b.Command("ping", nop)

	for _, p := range []string{"/", "!", "."} {
		if cmd, _, ok := b.matchCommand(newCtx(b, "group", p+"ping")); !ok || cmd == nil {
			t.Errorf("前缀 %q 应命中", p)
		}
	}
	// 群非@且不带前缀：带参不命中（避免普通聊天误触发）
	if _, _, ok := b.matchCommand(newCtx(b, "group", "ping hi")); ok {
		t.Error("群非@无前缀带参不应命中")
	}
	// 群@bot 不带前缀 → 命中
	if cmd, _, ok := b.matchCommand(newCtxAt(b, "group", "ping")); !ok || cmd == nil {
		t.Error("群@无前缀应命中")
	}
}

func TestArgsParsing(t *testing.T) {
	b := newTestBot()
	b.Command("echo", nop)

	_, args, ok := b.matchCommand(newCtx(b, "group", "/echo a b c"))
	if !ok {
		t.Fatal("expected match")
	}
	if args.Get(0) != "a" || args.Get(1) != "b" || args.Get(2) != "c" {
		t.Errorf("args = %v", args.Args)
	}
	if args.Get(5) != "" {
		t.Errorf("越界应返回空串，got %q", args.Get(5))
	}
	if args.Join() != "a b c" {
		t.Errorf("Join = %q", args.Join())
	}
}

func TestScopeDenies(t *testing.T) {
	b := newTestBot()
	var ran bool
	b.Command("greet", func(c *Ctx) { ran = true }).GroupOnly()
	b.Command("dm", func(c *Ctx) { ran = true }).PrivateOnly()
	b.Command("ch", func(c *Ctx) { ran = true }).ChannelOnly()

	// GroupOnly 在私聊应被拒绝
	ran = false
	runCmd(b, newCtx(b, "c2c", "greet"))
	if ran {
		t.Error("GroupOnly 在私聊不应执行")
	}
	// GroupOnly 在群应放行（用 @bot 消息使命令命中）
	ran = false
	runCmd(b, newCtxAt(b, "group", "greet"))
	if !ran {
		t.Error("GroupOnly 在群应执行")
	}
	// PrivateOnly 在群应被拒绝
	ran = false
	runCmd(b, newCtxAt(b, "group", "dm"))
	if ran {
		t.Error("PrivateOnly 在群不应执行")
	}
	// PrivateOnly 在私聊应放行
	ran = false
	runCmd(b, newCtx(b, "c2c", "dm"))
	if !ran {
		t.Error("PrivateOnly 在私聊应执行")
	}
	// ChannelOnly 在群应被拒绝
	ran = false
	runCmd(b, newCtxAt(b, "group", "ch"))
	if ran {
		t.Error("ChannelOnly 在群不应执行")
	}
}

func TestState(t *testing.T) {
	b := newTestBot()

	b.UserState("u1").Set("count", 5)
	b.UserState("u1").Set("name", "alice")
	if got := b.UserState("u1").GetInt("count"); got != 5 {
		t.Errorf("count = %d, want 5", got)
	}
	if got := b.UserState("u1").GetString("name"); got != "alice" {
		t.Errorf("name = %q", got)
	}

	// 用户与群命名空间独立
	b.GroupState("g1").Set("count", 99)
	if got := b.UserState("u1").GetInt("count"); got != 5 {
		t.Errorf("user count affected by group: %d", got)
	}
	if got := b.GroupState("g1").GetInt("count"); got != 99 {
		t.Errorf("group count = %d", got)
	}

	b.UserState("u1").Delete("count")
	if _, ok := b.UserState("u1").Get("count"); ok {
		t.Error("count 应被删除")
	}
}

func TestHelp(t *testing.T) {
	b := newTestBot()
	b.Command("ping", nop).Desc("测试在线").Alias("p")
	b.Command("secret", nop).Hidden()

	help := b.Help()
	if !strings.Contains(help, "ping") || !strings.Contains(help, "测试在线") {
		t.Errorf("help 缺 ping: %q", help)
	}
	if strings.Contains(help, "secret") {
		t.Errorf("help 不应包含 hidden: %q", help)
	}
}
