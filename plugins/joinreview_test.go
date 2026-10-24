package plugins

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"qqbot/internal/agent"
	"qqbot/internal/bot"
)

func TestAllCorrect(t *testing.T) {
	p := NewJoinReview([]string{"七七", "qiqi"}, "", false, nil, nil)

	cases := []struct {
		name   string
		set    []string
		submit []string
		want   bool
	}{
		{"精确命中", nil, []string{"七七"}, true},
		{"忽略大小写", nil, []string{"QIQI"}, true},
		{"忽略首尾空格", nil, []string{"  七七  "}, true},
		{"全角空格trim", nil, []string{"\u3000qiqi\u3000"}, true},
		{"答案错误", nil, []string{"七8"}, false},
		{"多题全对", nil, []string{"七七", "qiqi"}, true},
		{"多题一错", nil, []string{"七七", "错的"}, false},
		{"空提交", nil, nil, false},
		{"空答案集", []string{}, []string{"七七"}, false},
		{"空字符串答案", nil, []string{""}, false},
	}
	for _, c := range cases {
		p2 := p
		if c.set != nil {
			p2 = NewJoinReview(c.set, "", false, nil, nil)
		}
		if got := p2.AllCorrect(c.submit); got != c.want {
			t.Errorf("%s: AllCorrect(%v)=%v, want %v", c.name, c.submit, got, c.want)
		}
	}
}

// TestAllCorrectContains 关键词包含匹配（开放式入群问题场景）。
func TestAllCorrectContains(t *testing.T) {
	// 入群问题「Go中如何声明变量？」→ 关键词 var / :=
	p := NewJoinReview([]string{"var", ":="}, JoinMatchContains, false, nil, nil)

	cases := []struct {
		name   string
		submit []string
		want   bool
	}{
		{"裸关键词", []string{"var"}, true},
		{"var 声明", []string{"var x int"}, true},
		{"中文说法含 var", []string{"用var关键字声明"}, true},
		{"短变量声明", []string{"x := 10"}, true},
		{"大小写不敏感", []string{"VAR"}, true},
		{"无关答案", []string{"用 let 声明"}, false},
		{"空答案", []string{""}, false},
		{"空提交", nil, false},
		{"多题全含关键词", []string{"var x int", "y := 1"}, true},
		{"多题一题不含", []string{"var x int", "用 let"}, false},
	}
	for _, c := range cases {
		if got := p.AllCorrect(c.submit); got != c.want {
			t.Errorf("%s: AllCorrect(%v)=%v, want %v", c.name, c.submit, got, c.want)
		}
	}
}

// fakeApprover 记录审批调用的假 joinApprover。
type fakeApprover struct {
	action   string // 最后一次审批动作："" / approve / decline
	reason   string
	memberID string
	err      error
}

func (f *fakeApprover) ApproveGroupJoinRequest(_ context.Context, _, memberOpenID, _, op, rejectReason string) error {
	f.action, f.reason, f.memberID = op, rejectReason, memberOpenID
	return f.err
}

