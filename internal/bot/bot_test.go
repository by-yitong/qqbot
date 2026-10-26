package bot

import (
	"context"
	"strings"
	"testing"

	"qqbot/pkg/botgo/dto"
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
		{"群非@无前缀整条等于别名", newCtx(b, "group", "p"), true, "p", ""},           // 面板填入场景
		{"群非@无前缀带参也命中", newCtx(b, "group", "ping hi"), true, "ping", "hi"}, // 裸文本默认可命中
		{"群非@子串不命中", newCtx(b, "group", "xxpingxx"), false, "", ""},
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
	// 群非@且不带前缀：裸文本默认也命中（发送 xxx 等价 .xxx）
	if cmd, _, ok := b.matchCommand(newCtx(b, "group", "ping hi")); !ok || cmd == nil {
		t.Error("群非@无前缀带参应命中")
	}
	// 群@bot 不带前缀 → 命中
	if cmd, _, ok := b.matchCommand(newCtxAt(b, "group", "ping")); !ok || cmd == nil {
		t.Error("群@无前缀应命中")
	}
}

// TestMatchCommandRequireAtOrPrefix 验证 RequireAtOrPrefix：裸文本不命中，
// 带前缀或 @机器人 才命中（私聊无法 @，即必须前缀）。
func TestMatchCommandRequireAtOrPrefix(t *testing.T) {
	b := newTestBot()
	b.Command("admin", nop).RequireAtOrPrefix()

	if _, _, ok := b.matchCommand(newCtx(b, "group", "admin x")); ok {
		t.Error("群裸文本不应命中 RequireAtOrPrefix 命令")
	}
	if _, _, ok := b.matchCommand(newCtx(b, "group", "admin")); ok {
		t.Error("群裸文本（整条等于命令名，面板场景）也不应命中")
	}
	if _, _, ok := b.matchCommand(newCtx(b, "c2c", "admin x")); ok {
		t.Error("私聊裸文本不应命中 RequireAtOrPrefix 命令")
	}
	if _, _, ok := b.matchCommand(newCtx(b, "group", "/admin x")); !ok {
		t.Error("群带前缀应命中")
	}
	if _, _, ok := b.matchCommand(newCtxAt(b, "group", "admin x")); !ok {
		t.Error("群 @机器人 应命中")
	}
	if _, _, ok := b.matchCommand(newCtx(b, "c2c", "/admin x")); !ok {
		t.Error("私聊带前缀应命中")
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

// grpEvent 构造一条 GROUP_MESSAGE_CREATE 原始事件（onGroupMessage 的输入）。
func grpEvent(content, mentions string) []byte {
	return []byte(`{"op":0,"t":"GROUP_MESSAGE_CREATE","d":{"id":"MSG1","group_openid":"G1",` +
		`"author":{"id":"U1","username":"用户"},"content":"` + content + `","mentions":[` + mentions + `]}}`)
}

// TestOnGroupMessageIgnoresOtherBot 群全量消息未 @ 本 bot、却 @ 了其他机器人时
// 整条忽略：「@其他机器人 /help」经 cleanContent 剥掉 @ 后会按裸命中规则匹配到
// 本 bot 的命令。@ 人类（「/禁言 @某人」依赖 mentions）与 @ 本 bot 不受影响。
func TestOnGroupMessageIgnoresOtherBot(t *testing.T) {
	b := newTestBot()
	var ran bool
	b.Command("ping", func(c *Ctx) { ran = true })

	call := func(content, mentions string) bool {
		ran = false
		if err := b.onGroupMessage(nil, grpEvent(content, mentions)); err != nil {
			t.Fatalf("onGroupMessage: %v", err)
		}
		return ran
	}

	// @ 其他机器人 + 命令：忽略（本次修复的场景）
	if call("<@B2> /ping", `{"id":"B2","member_openid":"B2","bot":true,"username":"其他机器人"}`) {
		t.Error("@其他机器人的消息不应触发本 bot 命令")
	}
	// 裸命令：照常触发
	if !call("/ping", "") {
		t.Error("裸命令应照常触发")
	}
	// @ 人类成员：照常触发（裸文本命令 @ 目标用户的场景）
	if !call("/ping <@U9>", `{"id":"U9","member_openid":"U9","bot":false,"username":"小明"}`) {
		t.Error("@人类成员的命令不应被忽略")
	}
	// 同时 @ 本 bot 与其他机器人：本 bot 被点名，照常触发
	if !call("<@ME> <@B2> /ping", `{"id":"ME","member_openid":"ME","bot":true,"is_you":true},`+
		`{"id":"B2","member_openid":"B2","bot":true,"username":"其他机器人"}`) {
		t.Error("@本 bot（含另一 bot）的消息应照常触发")
	}
	// 旧事件无 bot 标记：无法判断，保持原行为（触发）
	if !call("<@B2> /ping", `{"id":"B2","member_openid":"B2","username":"未知账号"}`) {
		t.Error("缺 bot 标记的旧事件应保持原有触发行为")
	}
}

// TestExtractMentionsAndMentionsAccessor fromMessage 组装的 Ctx.Mentions()：
// 排除空 id；api 未就绪不排除任何人；已知自身 openid 后排除机器人自身。
func TestExtractMentionsAndMentionsAccessor(t *testing.T) {
	b := New(Config{})
	m := &dto.Message{
		Content: "禁言 <@M1> 10分钟",
		Author:  &dto.User{ID: "U1", Username: "管理员"},
		Mentions: []*dto.User{
			{ID: "M1", Username: "小明"},
			{ID: "", Username: "空id"}, // 跳过
			{ID: "B1", Username: "机器人自己"},
		},
	}
	c := b.fromMessage(context.Background(), "group", m, true)

	// api 未就绪 → selfOpenID 为空 → 不排除任何 mention
	ms := c.Mentions()
	if len(ms) != 2 || ms[0].ID != "M1" || ms[0].Username != "小明" || ms[1].ID != "B1" {
		t.Fatalf("Mentions()=%+v", ms)
	}

	// 自身 openid 已知（模拟 Me() 已缓存）→ 排除 B1
	b.selfMu.Lock()
	b.selfID = "B1"
	b.selfMu.Unlock()
	ms = c.Mentions()
	if len(ms) != 1 || ms[0].ID != "M1" {
		t.Fatalf("排除自身后 Mentions()=%+v", ms)
	}

	// 无 mentions → nil
	c2 := b.fromMessage(context.Background(), "group", &dto.Message{Author: &dto.User{ID: "U1"}}, false)
	if got := c2.Mentions(); len(got) != 0 {
		t.Errorf("无 mentions 应为空，得到 %+v", got)
	}
}

// Owners：未配置返回 nil；配置了则排序返回（遍历主人发通知用）。
func TestOwners(t *testing.T) {
	if got := New(Config{}).Owners(); got != nil {
		t.Errorf("空配置应返回 nil, got %v", got)
	}
	b := New(Config{}, WithOwners("b-owner", "a-owner"))
	if got := b.Owners(); len(got) != 2 || got[0] != "a-owner" || got[1] != "b-owner" {
		t.Errorf("Owners() = %v, want [a-owner b-owner]", got)
	}
}
