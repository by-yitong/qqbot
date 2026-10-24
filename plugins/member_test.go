package plugins

import (
	"context"
	"strings"
	"testing"

	"qqbot/internal/bot"
)

// fakeMemberAPI 记录调用的假实现。
type fakeMemberAPI struct {
	listMembers  []bot.GroupMember
	listErr      error
	getMember    *bot.GroupMember
	getErr       error
	removeIDs    []string
	removeBlack  bool
	removeRes    *bot.RemoveMembersResult
	removeErr    error
	blacklist    []bot.GroupBlacklistUser
	blacklistErr error
	setOp        string
	setIDs       []string
	setFail      []string
	setErr       error
}

func (f *fakeMemberAPI) ListGroupMembers(context.Context, string) ([]bot.GroupMember, error) {
	return f.listMembers, f.listErr
}

func (f *fakeMemberAPI) GetGroupMember(_ context.Context, _, memberOpenID string) (*bot.GroupMember, error) {
	if f.getMember != nil {
		m := *f.getMember
		m.MemberOpenID = memberOpenID
		return &m, nil
	}
	return nil, f.getErr
}

func (f *fakeMemberAPI) RemoveGroupMembers(_ context.Context, _ string, ids []string, black bool) (*bot.RemoveMembersResult, error) {
	f.removeIDs, f.removeBlack = ids, black
	if f.removeRes != nil {
		return f.removeRes, nil
	}
	return &bot.RemoveMembersResult{RemoveMembersResult: "success"}, f.removeErr
}

func (f *fakeMemberAPI) ListGroupBlacklist(context.Context, string) ([]bot.GroupBlacklistUser, error) {
	return f.blacklist, f.blacklistErr
}

func (f *fakeMemberAPI) SetGroupBlacklist(_ context.Context, _, op string, ids []string) ([]string, error) {
	f.setOp, f.setIDs = op, ids
	return f.setFail, f.setErr
}

func newMemberCtx(content string) *bot.Ctx {
	return bot.NewEventCtx(bot.New(bot.Config{}), "group", content, "U9", "主人", "G1", "MSG1", "", true)
}

func TestFormatMemberOverview(t *testing.T) {
	// 空列表
	if got := formatMemberOverview(nil); !strings.Contains(got, "为空") {
		t.Errorf("空列表: %q", got)
	}

	// 构成统计 + 角色 + 昵称缺失兜底 + 机器人计数
	members := []bot.GroupMember{
		{MemberOpenID: "O1", Username: "张三", MemberRole: bot.MemberRoleOwner},
		{MemberOpenID: "A1", Username: "李四", MemberRole: bot.MemberRoleAdmin},
		{MemberOpenID: "B1", Username: "机器人", MemberRole: bot.MemberRoleMember, Bot: true},
		{MemberOpenID: "ABCDEF1234", MemberRole: bot.MemberRoleMember}, // 无昵称 → openid 前 6 位
	}
	got := formatMemberOverview(members)
	for _, want := range []string{
		"本群共 4 人：群主 1、管理员 1、成员 1、机器人 1",
		"群主：张三", "管理员：李四",
		"成员：ABCDEF…", "机器人：机器人",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("概览缺 %q：\n%s", want, got)
		}
	}

	// 普通成员超 30 截断
	var many []bot.GroupMember
	for i := 0; i < 35; i++ {
		many = append(many, bot.GroupMember{MemberOpenID: "M", Username: "成" + string(rune('A'+i%26)), MemberRole: bot.MemberRoleMember})
	}
	got = formatMemberOverview(many)
	if !strings.Contains(got, "其余 5 人从略") {
		t.Errorf("超 30 应截断：\n%s", got)
	}
}

