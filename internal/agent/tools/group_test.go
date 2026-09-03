package tools

import (
	"context"
	"strings"
	"testing"

	"qqbot/internal/bot"
	"qqbot/internal/memory"
)

// fakeGroupAPI 群管接口假实现：可编排目标角色与调用记录。
type fakeGroupAPI struct {
	members    []bot.GroupMember
	target     *bot.GroupMember // GetGroupMember 返回（按请求 id 回填）
	targetErr  error
	removedIDs []string
	removeErr  error
	blacklist  []bot.GroupBlacklistUser
	setOp      string
	setIDs     []string
	setFail    []string
}

func (f *fakeGroupAPI) ListGroupMembers(_ context.Context, groupID string) ([]bot.GroupMember, error) {
	if groupID != "G1" {
		return nil, nil
	}
	return f.members, nil
}

func (f *fakeGroupAPI) GetGroupMember(_ context.Context, _, id string) (*bot.GroupMember, error) {
	if f.targetErr != nil {
		return nil, f.targetErr
	}
	m := *f.target
	m.MemberOpenID = id
	return &m, nil
}

func (f *fakeGroupAPI) RemoveGroupMembers(_ context.Context, _ string, ids []string, _ bool) (*bot.RemoveMembersResult, error) {
	if f.removeErr != nil {
		return nil, f.removeErr
	}
	f.removedIDs = append(f.removedIDs, ids...)
	return &bot.RemoveMembersResult{RemoveMembersResult: "success"}, nil
}

func (f *fakeGroupAPI) ListGroupBlacklist(context.Context, string) ([]bot.GroupBlacklistUser, error) {
	return f.blacklist, nil
}

func (f *fakeGroupAPI) SetGroupBlacklist(_ context.Context, _, op string, ids []string) ([]string, error) {
	f.setOp, f.setIDs = op, ids
	return f.setFail, nil
}

// fakeEvidence 记忆检索假实现：query 命中黑名单则返回空（证据不存在）。
type fakeEvidence struct{ hits []memory.Message }

func (f fakeEvidence) Search(_ memory.Scope, q memory.SearchQuery) ([]memory.Message, error) {
	if !strings.Contains(strings.Join(evidenceTexts(f.hits), "\n"), q.Query) {
		return nil, nil
	}
	return f.hits, nil
}

func evidenceTexts(msgs []memory.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Content)
	}
	return out
}

func groupCtx(role string, owner bool) context.Context {
	return WithGroupActor(context.Background(), GroupActor{
		UserID: "U9", GroupID: "G1", MemberRole: role, IsOwner: owner,
	})
}

func TestGroupMembersTool(t *testing.T) {
	fb := &fakeGroupAPI{members: []bot.GroupMember{
		{MemberOpenID: "M1", Username: "阳光小助手", MemberRole: bot.MemberRoleMember},
		{MemberOpenID: "A1", Username: "管理员甲", MemberRole: bot.MemberRoleAdmin},
	}}
	tool := &GroupMembers{API: fb}

	// 无注入（私聊）→ 拒绝
	if out, _ := tool.Execute(context.Background(), `{}`); !strings.Contains(out, "仅群聊") {
		t.Errorf("无私聊注入应拒绝: %q", out)
	}
	// 全量
	out, _ := tool.Execute(groupCtx("", false), `{}`)
	if !strings.Contains(out, "阳光小助手") || !strings.Contains(out, "管理员甲") || !strings.Contains(out, "M1") {
		t.Errorf("全量列表: %q", out)
	}
	// 昵称过滤（大小写不敏感）
	out, _ = tool.Execute(groupCtx("", false), `{"query":"阳光"}`)
	if !strings.Contains(out, "阳光小助手") || strings.Contains(out, "管理员甲") {
		t.Errorf("关键词过滤: %q", out)
	}
}

