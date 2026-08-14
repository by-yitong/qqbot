package plugins

import (
	"strings"
	"testing"
	"time"

	"qqbot/internal/bot"
)

func TestMentionPrefix(t *testing.T) {
	mk := func(source, uid string) *bot.Ctx {
		return bot.NewEventCtx(nil, source, "hi", uid, "小明", "g9", "m1", "", true)
	}
	// 群：<@openid>
	if got := mentionPrefix(mk("group", "u-openid")); got != "<@u-openid> " {
		t.Errorf("群 mention 错: %q", got)
	}
	// 频道：<@!id>
	if got := mentionPrefix(mk("channel", "123456")); got != "<@!123456> " {
		t.Errorf("频道 mention 错: %q", got)
	}
	// 私聊：不需要
	if got := mentionPrefix(mk("c2c", "u-openid")); got != "" {
		t.Errorf("私聊不应 mention: %q", got)
	}
	// 无 userID：安全返回空
	if got := mentionPrefix(mk("group", "")); got != "" {
		t.Errorf("无 id 不应 mention: %q", got)
	}
}

func TestTruncStr(t *testing.T) {
	if got := truncStr("短", 10); got != "短" {
		t.Errorf("短文本不应截断: %q", got)
	}
	long := strings.Repeat("错", 300)
	got := truncStr(long, 200)
	if len([]rune(got)) != 201 || !strings.HasSuffix(got, "…") {
		t.Errorf("长文本应截到 200+省略号, got %d runes", len([]rune(got)))
	}
}

func TestRenderZhipuUsage(t *testing.T) {
	// 字段语义按 zhipu.go 底部注释的真实响应样例：
	// TIME_LIMIT=MCP 本月额度（usage=总 4000, currentValue=已用 1020=各明细之和），
	// TOKENS_LIMIT=N 小时额度（number=5 → 5小时窗口, percentage=已用%）。
	// +30s 余量：渲染与构造间隔几毫秒，不能让相对时长跨分钟/小时边界（曾抖出 5小时19分后）。
	reset := time.Now().Add(5*time.Hour + 20*time.Minute + 30*time.Second).UnixMilli()
	mcpReset := time.Now().Add(17*24*time.Hour + 3*time.Hour + 30*time.Second).UnixMilli()
	total, used := int64(4000), int64(1020)
	mcp := &Limit{
		Type: "TIME_LIMIT", Number: 1,
		Usage: &total, CurrentValue: &used,
		Percentage: 25, NextResetTime: mcpReset,
		UsageDetails: []UsageDetail{
			{ModelCode: "search-prime", Usage: 414},
			{ModelCode: "web-reader", Usage: 606},
			{ModelCode: "zread", Usage: 0},
			{ModelCode: "unknown-mcp", Usage: 7},
		},
	}
	tok := &Limit{Type: "TOKENS_LIMIT", Number: 5, Percentage: 1, NextResetTime: reset}

	out := renderZhipuUsage("max", mcp, tok)
	for _, want := range []string{
		"max",
		"5小时额度：已用 **1%** 🟢",
		"MCP 本月用量：1020 / 4000（25%）",
		"联网搜索 `search-prime` | 414",
		"网页读取 `web-reader` | 606",
		"`zread` | 0",
		"`unknown-mcp` | 7", // 未知编码原样显示
		"约5小时20分后", "约17天3小时后",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("渲染缺 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<t:") {
		t.Errorf("不应使用 Discord 时间语法:\n%s", out)
	}

	// 只缺 token 段：MCP 段照常
	out = renderZhipuUsage("max", mcp, nil)
	if !strings.Contains(out, "MCP 本月用量") || strings.Contains(out, "小时额度") {
		t.Errorf("nil tokens 应只跳过小时段:\n%s", out)
	}
	// 两段全缺
	out = renderZhipuUsage("", nil, nil)
	if !strings.Contains(out, "未找到额度信息") || !strings.Contains(out, "未知") {
		t.Errorf("全缺应有提示:\n%s", out)
	}
	// 空明细不应渲染空表
	mcp2 := &Limit{Type: "TIME_LIMIT", Percentage: 0, NextResetTime: mcpReset}
	out = renderZhipuUsage("max", mcp2, nil)
	if strings.Contains(out, "| MCP |") {
		t.Errorf("空明细不应渲染表头:\n%s", out)
	}
}

func TestFmtIn(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "即将重置"},
		{40 * time.Minute, "40分钟后"},
		{5*time.Hour + 20*time.Minute, "5小时20分后"},
		{2*24*time.Hour + 3*time.Hour, "2天3小时后"},
	}
	for _, tc := range cases {
		if got := fmtIn(tc.d); got != tc.want {
			t.Errorf("fmtIn(%v)=%q want %q", tc.d, got, tc.want)
		}
	}
}
