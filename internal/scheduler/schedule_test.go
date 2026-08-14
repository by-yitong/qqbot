package scheduler

import (
	"testing"
	"time"
)

// loc 用固定 UTC+8（中国无夏令时，测试确定性强）。
var loc = time.FixedZone("CST", 8*3600)

func at(y int, mo time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, mo, d, h, mi, s, 0, loc)
}

func TestNextDaily(t *testing.T) {
	s := Schedule{Type: ScheduleDaily, At: "08:00"}
	cases := []struct {
		from time.Time
		want time.Time
	}{
		{at(2026, 8, 14, 7, 0, 0), at(2026, 8, 14, 8, 0, 0)},  // 今天还没到
		{at(2026, 8, 14, 8, 0, 0), at(2026, 8, 15, 8, 0, 0)},  // 恰好此刻 → 明天
		{at(2026, 8, 14, 9, 30, 0), at(2026, 8, 15, 8, 0, 0)}, // 已过 → 明天
	}
	for _, c := range cases {
		got, err := s.Next(c.from, loc)
		if err != nil {
			t.Fatalf("Next(%v): %v", c.from, err)
		}
		if !got.Equal(c.want) {
			t.Errorf("Next(%v) = %v, want %v", c.from, got, c.want)
		}
	}
	// 23:59 边界
	s2 := Schedule{Type: ScheduleDaily, At: "23:59"}
	got, _ := s2.Next(at(2026, 8, 14, 23, 59, 30), loc)
	if want := at(2026, 8, 15, 23, 59, 0); !got.Equal(want) {
		t.Errorf("23:59 边界: got %v, want %v", got, want)
	}
}

func TestNextWeekly(t *testing.T) {
	// 工作日 09:30：周五 10:00 → 下周一 09:30（2026-08-14 是周五）
	s := Schedule{Type: ScheduleWeekly, At: "09:30", Days: []int{1, 2, 3, 4, 5}}
	got, err := s.Next(at(2026, 8, 14, 10, 0, 0), loc) // 周五
	if err != nil {
		t.Fatal(err)
	}
	if want := at(2026, 8, 17, 9, 30, 0); !got.Equal(want) { // 周一
		t.Errorf("工作日: got %v, want %v", got, want)
	}
	// 周三 09:30:00 整（不严格晚于）→ 下一个工作日周四
	got, _ = s.Next(at(2026, 8, 12, 9, 30, 0), loc)
	if want := at(2026, 8, 13, 9, 30, 0); !got.Equal(want) {
		t.Errorf("恰好时刻: got %v, want %v", got, want)
	}
	// 周三 09:29 → 当天
	got, _ = s.Next(at(2026, 8, 12, 9, 29, 0), loc)
	if want := at(2026, 8, 12, 9, 30, 0); !got.Equal(want) {
		t.Errorf("当天未到: got %v, want %v", got, want)
	}
	// 仅周日：周六 → 明天周日
	sun := Schedule{Type: ScheduleWeekly, At: "20:00", Days: []int{0}}
	got, _ = sun.Next(at(2026, 8, 15, 12, 0, 0), loc) // 2026-08-15 周六
	if want := at(2026, 8, 16, 20, 0, 0); !got.Equal(want) {
		t.Errorf("周日: got %v, want %v", got, want)
	}
}

func TestNextInterval(t *testing.T) {
	s := Schedule{Type: ScheduleInterval, EveryMins: 30}
	got, err := s.Next(at(2026, 8, 14, 8, 0, 0), loc)
	if err != nil {
		t.Fatal(err)
	}
	if want := at(2026, 8, 14, 8, 30, 0); !got.Equal(want) {
		t.Errorf("interval: got %v, want %v", got, want)
	}
}

func TestNextOnce(t *testing.T) {
	s := Schedule{Type: ScheduleOnce, RunAt: "2026-08-15T09:00:00+08:00"}
	got, err := s.Next(at(2026, 8, 14, 12, 0, 0), loc)
	if err != nil {
		t.Fatal(err)
	}
	if want := at(2026, 8, 15, 9, 0, 0); !got.Equal(want) {
		t.Errorf("once: got %v, want %v", got, want)
	}
	// once 不看 from，过去的时间原样返回（是否拒绝由 Scheduler.Add 判断）
	got, _ = s.Next(at(2026, 8, 16, 0, 0, 0), loc)
	if want := at(2026, 8, 15, 9, 0, 0); !got.Equal(want) {
		t.Errorf("once 过去: got %v, want %v", got, want)
	}
}

func TestValidate(t *testing.T) {
	bad := []Schedule{
		{Type: "daily"},               // 缺 at
		{Type: "daily", At: "24:00"},  // 非法钟点
		{Type: "daily", At: "8点"},     // 非法格式
		{Type: "weekly", At: "08:00"}, // 缺 days
		{Type: "weekly", At: "08:00", Days: []int{7}},
		{Type: "interval", EveryMins: 0}, // 间隔过小
		{Type: "once", RunAt: "明天早上"},    // 非 RFC3339
		{Type: "hourly"},                 // 未知类型
	}
	for _, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("Validate(%+v) 应报错", s)
		}
	}
	good := []Schedule{
		{Type: ScheduleDaily, At: "8:05"}, // 容忍非补零
		{Type: ScheduleWeekly, At: "08:00", Days: []int{1, 5}},
		{Type: ScheduleInterval, EveryMins: 1},
		{Type: ScheduleOnce, RunAt: "2026-08-15T09:00:00Z"},
	}
	for _, s := range good {
		if err := s.Validate(); err != nil {
			t.Errorf("Validate(%+v): %v", s, err)
		}
	}
}

func TestHumanize(t *testing.T) {
	cases := []struct {
		s    Schedule
		want string
	}{
		{Schedule{Type: ScheduleDaily, At: "08:00"}, "每天 08:00"},
		{Schedule{Type: ScheduleWeekly, At: "09:30", Days: []int{1, 2, 3, 4, 5}}, "工作日 09:30"},
		{Schedule{Type: ScheduleWeekly, At: "09:30", Days: []int{6, 0}}, "周末 09:30"},
		{Schedule{Type: ScheduleWeekly, At: "09:30", Days: []int{1, 3, 5}}, "每周一/周三/周五 09:30"},
		{Schedule{Type: ScheduleInterval, EveryMins: 30}, "每30分钟"},
		{Schedule{Type: ScheduleInterval, EveryMins: 90}, "每1小时30分钟"},
		{Schedule{Type: ScheduleInterval, EveryMins: 1440}, "每1天"},
		{Schedule{Type: ScheduleOnce, RunAt: "2026-08-15T20:00:00+08:00"}, "单次 08-15 20:00"},
	}
	for _, c := range cases {
		if got := c.s.Humanize(); got != c.want {
			t.Errorf("Humanize(%+v) = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestParseTarget(t *testing.T) {
	tg, err := ParseTarget("group:ABC123")
	if err != nil || tg.Source != "group" || tg.ID != "ABC123" {
		t.Errorf("ParseTarget: %+v, %v", tg, err)
	}
	if tg.String() != "group:ABC123" {
		t.Errorf("String: %q", tg.String())
	}
	for _, bad := range []string{"", "group", "qq:123", "group:"} {
		if _, err := ParseTarget(bad); err == nil {
			t.Errorf("ParseTarget(%q) 应报错", bad)
		}
	}
}
