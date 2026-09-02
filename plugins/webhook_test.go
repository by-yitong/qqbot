package plugins

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qqbot/internal/bot"
)

func TestExtractText(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"msg_type":"text","content":{"text":"飞书格式"}}`, "飞书格式"},
		{`{"text":"简写格式"}`, "简写格式"},
		{`纯文本 body`, "纯文本 body"},
		{`{"msg_type":"image","content":{}}`, ""},
		{`  `, ""},
		{`{"msg_type":"text","content":{"text":"测试<at userid='all'>所有人</at>"}}`, "测试"},
		{`{"text":"<at user_id=\"ou_123\">张三</at> 看下"}`, "@张三 看下"},
	}
	for _, c := range cases {
		if got := extractText([]byte(c.in)); got != c.want {
			t.Errorf("extractText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAllowedIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/webhook/g/s", nil)
	req.RemoteAddr = "1.2.3.4:5678"
	if !allowedIP(nil, req) {
		t.Error("空白名单应放行")
	}
	if allowedIP([]string{"10.0.0.1"}, req) {
		t.Error("不在白名单应拒绝")
	}
	if !allowedIP([]string{"1.2.3.4"}, req) {
		t.Error("单 IP 精确匹配应放行")
	}
	if !allowedIP([]string{"1.2.0.0/16"}, req) {
		t.Error("CIDR 匹配应放行")
	}
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.9")
	if !allowedIP([]string{"1.2.3.4"}, req) {
		t.Error("应取 X-Forwarded-For 首个 IP")
	}
}

// TestWebhookInstallOwnerOnly 验证 /webhook 注册为 OwnerOnly：非主人视角的 help
// 与自动菜单/面板里都看不到它（命令本体在 internal/bot 的 owner 测试里覆盖）。
func TestWebhookInstallOwnerOnly(t *testing.T) {
	b := bot.New(bot.Config{}, bot.WithPrefixes("/"), bot.WithOwners("boss"))
	NewWebhook(t.TempDir()+"/webhook.json", "", "").Install(b)

	stranger := bot.NewEventCtx(b, "c2c", "help", "u9", "", "", "m", "", false)
	if help := b.HelpFor(stranger); strings.Contains(help, "webhook") || strings.Contains(help, "推送地址") {
		t.Errorf("非主人视角的 help 不应包含 webhook: %s", help)
	}
	owner := bot.NewEventCtx(b, "c2c", "help", "boss", "", "", "m", "", false)
	if help := b.HelpFor(owner); !strings.Contains(help, "webhook") {
		t.Errorf("主人视角的 help 应包含 webhook: %s", help)
	}
	for _, it := range b.AutoMenuConfig().Menu.Items {
		if it.SendMessage == "webhook" {
			t.Errorf("自动菜单不应包含 OwnerOnly 命令: %+v", it)
		}
	}
}

// TestWebhookGenRouting 验证生成/作废的目标群解析与状态流转：群里触发=本群、
// 私聊带参=参数群、私聊免参=最近记忆、无记忆不动状态；off 作废签名并清记忆。
// 回复与私聊发送在测试里因未连接 api 而是 no-op，这里只验证状态侧。
func TestWebhookGenRouting(t *testing.T) {
	b := bot.New(bot.Config{}, bot.WithPrefixes("/"))
	p := NewWebhook(t.TempDir()+"/webhook.json", "", "")
	p.Install(b)

	run := func(source, groupID, userID string, args ...string) {
		c := bot.NewEventCtx(b, source, "webhook", userID, "", groupID, "m", "", false)
		c.Args = bot.Args{Args: args}
		p.cmd(c)
	}

	// 群里触发生成：g1 有签名，且记为 u1 的最近目标群
	run("group", "g1", "u1")
	p.mu.Lock()
	sign1, last := p.conf.Groups["g1"], p.lastGroup["u1"]
	p.mu.Unlock()
	if len(sign1) != 32 { // 16 字节 hex
		t.Errorf("群里触发应为 g1 生成签名, got %q", sign1)
	}
	if last != "g1" {
		t.Errorf("lastGroup[u1] = %q, want g1", last)
	}

	// 私聊免参：用记忆的 g1 重新生成（签名应变化）
	run("c2c", "", "u1")
	p.mu.Lock()
	sign2 := p.conf.Groups["g1"]
	p.mu.Unlock()
	if sign2 == "" || sign2 == sign1 {
		t.Errorf("私聊免参应基于记忆重新生成 g1 签名: %q -> %q", sign1, sign2)
	}

	// 私聊裸带群 id（私聊兜底路径提示的形式）：为 g2 生成
	run("c2c", "", "u1", "g2")
	p.mu.Lock()
	signG2 := p.conf.Groups["g2"]
	p.mu.Unlock()
	if len(signG2) != 32 {
		t.Errorf("私聊 /webhook g2 应为 g2 生成签名, got %q", signG2)
	}

	// 无记忆的用户私聊免参：无目标群，不产生任何状态
	run("c2c", "", "u9")
	p.mu.Lock()
	_, emptyGroup := p.conf.Groups[""]
	u9last := p.lastGroup["u9"]
	p.mu.Unlock()
	if emptyGroup || u9last != "" {
		t.Errorf("无目标群时不应写状态: groups[\"\"]=%v lastGroup[u9]=%q", emptyGroup, u9last)
	}

	// off g1：作废 g1 签名；记忆指向 g2，不应被误清
	run("group", "g1", "u1", "off")
	p.mu.Lock()
	_, g1Exists := p.conf.Groups["g1"]
	last = p.lastGroup["u1"]
	p.mu.Unlock()
	if g1Exists {
		t.Error("off 应作废 g1 的签名")
	}
	if last != "g2" {
		t.Errorf("off g1 不应清掉指向 g2 的记忆, got %q", last)
	}

	// off g2：作废签名，记忆随之清空
	run("group", "g2", "u1", "off")
	p.mu.Lock()
	_, g2Exists := p.conf.Groups["g2"]
	last = p.lastGroup["u1"]
	p.mu.Unlock()
	if g2Exists {
		t.Error("off 应作废 g2 的签名")
	}
	if last != "" {
		t.Errorf("off g2 后 lastGroup[u1] 应清空, got %q", last)
	}
}