func TestReviewDecisionMatrix(t *testing.T) {
	// 决策矩阵：官方自动通过 / 群白名单外 / 机器人账号 / 命中风控 / 无答案（被邀请）/
	// 答对放行 / 答错留人工 / 答错自动拒绝。
	cases := []struct {
		name       string
		decline    bool
		groups     []string
		event      bot.GroupJoinRequestEvent
		wantAction string // "" = 不动；approve / decline
	}{
		{"官方自动通过", false, nil,
			bot.GroupJoinRequestEvent{GroupOpenID: "G1", AutoApproved: &bot.JoinAutoApproved{StrategyID: "st"}},
			""},
		{"群白名单外", false, []string{"GX"},
			bot.GroupJoinRequestEvent{GroupOpenID: "G1", JoinRequestID: "JR1", MemberOpenID: "M1"},
			""},
		{"机器人账号", false, nil,
			bot.GroupJoinRequestEvent{GroupOpenID: "G1", JoinRequestID: "JR1", MemberOpenID: "M1", Bot: true,
				VerifyInfo: &bot.JoinRequestVerifyInfo{Method: "verify_message", VerifyMessage: "七七"}},
			""},
		{"命中风控", false, nil,
			bot.GroupJoinRequestEvent{GroupOpenID: "G1", JoinRequestID: "JR1", MemberOpenID: "M1", RiskTips: "warning",
				VerifyInfo: &bot.JoinRequestVerifyInfo{Method: "verify_message", VerifyMessage: "七七"}},
			""},
		{"被邀请无答案", false, nil,
			bot.GroupJoinRequestEvent{GroupOpenID: "G1", JoinRequestID: "JR1", MemberOpenID: "M1", ApplySource: "invited"},
			""},
		{"答对放行", false, nil,
			bot.GroupJoinRequestEvent{GroupOpenID: "G1", JoinRequestID: "JR1", MemberOpenID: "M1",
				VerifyInfo: &bot.JoinRequestVerifyInfo{Method: "verify_message", VerifyMessage: " 七七 "}},
			"approve"},
		{"多题全对放行", false, nil,
			bot.GroupJoinRequestEvent{GroupOpenID: "G1", JoinRequestID: "JR1", MemberOpenID: "M1",
				VerifyInfo: &bot.JoinRequestVerifyInfo{Method: "admin_review_qa", ReviewQAList: []bot.JoinRequestReviewQA{
					{Question: "Q1", Answer: "七七"}, {Question: "Q2", Answer: "qiqi"},
				}}},
			"approve"},
		{"答错留人工", false, nil,
			bot.GroupJoinRequestEvent{GroupOpenID: "G1", JoinRequestID: "JR2", MemberOpenID: "M2",
				VerifyInfo: &bot.JoinRequestVerifyInfo{Method: "verify_message", VerifyMessage: "七8"}},
			""},
		{"答错自动拒绝", true, nil,
			bot.GroupJoinRequestEvent{GroupOpenID: "G1", JoinRequestID: "JR3", MemberOpenID: "M3",
				VerifyInfo: &bot.JoinRequestVerifyInfo{Method: "verify_message", VerifyMessage: "七8"}},
			"decline"},
	}
	for _, c := range cases {
		fb := &fakeApprover{}
		p := NewJoinReview([]string{"七七", "qiqi"}, "", c.decline, c.groups, nil)
		p.review(fb, &c.event)
		if fb.action != c.wantAction {
			t.Errorf("%s: action=%q, want %q", c.name, fb.action, c.wantAction)
		}
		if c.wantAction == "decline" && fb.reason == "" {
			t.Errorf("%s: 拒绝应带理由", c.name)
		}
	}
}

func TestAnswerSummary(t *testing.T) {
	it := bot.GroupJoinRequestItem{
		Username:    "小明",
		ApplySource: "self_apply",
		VerifyInfo:  &bot.JoinRequestVerifyInfo{Method: "verify_message", VerifyMessage: "  " + string(make([]byte, 0)) + "短答案"},
	}
	if got := answerSummary(it); got != "答案：短答案" {
		t.Errorf("answerSummary: %q", got)
	}
	// 超长截断
	long := make([]byte, 50)
	for i := range long {
		long[i] = 'a'
	}
	it.VerifyInfo.VerifyMessage = string(long)
	if got := answerSummary(it); got != "答案："+string(long[:20])+"…" {
		t.Errorf("截断: %q", got)
	}
	// 无答案
	if got := answerSummary(bot.GroupJoinRequestItem{ApplySource: "invited"}); got != "无验证答案" {
		t.Errorf("无答案: %q", got)
	}
}

func TestSourceName(t *testing.T) {
	if sourceName("invited") != "被邀请" || sourceName("self_apply") != "主动申请" || sourceName("") != "来源未知" {
		t.Error("sourceName 映射错误")
	}
}

// fakeCompleter 记录 prompt 并返回预设裁决的假 LLM。
type fakeCompleter struct {
	content string
	err     error
	calls   int
	gotSys  string
	gotUser string
}

func (f *fakeCompleter) Complete(_ context.Context, system string, msgs []agent.Message, _ []map[string]any) (*agent.LLMResult, error) {
	f.calls++
	f.gotSys = system
	if len(msgs) > 0 {
		f.gotUser = msgs[len(msgs)-1].Content
	}
	if f.err != nil {
		return nil, f.err
	}
	return &agent.LLMResult{Content: f.content}, nil
}

