package plugins

import (
	"strings"
	"testing"
	"time"

	"qqbot/internal/agent"
	"qqbot/internal/bot"
)

// mkZhihuCtx 构造一个测试用 Ctx。
func mkZhihuCtx(source, groupID, userID string) *bot.Ctx {
	return bot.NewEventCtx(nil, source, "知乎 xxx", userID, "小明", groupID, "m1", "", true)
}

func TestZhihuChatKey(t *testing.T) {
	if got := zhihuChatKey(mkZhihuCtx("group", "g9", "u1")); got != "g:g9" {
		t.Errorf("群键错: %q", got)
	}
	if got := zhihuChatKey(mkZhihuCtx("c2c", "", "u1")); got != "u:u1" {
		t.Errorf("私聊键错: %q", got)
	}
}

func TestZhihuSessionSaveTrimReset(t *testing.T) {
	p := NewZhihuPlugin("http://localhost:1/v1", "k", "zhida-ai")
	key := "g:g9"

	// 空历史起步
	if h := p.history(key); h != nil {
		t.Errorf("初始应为空历史, got %d 条", len(h))
	}

	// 写入 8 轮（16 条）后裁到 zhihuMaxMsgs 条
	for i := 0; i < 8; i++ {
		p.save(key, agent.NewUser("q"), agent.Message{Role: agent.RoleAssistant, Content: strings.Repeat("a", i+1)})
	}
	h := p.history(key)
	if len(h) != zhihuMaxMsgs {
		t.Fatalf("应裁剪到 %d 条, got %d", zhihuMaxMsgs, len(h))
	}
	// 16 条裁掉最早 4 条（q a a a），窗口应为 q a a a a a a a a a a a（最后是第 8 轮的 a×8）
	if h[0].Role != agent.RoleUser || h[0].Content != "q" {
		t.Errorf("裁剪后首条应为第 5 轮的 user 消息, got %v %q", h[0].Role, h[0].Content)
	}
	if last := h[len(h)-1]; last.Role != agent.RoleAssistant || last.Content != "aaaaaaaa" {
		t.Errorf("裁剪后末条应为第 8 轮的回复, got %v %q", last.Role, last.Content)
	}

	// 重置后清空
	p.reset(key)
	if h := p.history(key); h != nil {
		t.Errorf("重置后应为空, got %d 条", len(h))
	}
}

func TestZhihuSessionIdleExpire(t *testing.T) {
	p := NewZhihuPlugin("http://localhost:1/v1", "k", "zhida-ai")
	key := "g:g9"
	p.save(key, agent.NewUser("q"), agent.Message{Role: agent.RoleAssistant, Content: "a"})

	// 把会话时间拨回过期之前，history 应视为无历史并清除
	p.mu.Lock()
	p.sessions[key].at = time.Now().Add(-zhihuIdle - time.Minute)
	p.mu.Unlock()
	if h := p.history(key); h != nil {
		t.Fatalf("过期会话应返回空历史, got %d 条", len(h))
	}
	p.mu.Lock()
	_, exists := p.sessions[key]
	p.mu.Unlock()
	if exists {
		t.Errorf("过期会话应被清除")
	}
}

func TestZhihuPlain(t *testing.T) {
	in := "看 **这篇** [深入分析GMP](https://zhuanlan.zhihu.com/p/586236582) 就够了"
	want := "看 这篇 深入分析GMP：https://zhuanlan.zhihu.com/p/586236582 就够了"
	if got := zhihuPlain(in); got != want {
		t.Errorf("zhihuPlain=\n%q\nwant\n%q", got, want)
	}
	// 无链接、纯加粗也能去符号
	if got := zhihuPlain("**热榜** 来了"); got != "热榜 来了" {
		t.Errorf("纯加粗处理错: %q", got)
	}
	// 图片语法 / 非 http 链接不动
	if got := zhihuPlain("[第8条](zhihu.com/x)"); !strings.Contains(got, "[第8条](zhihu.com/x)") {
		t.Errorf("非 http 链接不应展开: %q", got)
	}
}