func TestFormatMemberSearch(t *testing.T) {
	members := []bot.GroupMember{
		{MemberOpenID: "AAAA1111BBBB2222CCCC3332", Username: "阳光小助手", MemberRole: bot.MemberRoleMember, JoinedAt: "2025-08-20T09:15:00+08:00"},
		{MemberOpenID: "DDDD4444EEEE5555FFFF6666", Username: "阳光少年", MemberRole: bot.MemberRoleAdmin, JoinedAt: "2025-07-01T10:30:00+08:00"},
		{MemberOpenID: "GGGG7777", Username: "小红", MemberRole: bot.MemberRoleMember},
	}

	// 无命中
	if got := formatMemberSearch(members, "不存在的人"); !strings.Contains(got, "没有找到") {
		t.Errorf("无命中: %q", got)
	}

	// 单命中：走完整详情（含完整 openid 与入群时间格式化）
	got := formatMemberSearch(members, "小红")
	for _, want := range []string{"找到 1 人", "【小红】成员", "入群：未知", "openid：GGGG7777"} {
		if !strings.Contains(got, want) {
			t.Errorf("单命中缺 %q: %q", want, got)
		}
	}

	// 多命中：openid 截前 8 位、角色中文、大小写不敏感（阳光 → 2 人）
	got = formatMemberSearch(members, "阳光")
	for _, want := range []string{"找到 2 人", "阳光小助手（成员，入群 2025-08-20 09:15）AAAA1111…", "阳光少年（管理员，入群 2025-07-01 10:30）DDDD4444…"} {
		if !strings.Contains(got, want) {
			t.Errorf("多命中缺 %q:\n%s", want, got)
		}
	}
}

func TestMemberExec(t *testing.T) {
	p := NewMember(nil)
	fb := &fakeMemberAPI{}
	c := newMemberCtx("成员")

	// 无关键词：概览
	fb.listMembers = []bot.GroupMember{
		{MemberOpenID: "O1", Username: "张三", MemberRole: bot.MemberRoleOwner},
	}
	p.memberList(fb, c, "")
	if fb.listMembers == nil {
		t.Error("memberList 应调 ListGroupMembers")
	}

	// 带 @：逐个拉详情
	fb.getMember = &bot.GroupMember{Username: "小明", MemberRole: bot.MemberRoleAdmin, JoinedAt: "2025-08-20T09:15:00+08:00"}
	p.memberDetail(fb, c, []bot.Mention{{ID: "M1", Username: "小明"}, {ID: "M2"}})

	// 踢：不拉黑 / 拉黑（含部分拉黑失败提示）
	p.kick(fb, c, []bot.Mention{{ID: "M1", Username: "小明"}}, false)
	if len(fb.removeIDs) != 1 || fb.removeIDs[0] != "M1" {
		t.Errorf("kick ids=%v", fb.removeIDs)
	}
	if fb.removeBlack {
		t.Error("默认不拉黑")
	}
	p.kick(fb, c, []bot.Mention{{ID: "M1"}}, true)
	if !fb.removeBlack {
		t.Error("拉黑开关应传 true")
	}

	// 踢：前置校验拦下群主/机器人目标，其余照常移除
	p.kick(fb, c, []bot.Mention{
		{ID: "M1", Username: "小明"},
		{ID: "M2", Username: "群主", Role: bot.MemberRoleOwner},
		{ID: "M3", Username: "机器人", Bot: true},
	}, false)
	if len(fb.removeIDs) != 1 || fb.removeIDs[0] != "M1" {
		t.Errorf("群主/机器人应被本地拦下，kick ids=%v", fb.removeIDs)
	}

	// 全部被拦（只有群主）→ 不调 API
	fb.removeIDs = nil
	p.kick(fb, c, []bot.Mention{{ID: "M2", Username: "群主", Role: bot.MemberRoleOwner}}, false)
	if fb.removeIDs != nil {
		t.Errorf("全部目标被拦不应调 API，ids=%v", fb.removeIDs)
	}

	// 踢超 20 人截断
	many := make([]bot.Mention, 25)
	for i := range many {
		many[i] = bot.Mention{ID: string(rune('A' + i))}
	}
	p.kick(fb, c, many, false)
	if len(fb.removeIDs) != 20 {
		t.Errorf("25 人应截断为 20，实际 %d", len(fb.removeIDs))
	}

	// 黑名单 add / del
	p.blacklistOp(fb, c, bot.BlacklistOpAdd, []string{"M1"})
	if fb.setOp != bot.BlacklistOpAdd || len(fb.setIDs) != 1 {
		t.Errorf("blacklist add: op=%s ids=%v", fb.setOp, fb.setIDs)
	}
	p.blacklistOp(fb, c, bot.BlacklistOpDel, []string{"M1"})
	if fb.setOp != bot.BlacklistOpDel {
		t.Errorf("blacklist del: op=%s", fb.setOp)
	}

	// API 失败路径不 panic（回复失败被忽略：未连接）
	fb.removeErr = context.DeadlineExceeded
	p.kick(fb, c, []bot.Mention{{ID: "M1"}}, false)
	fb.removeErr = nil
	fb.setErr = context.DeadlineExceeded
	p.blacklistOp(fb, c, bot.BlacklistOpAdd, []string{"M1"})
	fb.setErr = nil
	fb.listErr = context.DeadlineExceeded
	p.memberList(fb, c, "")
	p.blacklistList(fb, c)
}