func TestParseVerdict(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		pass    bool
		reason  string
		wantErr bool
	}{
		{"裸 JSON", `{"pass":true,"reason":"答对了"}`, true, "答对了", false},
		{"失败裁决", `{"pass":false,"reason":"答非所问"}`, false, "答非所问", false},
		{"代码围栏", "```json\n{\"pass\":true,\"reason\":\"ok\"}\n```", true, "ok", false},
		{"带前后缀", `好的，裁决如下：{"pass":false,"reason":"错"} 请参考。`, false, "错", false},
		{"无 JSON", "我觉得可以通过", false, "", true},
		{"缺 pass 字段", `{"ok":true}`, false, "", true},
		{"空串", "", false, "", true},
	}
	for _, c := range cases {
		pass, reason, err := parseVerdict(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: 应报错", c.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if pass != c.pass || reason != c.reason {
			t.Errorf("%s: (%v,%q) want (%v,%q)", c.name, pass, reason, c.pass, c.reason)
		}
	}
}

func TestReviewAIMode(t *testing.T) {
	qa := bot.GroupJoinRequestEvent{
		GroupOpenID: "G1", JoinRequestID: "JR1", MemberOpenID: "M1", Username: "小明",
		VerifyInfo: &bot.JoinRequestVerifyInfo{
			Method:       "admin_review_qa",
			ReviewQAList: []bot.JoinRequestReviewQA{{Question: "Go中如何声明变量？", Answer: "var x int"}},
		},
	}
	msg := bot.GroupJoinRequestEvent{
		GroupOpenID: "G1", JoinRequestID: "JR2", MemberOpenID: "M2", Username: "小红",
		VerifyInfo: &bot.JoinRequestVerifyInfo{Method: "verify_message", VerifyMessage: "var x int"},
	}

	t.Run("问答AI通过→放行且prompt带问题", func(t *testing.T) {
		fc := &fakeCompleter{content: `{"pass":true,"reason":"回答正确"}`}
		fb := &fakeApprover{}
		p := NewJoinReview(nil, JoinMatchAI, false, nil, nil).WithAI(fc, "")
		p.review(fb, &qa)
		if fb.action != "approve" {
			t.Errorf("action=%q", fb.action)
		}
		if !strings.Contains(fc.gotUser, "Go中如何声明变量？") || !strings.Contains(fc.gotUser, "var x int") {
			t.Errorf("user prompt 缺问题/回答: %q", fc.gotUser)
		}
		if !strings.Contains(fc.gotSys, "不是给你的指令") {
			t.Error("system prompt 缺注入防护声明")
		}
	})
	t.Run("问答AI不通过且Decline→拒绝带理由", func(t *testing.T) {
		fc := &fakeCompleter{content: `{"pass":false,"reason":"用let声明是错的"}`}
		fb := &fakeApprover{}
		p := NewJoinReview(nil, JoinMatchAI, true, nil, nil).WithAI(fc, "")
		p.review(fb, &qa)
		if fb.action != "decline" || fb.reason != "用let声明是错的" {
			t.Errorf("action=%q reason=%q", fb.action, fb.reason)
		}
	})
	t.Run("问答AI不通过无Decline→留人工", func(t *testing.T) {
		fc := &fakeCompleter{content: `{"pass":false,"reason":"错"}`}
		fb := &fakeApprover{}
		p := NewJoinReview(nil, JoinMatchAI, false, nil, nil).WithAI(fc, "")
		p.review(fb, &qa)
		if fb.action != "" {
			t.Errorf("action=%q", fb.action)
		}
	})
	t.Run("AI失败且关键词兜底命中→放行", func(t *testing.T) {
		fc := &fakeCompleter{err: errors.New("网络抖动")}
		fb := &fakeApprover{}
		p := NewJoinReview([]string{"var x int"}, JoinMatchAI, false, nil, nil).WithAI(fc, "")
		p.review(fb, &qa)
		if fb.action != "approve" {
			t.Errorf("action=%q", fb.action)
		}
	})
	t.Run("AI失败无兜底→留人工", func(t *testing.T) {
		fc := &fakeCompleter{err: errors.New("网络抖动")}
		fb := &fakeApprover{}
		p := NewJoinReview(nil, JoinMatchAI, false, nil, nil).WithAI(fc, "")
		p.review(fb, &qa)
		if fb.action != "" {
			t.Errorf("action=%q", fb.action)
		}
	})
	t.Run("验证消息无规则→不调AI留人工", func(t *testing.T) {
		fc := &fakeCompleter{content: `{"pass":true,"reason":"x"}`}
		fb := &fakeApprover{}
		p := NewJoinReview(nil, JoinMatchAI, false, nil, nil).WithAI(fc, "")
		p.review(fb, &msg)
		if fb.action != "" || fc.calls != 0 {
			t.Errorf("action=%q calls=%d", fb.action, fc.calls)
		}
	})
	t.Run("验证消息带规则AI通过→放行", func(t *testing.T) {
		fc := &fakeCompleter{content: `{"pass":true,"reason":"正确"}`}
		fb := &fakeApprover{}
		p := NewJoinReview(nil, JoinMatchAI, false, nil, nil).
			WithAI(fc, "入群问题：Go中如何声明变量？验证消息需正确回答")
		p.review(fb, &msg)
		if fb.action != "approve" {
			t.Errorf("action=%q", fb.action)
		}
		if !strings.Contains(fc.gotUser, "Go中如何声明变量？") || !strings.Contains(fc.gotUser, "var x int") {
			t.Errorf("user prompt: %q", fc.gotUser)
		}
	})
	t.Run("AI模式AllCorrect按exact兜底", func(t *testing.T) {
		p := NewJoinReview([]string{"var"}, JoinMatchAI, false, nil, nil)
		if p.AllCorrect([]string{"var x int"}) {
			t.Error("ai 模式兜底应为精确匹配，var x int 不应命中关键词 var")
		}
		if !p.AllCorrect([]string{"var"}) {
			t.Error("精确命中应通过")
		}
	})
}

