package memory

import (
	"context"
	"strings"
	"testing"
	"time"
)

// newTestStore 开一个临时文件库（WAL 在真文件上才生效），用完关。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/mem.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// drain 记几条并等异步落库（Close 会 flush，但库还要继续用——这里轮询等写入可见）。
func drain(t *testing.T, s *Store, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var n int64
		s.db.Model(&Message{}).Count(&n)
		if int(n) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("异步落库超时：等 %d 条", want)
}

func mkMsg(msgID, group, user, name, content string, ts time.Time) Message {
	return Message{
		MsgID: msgID, Ts: ts, Source: "group", GroupID: group,
		UserID: user, Username: name, Content: content,
	}
}

func TestRecordSearch(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	s.Record(mkMsg("m1", "g1", "u1", "小明", "今天去哪吃饭", now))
	s.Record(mkMsg("m2", "g1", "u2", "小红", "火锅吧，吃饭这事你定", now.Add(time.Minute)))
	s.Record(mkMsg("m3", "g2", "u3", "小刚", "明天爬山去哪吃饭好", now))
	drain(t, s, 3)

	// 关键词 + 群隔离：g1 查「吃饭」应命中 2 条（m1/m2），不含 g2 的
	msgs, err := s.Search(Scope{Source: "group", GroupID: "g1"}, SearchQuery{Query: "吃饭", Since: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("g1 应查到 2 条, got %d", len(msgs))
	}
	// 群 g2 只有自己的
	msgs, _ = s.Search(Scope{Source: "group", GroupID: "g2"}, SearchQuery{Query: "吃饭", Since: time.Hour})
	if len(msgs) != 1 || msgs[0].Content != "明天爬山去哪吃饭好" {
		t.Fatalf("g2 群隔离失效: %+v", msgs)
	}
	// 跨群不可查：g1 查不到 g2 的内容（上面已证）；再证按人过滤
	msgs, _ = s.Search(Scope{Source: "group", GroupID: "g1"}, SearchQuery{User: "小红", Since: time.Hour})
	if len(msgs) != 1 || msgs[0].Username != "小红" {
		t.Fatalf("按说话人过滤失效: %+v", msgs)
	}
}

func TestRecordDedup(t *testing.T) {
	s := newTestStore(t)
	m := mkMsg("dup", "g1", "u1", "小明", "重复消息", time.Now())
	s.Record(m)
	s.Record(m) // QQ 重连重投同一条 → 唯一约束去重
	drain(t, s, 1)
	var n int64
	s.db.Model(&Message{}).Count(&n)
	if n != 1 {
		t.Fatalf("重复 MsgID 应只存 1 条, got %d", n)
	}
}

func TestRecentWindowAndLimit(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	// 5 条新的（2 分钟内）+ 2 条旧的（3 小时前）
	for i := 0; i < 5; i++ {
		s.Record(mkMsg("n"+string(rune('a'+i)), "g1", "u1", "小明", "新消息", now.Add(-time.Duration(i)*time.Minute)))
	}
	s.Record(mkMsg("old1", "g1", "u1", "小明", "旧消息1", now.Add(-3*time.Hour)))
	s.Record(mkMsg("old2", "g1", "u1", "小明", "旧消息2", now.Add(-4*time.Hour)))
	drain(t, s, 7)

	msgs, err := s.Recent(Scope{Source: "group", GroupID: "g1"}, time.Hour, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 5 {
		t.Fatalf("1 小时窗口应取 5 条新消息, got %d", len(msgs))
	}
	// 条数上限：限 3 条
	msgs, _ = s.Recent(Scope{Source: "group", GroupID: "g1"}, time.Hour, 3)
	if len(msgs) != 3 {
		t.Fatalf("limit=3 应取 3 条, got %d", len(msgs))
	}
	// 正序：最早在前
	if !msgs[0].Ts.Before(msgs[2].Ts) {
		t.Error("Recent 应时间正序")
	}
}

func TestRecentAfterIncremental(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	base := now.Add(-10 * time.Minute)
	for i := 0; i < 4; i++ {
		s.Record(mkMsg("a"+string(rune('0'+i)), "g1", "u1", "小明", "增量"+string(rune('0'+i)), base.Add(time.Duration(i)*time.Minute)))
	}
	drain(t, s, 4)

	// since 与第 2 条（a1）同刻：严格大于 → 只取 a2、a3
	msgs, err := s.RecentAfter(Scope{Source: "group", GroupID: "g1"}, base.Add(time.Minute), 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].MsgID != "a2" || msgs[1].MsgID != "a3" {
		t.Fatalf("增量应取 a2,a3, got %+v", msgs)
	}
	// 无增量 → 空
	if m2, _ := s.RecentAfter(Scope{Source: "group", GroupID: "g1"}, msgs[1].Ts, 30); len(m2) != 0 {
		t.Fatalf("无增量应为空, got %+v", m2)
	}
	// 群隔离
	if m3, _ := s.RecentAfter(Scope{Source: "group", GroupID: "g2"}, base, 30); len(m3) != 0 {
		t.Fatalf("跨群应为空, got %+v", m3)
	}
}

func TestC2CScopeByUser(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	s.Record(Message{MsgID: "c1", Ts: now, Source: "c2c", UserID: "u1", Username: "甲", Content: "私聊一"})
	s.Record(Message{MsgID: "c2", Ts: now, Source: "c2c", UserID: "u2", Username: "乙", Content: "私聊二"})
	drain(t, s, 2)

	// u1 只能查到自己的
	msgs, _ := s.Search(Scope{Source: "c2c", UserID: "u1"}, SearchQuery{Since: time.Hour})
	if len(msgs) != 1 || msgs[0].UserID != "u1" {
		t.Fatalf("私聊按用户隔离失效: %+v", msgs)
	}
}

func TestScopeCtx(t *testing.T) {
	sc := Scope{Source: "group", GroupID: "g9"}
	if _, ok := ScopeFrom(context.Background()); ok {
		t.Error("未绑定应 ok=false")
	}
	ctx := WithScope(context.Background(), sc)
	got, ok := ScopeFrom(ctx)
	if !ok || got.GroupID != "g9" {
		t.Fatalf("ScopeFrom 应取回注入的 scope: %+v ok=%v", got, ok)
	}
}

func TestFormatMessages(t *testing.T) {
	msgs := []Message{
		{Ts: time.Date(2026, 8, 14, 12, 0, 0, 0, time.Local), Username: "小明", Content: "第一行\n第二行"},
		{Ts: time.Date(2026, 8, 14, 12, 1, 0, 0, time.Local), UserID: "u2", Content: "hi"},
	}
	out := FormatMessages(msgs)
	if !strings.Contains(out, "[08-14 12:00] 小明: 第一行 第二行") {
		t.Errorf("多行内容应压成一行: %q", out)
	}
	if !strings.Contains(out, "[08-14 12:01] u2: hi") {
		t.Errorf("无昵称应用 openid: %q", out)
	}
}