func TestKickGroupMemberGating(t *testing.T) {
	normal := &bot.GroupMember{Username: "小明", MemberRole: bot.MemberRoleMember}
	ev := fakeEvidence{hits: []memory.Message{{UserID: "M1", Content: "点击领取色情资源 http://bad.example.com"}}}

	cases := []struct {
		name     string
		ctx      context.Context
		api      *fakeGroupAPI
		mem      EvidenceSearcher
		args     string
		wantKick bool
		wantRefu string // 拒绝时应包含的提示
	}{
		{"管理员直接踢", groupCtx("admin", false), &fakeGroupAPI{target: normal}, nil,
			`{"member_openid":"M1","reason":"诈骗"}`, true, ""},
		{"主人直接踢", groupCtx("", true), &fakeGroupAPI{target: normal}, nil,
			`{"member_openid":"M1","reason":"诈骗"}`, true, ""},
		{"普通成员无证据拒绝", groupCtx("member", false), &fakeGroupAPI{target: normal}, ev,
			`{"member_openid":"M1","reason":"诈骗"}`, false, "证据"},
		{"普通成员证据过短", groupCtx("member", false), &fakeGroupAPI{target: normal}, ev,
			`{"member_openid":"M1","reason":"r","evidence":"你好"}`, false, "证据"},
		{"普通成员证据不实", groupCtx("member", false), &fakeGroupAPI{target: normal}, ev,
			`{"member_openid":"M1","reason":"r","evidence":"我根本没发过的内容片段"}`, false, "核实未通过"},
		{"普通成员证据属实放行", groupCtx("member", false), &fakeGroupAPI{target: normal}, ev,
			`{"member_openid":"M1","reason":"色情引流","evidence":"http://bad.example.com"}`, true, ""},
		{"普通成员但无记忆库", groupCtx("member", false), &fakeGroupAPI{target: normal}, nil,
			`{"member_openid":"M1","reason":"r","evidence":"http://bad.example.com"}`, false, "记忆库"},
		{"目标是群主拒绝", groupCtx("admin", false), &fakeGroupAPI{target: &bot.GroupMember{Username: "群主", MemberRole: bot.MemberRoleOwner}}, nil,
			`{"member_openid":"M2","reason":"r"}`, false, "群主"},
		{"目标是管理员拒绝", groupCtx("admin", false), &fakeGroupAPI{target: &bot.GroupMember{Username: "管", MemberRole: bot.MemberRoleAdmin}}, nil,
			`{"member_openid":"M3","reason":"r"}`, false, "管理员"},
		{"目标是机器人拒绝", groupCtx("admin", false), &fakeGroupAPI{target: &bot.GroupMember{Username: "bot", MemberRole: bot.MemberRoleMember, Bot: true}}, nil,
			`{"member_openid":"M4","reason":"r"}`, false, "机器人"},
		{"私聊无身份拒绝", context.Background(), &fakeGroupAPI{target: normal}, nil,
			`{"member_openid":"M1","reason":"r"}`, false, "仅群聊"},
	}
	for _, c := range cases {
		tool := &KickGroupMember{API: c.api, Mem: c.mem}
		out, err := tool.Execute(c.ctx, c.args)
		if err != nil {
			t.Errorf("%s: 意外错误 %v", c.name, err)
			continue
		}
		kicked := len(c.api.removedIDs) > 0
		if kicked != c.wantKick {
			t.Errorf("%s: kicked=%v want %v，回执: %q", c.name, kicked, c.wantKick, out)
		}
		if !c.wantKick && c.wantRefu != "" && !strings.Contains(out, c.wantRefu) {
			t.Errorf("%s: 回执应含 %q: %q", c.name, c.wantRefu, out)
		}
	}
	// 缺 openid
	tool := &KickGroupMember{API: &fakeGroupAPI{target: normal}, Mem: ev}
	if out, _ := tool.Execute(groupCtx("admin", false), `{"reason":"r"}`); !strings.Contains(out, "member_openid") {
		t.Errorf("缺 openid 应提示: %q", out)
	}
}

func TestGroupBlacklistTool(t *testing.T) {
	fb := &fakeGroupAPI{blacklist: []bot.GroupBlacklistUser{
		{MemberOpenID: "M1", Username: "阳光少年", BannedAt: "2025-07-01T10:30:00+08:00"},
	}}
	tool := &GroupBlacklist{API: fb}

	// list 所有人可用
	out, _ := tool.Execute(groupCtx("member", false), `{"op":"list"}`)
	if !strings.Contains(out, "阳光少年") {
		t.Errorf("list: %q", out)
	}
	// 普通成员 add 拒绝
	if out, _ := tool.Execute(groupCtx("member", false), `{"op":"add","member_openids":["M2"]}`); !strings.Contains(out, "仅群主") {
		t.Errorf("普通成员 add 应拒绝: %q", out)
	}
	// 管理员 add 放行 + 传对 op
	if out, _ := tool.Execute(groupCtx("admin", false), `{"op":"add","member_openids":["M2","M3"]}`); !strings.Contains(out, "已加入") {
		t.Errorf("管理员 add: %q", out)
	}
	if fb.setOp != bot.BlacklistOpAdd || len(fb.setIDs) != 2 {
		t.Errorf("add 调用: op=%s ids=%v", fb.setOp, fb.setIDs)
	}
	// 管理员 del
	tool.Execute(groupCtx("owner", false), `{"op":"del","member_openids":["M1"]}`)
	if fb.setOp != bot.BlacklistOpDel {
		t.Errorf("del op=%s", fb.setOp)
	}
	// 非法 op
	if out, _ := tool.Execute(groupCtx("admin", false), `{"op":"drop"}`); !strings.Contains(out, "未知") {
		t.Errorf("非法 op: %q", out)
	}
}