// fakeSender 记录提醒卡片发送调用的假 joinSender；err 非空时 md 发送失败（测纯文本兜底）。
type fakeSender struct {
	md   []string
	text []string
	err  error
}

func (f *fakeSender) SendGroupMarkdown(_ context.Context, _, _, md string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.md = append(f.md, md)
	return "card1", nil
}

func (f *fakeSender) SendGroupText(_ context.Context, _, _, text string) (string, error) {
	f.text = append(f.text, text)
	return "card2", nil
}

func (f *fakeSender) AvatarURL(openid string) string {
	return "https://q.qlogo.cn/qqapp/FAKE/" + openid + "/640"
}

// qaEvent 构造一条问答模式的入群申请事件。
func qaEvent() bot.GroupJoinRequestEvent {
	return bot.GroupJoinRequestEvent{
		GroupOpenID: "G1", JoinRequestID: "JR1", MemberOpenID: "M1",
		Username: "小明", ApplySource: "self_apply",
		VerifyInfo: &bot.JoinRequestVerifyInfo{
			Method:       "admin_review_qa",
			ReviewQAList: []bot.JoinRequestReviewQA{{Question: "Go中如何声明变量？", Answer: "用let"}},
		},
	}
}

// TestReviewNotifyCard 提醒卡片推送时机：判定不通过/无法判定留人工时推，
// 通过/自动拒绝不推；头像内嵌在 md 里（不单独发图）；卡片带指令标签与卡片码；
// md 被审核拒发时降级纯文本（带文字命令）。
func TestReviewNotifyCard(t *testing.T) {
	t.Run("AI不通过留人工→推卡片带头像问答理由与标签", func(t *testing.T) {
		fb := &fakeApprover{}
		fs := &fakeSender{}
		p := NewJoinReview(nil, JoinMatchAI, false, nil, nil).WithAI(
			&fakeCompleter{content: `{"pass":false,"reason":"let是JS的关键字"}`}, "")
		p.sender = fs
		e := qaEvent()
		p.review(fb, &e)
		if fb.action != "" {
			t.Errorf("action=%q, 应留人工", fb.action)
		}
		if len(fs.md) != 1 {
			t.Fatalf("应推 1 张卡片，got %d", len(fs.md))
		}
		card := fs.md[0]
		for _, want := range []string{"小明", "Go中如何声明变量？", "用let", "let是JS的关键字"} {
			if !strings.Contains(card, want) {
				t.Errorf("卡片缺 %q:\n%s", want, card)
			}
		}
		// 头像内嵌 md 图片语法（qlogo 直链），不再单独发图
		if !strings.Contains(card, "![avatar](https://q.qlogo.cn/qqapp/FAKE/M1/640)") {
			t.Errorf("卡片应内嵌申请人头像:\n%s", card)
		}
		// 处理引导：指令标签（点击填入「审批 同意/拒绝 <卡片码>」；指令文本经 urlencode）
		if !strings.Contains(card, "qqbot-cmd-input") ||
			!strings.Contains(card, url.QueryEscape("审批 同意 ")) ||
			!strings.Contains(card, url.QueryEscape("审批 拒绝 ")) {
			t.Errorf("卡片应带同意/拒绝指令标签:\n%s", card)
		}
	})
	t.Run("AI通过放行→不推卡片", func(t *testing.T) {
		fb := &fakeApprover{}
		fs := &fakeSender{}
		p := NewJoinReview(nil, JoinMatchAI, false, nil, nil).WithAI(
			&fakeCompleter{content: `{"pass":true,"reason":"对"}`}, "")
		p.sender = fs
		e := qaEvent()
		p.review(fb, &e)
		if fb.action != "approve" || len(fs.md) != 0 {
			t.Errorf("action=%q cards=%d", fb.action, len(fs.md))
		}
	})
	t.Run("自动拒绝→不推卡片", func(t *testing.T) {
		fb := &fakeApprover{}
		fs := &fakeSender{}
		p := NewJoinReview(nil, JoinMatchAI, true, nil, nil).WithAI(
			&fakeCompleter{content: `{"pass":false,"reason":"错"}`}, "")
		p.sender = fs
		e := qaEvent()
		p.review(fb, &e)
		if fb.action != "decline" || len(fs.md) != 0 {
			t.Errorf("action=%q cards=%d", fb.action, len(fs.md))
		}
	})
	t.Run("AI无法判定→推卡片", func(t *testing.T) {
		fs := &fakeSender{}
		p := NewJoinReview(nil, JoinMatchAI, false, nil, nil).WithAI(
			&fakeCompleter{err: errors.New("网络抖动")}, "")
		p.sender = fs
		e := qaEvent()
		p.review(&fakeApprover{}, &e)
		if len(fs.md) != 1 {
			t.Fatalf("应推 1 张卡片，got %d", len(fs.md))
		}
		if !strings.Contains(fs.md[0], "暂无法自动判定") {
			t.Errorf("卡片应注明无法判定:\n%s", fs.md[0])
		}
	})
	t.Run("md发送失败→纯文本兜底", func(t *testing.T) {
		fs := &fakeSender{err: errors.New("语料审核拒绝")}
		p := NewJoinReview(nil, JoinMatchAI, false, nil, nil).WithAI(
			&fakeCompleter{content: `{"pass":false,"reason":"错"}`}, "")
		p.sender = fs
		e := qaEvent()
		p.review(&fakeApprover{}, &e)
		if len(fs.text) != 1 {
			t.Fatalf("应发 1 条纯文本兜底，got %d", len(fs.text))
		}
		if !strings.Contains(fs.text[0], "审批 同意 ") || !strings.Contains(fs.text[0], "审批 拒绝 ") {
			t.Errorf("纯文本应带文字命令引导:\n%s", fs.text[0])
		}
	})
	t.Run("Notify关闭→不推", func(t *testing.T) {
		fs := &fakeSender{}
		p := NewJoinReview(nil, JoinMatchAI, false, nil, nil).WithAI(
			&fakeCompleter{content: `{"pass":false,"reason":"错"}`}, "")
		p.Notify = false
		p.sender = fs
		e := qaEvent()
		p.review(&fakeApprover{}, &e)
		if len(fs.md) != 0 || len(fs.text) != 0 {
			t.Errorf("不应推送 cards=%d texts=%d", len(fs.md), len(fs.text))
		}
	})
}

