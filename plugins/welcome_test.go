package plugins

import (
	"strconv"
	"testing"

	"qqbot/internal/bot"
)

func TestWelcomeRender(t *testing.T) {
	p := NewWelcome("欢迎 {name} 加入本群！🎉 {name}", nil)
	if got := p.Render("小明"); got != "欢迎 小明 加入本群！🎉 小明" {
		t.Errorf("Render: %q", got)
	}
	if got := p.Render(""); got != "欢迎  加入本群！🎉 " {
		t.Errorf("空名 Render: %q", got)
	}
}

func TestWelcomeNameCache(t *testing.T) {
	p := NewWelcome("hi {name}", nil)
	p.rememberName(&bot.GroupJoinRequestEvent{MemberOpenID: "M1", Username: "小明"})
	if got := p.nameOf("M1", "新朋友"); got != "小明" {
		t.Errorf("nameOf: %q", got)
	}
	if got := p.nameOf("M2", "新朋友"); got != "新朋友" {
		t.Errorf("fallback: %q", got)
	}
	// 空昵称不记录
	p.rememberName(&bot.GroupJoinRequestEvent{MemberOpenID: "M3", Username: ""})
	if got := p.nameOf("M3", "新朋友"); got != "新朋友" {
		t.Errorf("空昵称应走 fallback: %q", got)
	}
}

func TestWelcomeNameCacheEvict(t *testing.T) {
	p := NewWelcome("hi", nil)
	for i := 0; i < nameCacheCap+10; i++ {
		p.rememberName(&bot.GroupJoinRequestEvent{
			MemberOpenID: string(rune('A'+i%26)) + strconv.Itoa(i), Username: "u" + strconv.Itoa(i),
		})
	}
	if len(p.names) > nameCacheCap {
		t.Errorf("缓存应不超过 %d，实际 %d", nameCacheCap, len(p.names))
	}
}

func TestGroupAllowed(t *testing.T) {
	if !groupAllowed(nil, "G1") {
		t.Error("空白名单应全允许")
	}
	if groupAllowed([]string{"G1"}, "G2") {
		t.Error("不在白名单应拒绝")
	}
	if !groupAllowed([]string{"G1", "G2"}, "G2") {
		t.Error("白名单内应允许")
	}
}
