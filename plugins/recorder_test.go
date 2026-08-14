package plugins

import (
	"testing"
	"time"

	"qqbot/internal/bot"
	"qqbot/internal/memory"
)

// waitRecords 轮询等异步落库可见（Since 放宽到 24h，容忍测试机上消息时间与当前的偏差）。
func waitRecords(t *testing.T, s *memory.Store, sc memory.Scope, want int) []memory.Message {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := s.Search(sc, memory.SearchQuery{Limit: 10, Since: 24 * time.Hour})
		if len(got) >= want {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("异步落库超时：等 %d 条", want)
	return nil
}

func TestRecorderRecords(t *testing.T) {
	s, err := memory.Open(t.TempDir() + "/r.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ts1 := time.Now().Add(-2 * time.Minute).Format(time.RFC3339)
	ts2 := time.Now().Add(-1 * time.Minute).Format(time.RFC3339)
	p := NewRecorder(s)
	c1 := bot.NewEventCtx(nil, "group", "群里闲聊", "u1", "小明", "g1", "m1", ts1, false)
	p.record(c1)

	c2 := bot.NewEventCtx(nil, "group", "画只猫", "u2", "小红", "g1", "m2", ts2, true)
	p2 := NewRecorder(s)
	p2.record(c2)

	got := waitRecords(t, s, memory.Scope{Source: "group", GroupID: "g1"}, 2)
	if got[0].Username != "小明" || got[0].Content != "群里闲聊" || got[0].AtBot {
		t.Errorf("第一条字段错: %+v", got[0])
	}
	if !got[1].AtBot || got[1].UserID != "u2" {
		t.Errorf("第二条 @bot 标记/用户错: %+v", got[1])
	}
	if got[0].MsgID != "m1" {
		t.Errorf("MsgID=%q want m1（去重键）", got[0].MsgID)
	}
}

func TestRecorderBadTimestampFallsBack(t *testing.T) {
	s, err := memory.Open(t.TempDir() + "/rt.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := NewRecorder(s)
	// 时间戳非 RFC3339 → 用当前时间兜底，仍能入库
	c := bot.NewEventCtx(nil, "group", "x", "u", "n", "g", "m9", "not-a-time", false)
	p.record(c)
	got := waitRecords(t, s, memory.Scope{Source: "group", GroupID: "g"}, 1)
	if got[0].Ts.IsZero() {
		t.Error("时间戳解析失败应兜底当前时间，不应为零值")
	}
}

func TestNewRecorderNil(t *testing.T) {
	if NewRecorder(nil) != nil {
		t.Error("nil store 应返回 nil 插件（UsePlugin 跳过）")
	}
}