// TestJoinTokenRegistry 卡片码注册表：登记/查询/移除、过期、按申请作废、容量上限。
func TestJoinTokenRegistry(t *testing.T) {
	p := NewJoinReview(nil, "", false, nil, nil)
	e := qaEvent()

	token := p.remember(&e, "AI理由")
	if len(token) != joinTokenLen {
		t.Fatalf("卡片码长度 %d, want %d", len(token), joinTokenLen)
	}
	pj := p.lookupJoinToken(token)
	if pj == nil || pj.JoinRequestID != "JR1" || pj.Reason != "AI理由" || pj.GroupOpenID != "G1" {
		t.Fatalf("lookup: %+v", pj)
	}

	// 过期后查不到
	p.pending[token].Expires = time.Now().Add(-time.Minute)
	if p.lookupJoinToken(token) != nil {
		t.Error("过期卡片码不应命中")
	}

	// 处理成功按申请作废（同申请的所有卡片码一起失效）
	t2 := p.remember(&e, "")
	t3 := p.remember(&e, "")
	if t2 == t3 {
		t.Error("两次登记不应撞码")
	}
	p.forgetJoinRequest("JR1")
	if p.lookupJoinToken(t2) != nil || p.lookupJoinToken(t3) != nil {
		t.Error("作废后不应命中")
	}

	// 容量上限
	for i := 0; i < joinPendingMax+20; i++ {
		p.remember(&bot.GroupJoinRequestEvent{GroupOpenID: "G", JoinRequestID: fmt.Sprintf("JR%d", i)}, "")
	}
	if len(p.pending) > joinPendingMax {
		t.Errorf("注册表应封顶 %d, got %d", joinPendingMax, len(p.pending))
	}
}

