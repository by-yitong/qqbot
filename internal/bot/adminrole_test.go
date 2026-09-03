package bot

import (
	"strings"
	"testing"
)

// TestEnrichGroupRoles 从原始事件 payload 补齐角色：发送者 member_role 与
// mentions 的 member_role/bot（结构与真实 WSS 事件一致，botgo dto 会丢弃这些字段）。
func TestEnrichGroupRoles(t *testing.T) {
	b := newTestBot()
	c := newCtx(b, "group", "/踢 <@M1> <@M2> <@M3>")
	c.mentions = []Mention{{ID: "M1", Username: "小明"}, {ID: "M2", Username: "群主"}, {ID: "M3", Username: "机器人"}}
	raw := []byte(`{"op":0,"t":"GROUP_MESSAGE_CREATE","d":{"author":{"id":"U9","member_role":"admin","username":"管理员"},` +
		`"mentions":[{"id":"M1","member_openid":"M1","member_role":"member","bot":false,"username":"小明"},` +
		`{"id":"M2","member_openid":"M2","member_role":"owner","bot":false,"username":"群主"},` +
		`{"id":"M3","member_openid":"M3","member_role":"member","bot":true,"username":"机器人"}]}}`)

	b.EnrichGroupRoles(c, raw)
	if c.MemberRole() != "admin" || !c.IsGroupAdmin() {
		t.Errorf("发送者角色应为 admin: %q", c.MemberRole())
	}
	ms := c.Mentions()
	if len(ms) != 3 {
		t.Fatalf("mentions=%+v", ms)
	}
	if ms[0].Role != MemberRoleMember || ms[0].Bot {
		t.Errorf("M1: %+v", ms[0])
	}
	if ms[1].Role != MemberRoleOwner || ms[1].Bot {
		t.Errorf("M2: %+v", ms[1])
	}
	if ms[2].Role != MemberRoleMember || !ms[2].Bot {
		t.Errorf("M3: %+v", ms[2])
	}

	// 缺字段/空 payload：不 panic、保持零值
	c2 := newCtx(b, "group", "x")
	b.EnrichGroupRoles(c2, nil)
	b.EnrichGroupRoles(c2, []byte(`{"d":{"author":{"id":"U9"}}}`))
	if c2.MemberRole() != "" || c2.IsGroupAdmin() {
		t.Errorf("无角色事件应保持零值: %q", c2.MemberRole())
	}
}

// TestAdminOnly 验证 AdminOnly：群主/管理员放行、普通成员拒绝、主人无角色也放行、
// 非群聊（无私聊角色）仅主人可用。
func TestAdminOnly(t *testing.T) {
	b := New(Config{}, WithPrefixes("/"), WithOwners("u1"))
	var ran bool
	b.Command("kick", func(c *Ctx) { ran = true }).AdminOnly()

	run := func(source, role, userID string) bool {
		ran = false
		c := newCtx(b, source, "/kick")
		c.userID = userID
		c.memberRole = role
		runCmd(b, c)
		return ran
	}

	if !run("group", "admin", "u2") {
		t.Error("群管理员应能执行 AdminOnly 命令")
	}
	if !run("group", "owner", "u2") {
		t.Error("群主应能执行 AdminOnly 命令")
	}
	if !run("group", "", "u1") {
		t.Error("主人（无角色下发）应能执行 AdminOnly 命令")
	}
	if run("group", "member", "u2") {
		t.Error("普通成员不应执行 AdminOnly 命令")
	}
	if run("group", "", "u2") {
		t.Error("角色未下发的普通用户不应执行（fail-closed）")
	}
	if run("c2c", "admin", "u2") {
		t.Error("私聊不应执行 AdminOnly 命令（无私聊角色，仅主人）")
	}
	if !run("c2c", "", "u1") {
		t.Error("主人私聊应能执行 AdminOnly 命令")
	}
}

// TestHelpForHidesAdminOnly 验证 /help 的管理员视角过滤。
func TestHelpForHidesAdminOnly(t *testing.T) {
	b := New(Config{}, WithPrefixes("/"), WithOwners("u1"))
	b.Command("ping", nop).Desc("在线检测")
	b.Command("kick", nop).Desc("踢人").AdminOnly()

	viewer := func(role, userID string) string {
		c := newCtx(b, "group", "/help")
		c.userID, c.memberRole = userID, role
		return b.HelpFor(c)
	}
	if !strings.Contains(viewer("admin", "u2"), "kick") {
		t.Error("管理员视角的 help 应包含 AdminOnly 命令")
	}
	if !strings.Contains(viewer("owner", "u2"), "kick") {
		t.Error("群主视角的 help 应包含 AdminOnly 命令")
	}
	if !strings.Contains(viewer("", "u1"), "kick") {
		t.Error("主人视角的 help 应包含 AdminOnly 命令")
	}
	if strings.Contains(viewer("member", "u2"), "kick") {
		t.Error("普通成员视角的 help 不应包含 AdminOnly 命令")
	}
	if !strings.Contains(viewer("member", "u2"), "ping") {
		t.Error("普通命令任何视角都应可见")
	}
	if !strings.Contains(b.Help(), "kick") {
		t.Error("Help()（不过滤）应包含 AdminOnly 命令")
	}
}

// TestAutoMenuSkipsAdminOnly 验证 AdminOnly 命令不进自动菜单/指令面板。
func TestAutoMenuSkipsAdminOnly(t *testing.T) {
	b := New(Config{}, WithPrefixes("/"), WithOwners("u1"))
	b.Command("ping", nop).Desc("在线检测")
	b.Command("kick", nop).Desc("踢人").AdminOnly()

	cfg := b.AutoMenuConfig()
	for _, it := range cfg.Menu.Items {
		if it.SendMessage == "kick" {
			t.Errorf("菜单不应包含 AdminOnly 命令: %+v", it)
		}
	}
	for _, p := range cfg.Panels {
		for _, it := range p.Panel.Items {
			if it.Name == "kick" {
				t.Errorf("面板不应包含 AdminOnly 命令: %+v", it)
			}
		}
	}
}
