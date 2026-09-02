package plugins

import (
	"context"
	"strings"
	"testing"
	"time"

	"qqbot/internal/bot"
)

func TestParseMuteDuration(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"10分钟", 10 * time.Minute, false},
		{"10分", 10 * time.Minute, false},
		{"10", 10 * time.Minute, false}, // 裸数字按分钟
		{"1个小时", time.Hour, false},
		{"2小时", 2 * time.Hour, false},
		{"1天", 24 * time.Hour, false},
		{"1日", 24 * time.Hour, false},
		{"1周", 7 * 24 * time.Hour, false},
		{"1个星期", 7 * 24 * time.Hour, false},
		{"30秒", 30 * time.Second, false},
		{"30m", 30 * time.Minute, false},
		{"30M", 30 * time.Minute, false}, // 大小写不敏感
		{"2h", 2 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"1w", 7 * 24 * time.Hour, false},
		{"10min", 10 * time.Minute, false},
		{"30天", 30 * 24 * time.Hour, false}, // 整 30 天放行
		{"", 0, true},
		{"abc", 0, true},
		{"31天", 0, true},     // 超 30 天
		{"31d", 0, true},     // 超 30 天（字母单位）
		{"0分钟", 0, true},     // 非正数
		{"-5分钟", 0, true},    // 负数
		{"1小时30分钟", 0, true}, // 复合时长不支持
		{"禁言1小时", 0, true},   // 前缀不是数字
	}
	for _, c := range cases {
		got, err := parseMuteDuration(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseMuteDuration(%q) 应报错，得到 %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseMuteDuration(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseMuteDuration(%q)=%v, want %v", c.in, got, c.want)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30秒"},
		{10 * time.Minute, "10分钟"},
		{90 * time.Minute, "1小时30分钟"},
		{25 * time.Hour, "1天1小时"},
		{7 * 24 * time.Hour, "7天"},
	}
	for _, c := range cases {
		if got := humanDuration(c.d); got != c.want {
			t.Errorf("humanDuration(%v)=%q, want %q", c.d, got, c.want)
		}
	}
}

// fakeMuteSetter 记录禁言调用的假实现。
type fakeMuteSetter struct {
	group  string
	states []bot.MemberMuteState
	err    error
}

func (f *fakeMuteSetter) SetGroupMemberMutes(_ context.Context, groupOpenID string, states []bot.MemberMuteState) error {
	f.group, f.states = groupOpenID, states
	return f.err
}

func TestMuteExec(t *testing.T) {
	b := bot.New(bot.Config{})
	c := bot.NewEventCtx(b, "group", "禁言 10分钟", "U9", "管理员", "G1", "MSG1", "", true)
	fb := &fakeMuteSetter{}
	p := NewMute(nil)

	// 单人禁言：op=add、目标正确、到期 ≈ now+10min
	p.mute(fb, c, []bot.Mention{{ID: "M1", Username: "小明"}}, 10*time.Minute)
	if fb.group != "G1" || len(fb.states) != 1 {
		t.Fatalf("group=%s states=%+v", fb.group, fb.states)
	}
	st := fb.states[0]
	if st.Op != bot.MuteOpAdd || st.MemberOpenID != "M1" {
		t.Errorf("state=%+v", st)
	}
	exp, err := time.Parse(time.RFC3339, st.MuteExpireAt)
	if err != nil {
		t.Fatalf("MuteExpireAt 非 RFC3339: %q", st.MuteExpireAt)
	}
	if drift := time.Until(exp) - 10*time.Minute; drift < -time.Minute || drift > time.Minute {
		t.Errorf("到期时间偏差过大: %v", drift)
	}

	// 多人批量：一次请求全部带上；超过 10 人截断
	many := make([]bot.Mention, 12)
	for i := range many {
		many[i] = bot.Mention{ID: string(rune('A' + i))}
	}
	p.mute(fb, c, many, 30*time.Minute)
	if len(fb.states) != 10 {
		t.Errorf("12 人应截断为 10，实际 %d", len(fb.states))
	}

	// 解禁：op=del、无到期时间
	p.unmute(fb, c, []bot.Mention{{ID: "M1", Username: "小明"}})
	if fb.group != "G1" || len(fb.states) != 1 || fb.states[0].Op != bot.MuteOpDel || fb.states[0].MuteExpireAt != "" {
		t.Fatalf("unmute states=%+v", fb.states)
	}

	// API 失败路径不 panic（回复失败被忽略：未连接）
	fb.err = context.DeadlineExceeded
	p.mute(fb, c, []bot.Mention{{ID: "M1"}}, time.Minute)
	p.unmute(fb, c, []bot.Mention{{ID: "M1"}})
}

func TestMuteGroupGate(t *testing.T) {
	b := bot.New(bot.Config{})
	c := bot.NewEventCtx(b, "group", "禁言 10分钟", "U2", "路人", "G1", "MSG1", "", true)
	// 群白名单外 → 命令不处理（groupAllowed 复用 welcome.go 的实现）
	if groupAllowed([]string{"GX"}, c.GroupID()) {
		t.Error("群白名单外应不通过")
	}
	if !groupAllowed(nil, c.GroupID()) {
		t.Error("空白名单应不限制")
	}
}

// TestMuteInstallOwnerOnly 验证命令以 OwnerOnly 注册：非主人视角 /help 看不到。
func TestMuteInstallOwnerOnly(t *testing.T) {
	b := bot.New(bot.Config{}, bot.WithOwners("OWN1"))
	NewMute(nil).Install(b)
	owner := bot.NewEventCtx(b, "group", "", "OWN1", "主人", "G1", "M1", "", true)
	stranger := bot.NewEventCtx(b, "group", "", "U2", "路人", "G1", "M1", "", true)
	if !strings.Contains(b.HelpFor(owner), "禁言") {
		t.Error("主人视角 /help 应含「禁言」")
	}
	if strings.Contains(b.HelpFor(stranger), "禁言") {
		t.Error("OwnerOnly 命令不应出现在非主人视角 /help")
	}
}

func TestMentionNames(t *testing.T) {
	if got := mentionNames([]bot.Mention{{ID: "M1", Username: "小明"}, {ID: "M2", Username: "小红"}}); got != "小明、小红" {
		t.Errorf("mentionNames=%q", got)
	}
	// 昵称缺失用 openid 前 6 位
	if got := mentionNames([]bot.Mention{{ID: "ABCDEF1234"}}); got != "ABCDEF…" {
		t.Errorf("mentionNames fallback=%q", got)
	}
}