// TestCanReview /审批 权限闸：白名单优先；未配置时群主/管理员/主人可用，
// 普通成员与角色未知一律拒绝（按钮点进来的消息与手打同校验）。
func TestCanReview(t *testing.T) {
	b := bot.New(bot.Config{}, bot.WithOwners("master"))
	mk := func(userID, role string) *bot.Ctx {
		c := bot.NewEventCtx(b, "group", "审批", userID, "", "g1", "m1", "", true)
		if role != "" {
			b.EnrichGroupRoles(c, []byte(`{"d":{"author":{"member_role":"`+role+`"}}}`))
		}
		return c
	}
	p := NewJoinReview(nil, "", false, nil, nil)
	cases := []struct {
		userID, role string
		want         bool
	}{
		{"u1", "member", false},
		{"u1", "admin", true},
		{"u1", "owner", true},
		{"u1", "", false},          // 角色未知 fail-closed
		{"master", "member", true}, // BOT_OWNERS 主人恒可用
	}
	for _, c := range cases {
		if got := p.canReview(mk(c.userID, c.role)); got != c.want {
			t.Errorf("canReview(user=%s role=%s)=%v, want %v", c.userID, c.role, got, c.want)
		}
	}
	// 白名单模式：只有名单内 openid 可用，角色再高也不行
	p2 := NewJoinReview(nil, "", false, nil, []string{"vip"})
	if !p2.canReview(mk("vip", "member")) {
		t.Error("白名单用户应可用")
	}
	if p2.canReview(mk("u1", "owner")) {
		t.Error("白名单外即使群主也不应可用")
	}
}

// TestDeclineReason 拒绝理由优先级：命令尾部参数 > AI 理由 > 默认理由；50 字截断。
func TestDeclineReason(t *testing.T) {
	p := NewJoinReview(nil, "", false, nil, nil)
	mk := func(args ...string) *bot.Ctx {
		c := bot.NewEventCtx(nil, "group", "审批", "u1", "", "g1", "m1", "", true)
		c.Args = bot.Args{Args: args}
		return c
	}
	if got := p.declineReason(mk("拒绝", "1"), "拒绝", "AI说的"); got != "AI说的" {
		t.Errorf("AI理由优先于默认: %q", got)
	}
	if got := p.declineReason(mk("拒绝", "1"), "拒绝", ""); got != p.RejectReason {
		t.Errorf("默认理由兜底: %q", got)
	}
	if got := p.declineReason(mk("拒绝", "1", "手打", "理由"), "拒绝", "AI说的"); got != "手打 理由" {
		t.Errorf("手打理由最优先: %q", got)
	}
	if got := p.declineReason(mk("同意", "1"), "同意", "AI说的"); got != "" {
		t.Errorf("非拒绝动作应返回空: %q", got)
	}
	long := strings.Repeat("长", 60)
	if got := p.declineReason(mk("拒绝", "1"), "拒绝", long); len([]rune(got)) != 50 {
		t.Errorf("应截断到 50 字: %d", len([]rune(got)))
	}
}
