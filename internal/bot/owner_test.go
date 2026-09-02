package bot

import (
	"strings"
	"testing"
)

// TestOwnerOnly 验证 OwnerOnly：主人放行、非主人拒绝；未配置主人 = 无人可用（fail-closed）。
func TestOwnerOnly(t *testing.T) {
	b := New(Config{}, WithPrefixes("/"), WithOwners("u1"))
	var ran bool
	b.Command("webhook", func(c *Ctx) { ran = true }).OwnerOnly()
	b.Command("aliascmd", func(c *Ctx) { ran = true }).Alias("推送地址").OwnerOnly()

	// 主人放行
	ran = false
	runCmd(b, newCtx(b, "c2c", "/webhook"))
	if !ran {
		t.Error("主人应能执行 OwnerOnly 命令")
	}
	// 非主人拒绝
	ran = false
	c := newCtx(b, "c2c", "/webhook")
	c.userID = "u2"
	runCmd(b, c)
	if ran {
		t.Error("非主人不应执行 OwnerOnly 命令")
	}
	// 别名同样受限（别名与命令本体是同一个 *Command）
	ran = false
	c = newCtx(b, "group", "/推送地址")
	c.userID = "u2"
	runCmd(b, c)
	if ran {
		t.Error("非主人不应经别名执行 OwnerOnly 命令")
	}

	// 未配置主人：无人可用
	b2 := newTestBot()
	ran = false
	b2.Command("webhook", func(c *Ctx) { ran = true }).OwnerOnly()
	runCmd(b2, newCtx(b2, "c2c", "/webhook"))
	if ran {
		t.Error("未配置主人时 OwnerOnly 命令应对所有人拒绝")
	}
}

// TestIsOwner 验证 Bot.IsOwner / Ctx.IsOwner 的集合判断。
func TestIsOwner(t *testing.T) {
	b := New(Config{}, WithOwners("u1", "u2"))
	if !b.IsOwner("u1") || !b.IsOwner("u2") {
		t.Error("配置的 openid 应是主人")
	}
	if b.IsOwner("u3") || b.IsOwner("") {
		t.Error("未配置的 openid / 空串不应是主人")
	}
	c := newCtx(b, "c2c", "x")
	if !c.IsOwner() { // newCtx 固定 userID=u1
		t.Error("u1 的消息应 IsOwner")
	}
	if newTestBot().IsOwner("u1") {
		t.Error("未配置主人时任何人都不是主人")
	}
}

// TestHelpForHidesOwnerOnly 验证非主人视角的 /help 不含 OwnerOnly 命令，主人视角可见。
func TestHelpForHidesOwnerOnly(t *testing.T) {
	b := New(Config{}, WithPrefixes("/"), WithOwners("u1"))
	b.Command("ping", nop).Desc("在线检测")
	b.Command("webhook", nop).Desc("生成推送地址").OwnerOnly()

	owner := newCtx(b, "c2c", "/help")
	if !strings.Contains(b.HelpFor(owner), "webhook") {
		t.Error("主人视角的 help 应包含 OwnerOnly 命令")
	}
	stranger := newCtx(b, "c2c", "/help")
	stranger.userID = "u2"
	if strings.Contains(b.HelpFor(stranger), "webhook") {
		t.Error("非主人视角的 help 不应包含 OwnerOnly 命令")
	}
	if !strings.Contains(b.HelpFor(stranger), "ping") {
		t.Error("普通命令任何视角都应可见")
	}
	if !strings.Contains(b.Help(), "webhook") {
		t.Error("Help()（不过滤）应包含 OwnerOnly 命令")
	}
}

// TestAutoMenuSkipsOwnerOnly 验证 OwnerOnly 命令不进自动菜单/指令面板（面板对所有用户可见）。
func TestAutoMenuSkipsOwnerOnly(t *testing.T) {
	b := New(Config{}, WithPrefixes("/"), WithOwners("u1"))
	b.Command("ping", nop).Desc("在线检测")
	b.Command("webhook", nop).Desc("生成推送地址").OwnerOnly()

	cfg := b.AutoMenuConfig()
	for _, it := range cfg.Menu.Items {
		if it.SendMessage == "webhook" || strings.Contains(it.Name, "webhook") {
			t.Errorf("菜单不应包含 OwnerOnly 命令: %+v", it)
		}
	}
	for _, p := range cfg.Panels {
		for _, it := range p.Panel.Items {
			if it.Name == "webhook" || strings.Contains(it.Desc, "推送地址") {
				t.Errorf("面板不应包含 OwnerOnly 命令: %+v", it)
			}
		}
	}
	found := false
	for _, it := range cfg.Menu.Items {
		if it.SendMessage == "ping" {
			found = true
		}
	}
	if !found {
		t.Error("普通命令应进自动菜单")
	}
}