func TestKickBlacklistFlag(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"拉黑"}, true},
		{[]string{"10分钟", "黑名单"}, true},
		{[]string{"ban"}, true},
		{[]string{}, false},
		{[]string{"10分钟"}, false},
		{[]string{"拉黑了吗"}, false}, // 只认完整参数，不子串匹配
	}
	for _, c := range cases {
		if got := kickBlacklistFlag(c.args); got != c.want {
			t.Errorf("kickBlacklistFlag(%v)=%v, want %v", c.args, got, c.want)
		}
	}
}

func TestBlacklistTargets(t *testing.T) {
	// NewEventCtx 不解析命令参数，Args 需显式注入（运行时由命令分发在 bot.go 注入）
	c := newMemberCtx("拉黑 AAAA BBBB")
	c.Args = bot.Args{Args: []string{"AAAA", "BBBB"}}
	got := blacklistTargets(c)
	if len(got) != 2 || got[0] != "AAAA" || got[1] != "BBBB" {
		t.Errorf("blacklistTargets=%v", got)
	}
	var args []string
	for i := 0; i < 25; i++ {
		args = append(args, string(rune('A'+i)))
	}
	c2 := newMemberCtx("拉黑 " + strings.Join(args, " "))
	c2.Args = bot.Args{Args: args}
	if got := blacklistTargets(c2); len(got) != 20 {
		t.Errorf("超 20 应截断，实际 %d", len(got))
	}
}

func TestFormatBlacklist(t *testing.T) {
	if got := formatBlacklist(nil, false); !strings.Contains(got, "为空") {
		t.Errorf("空黑名单: %q", got)
	}
	users := []bot.GroupBlacklistUser{
		{MemberOpenID: "AAAA1111BBBB", Username: "阳光少年", BannedAt: "2025-07-01T10:30:00+08:00"},
	}
	got := formatBlacklist(users, false)
	if !strings.Contains(got, "群黑名单共 1 人") || !strings.Contains(got, "阳光少年 拉黑于 2025-07-01 10:30  AAAA1111BBBB") {
		t.Errorf("黑名单格式:\n%s", got)
	}
	// tagged 版每行带「解除」标签，text 填入完整的 解除拉黑 openid。
	tagged := formatBlacklist(users, true)
	if !strings.Contains(tagged, bot.CmdInputTag("解除拉黑 AAAA1111BBBB", "解除")) {
		t.Errorf("tagged 版应带解除标签:\n%s", tagged)
	}
}

// TestMemberInstall 权限分层：查询类所有人可见；操作类 AdminOnly——群管理员可见，
// 普通成员不可见（角色经 EnrichGroupRoles 从事件 JSON 补齐，走真实解析路径）。
func TestMemberInstall(t *testing.T) {
	b := bot.New(bot.Config{}, bot.WithOwners("OWN1"))
	NewMember(nil).Install(b)
	helpAs := func(role, userID string) string {
		c := bot.NewEventCtx(b, "group", "", userID, "路人", "G1", "M1", "", true)
		b.EnrichGroupRoles(c, []byte(`{"d":{"author":{"member_role":"`+role+`"}}}`))
		return b.HelpFor(c)
	}

	for _, name := range []string{"成员", "黑名单"} {
		if !strings.Contains(helpAs("member", "U2"), name) {
			t.Errorf("查询命令「%s」应对所有人可见", name)
		}
	}
	for _, name := range []string{"踢", "拉黑", "解除拉黑"} {
		if !strings.Contains(helpAs("admin", "U2"), name) {
			t.Errorf("管理员 /help 应含「%s」", name)
		}
		if !strings.Contains(helpAs("", "OWN1"), name) {
			t.Errorf("主人 /help 应含「%s」", name)
		}
		if strings.Contains(helpAs("member", "U2"), name) {
			t.Errorf("操作命令「%s」不应出现在普通成员 /help", name)
		}
	}
}
