package scheduler

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "time/tzdata" // 内嵌时区库：容器/精简系统上也能 LoadLocation
)

// 计划类型。
const (
	ScheduleDaily    = "daily"    // 每天 At 时刻
	ScheduleWeekly   = "weekly"   // 每周 Days 的 At 时刻
	ScheduleInterval = "interval" // 每 EveryMins 分钟
	ScheduleOnce     = "once"     // 单次 RunAt
)

// Schedule 是结构化的触发计划。agent 把用户的自然语言时间转成它（比让 LLM 写 cron
// 可靠），插件也可以直接声明；Humanize 反向转成人话用于展示。
type Schedule struct {
	Type      string `json:"type"`                 // daily | weekly | interval | once
	At        string `json:"at,omitempty"`         // "HH:MM"（daily/weekly）
	Days      []int  `json:"days,omitempty"`       // weekly：0=周日…6=周六
	EveryMins int    `json:"every_mins,omitempty"` // interval：间隔分钟（>=1）
	RunAt     string `json:"run_at,omitempty"`     // once：RFC3339（带时区偏移）
}

// Validate 校验计划字段完整合法。
func (s Schedule) Validate() error {
	switch s.Type {
	case ScheduleDaily:
		_, _, err := parseHHMM(s.At)
		return err
	case ScheduleWeekly:
		if _, _, err := parseHHMM(s.At); err != nil {
			return err
		}
		if len(s.Days) == 0 {
			return fmt.Errorf("weekly 需要 days（0=周日…6=周六，工作日=[1,2,3,4,5]）")
		}
		for _, d := range s.Days {
			if d < 0 || d > 6 {
				return fmt.Errorf("days 取值 0-6（0=周日）: %d", d)
			}
		}
	case ScheduleInterval:
		if s.EveryMins < 1 {
			return fmt.Errorf("interval 的 every_minutes 需 >=1")
		}
		if s.EveryMins > 1440*30 {
			return fmt.Errorf("every_minutes 过大（上限 %d）", 1440*30)
		}
	case ScheduleOnce:
		if _, err := time.Parse(time.RFC3339, s.RunAt); err != nil {
			return fmt.Errorf("once 的 run_at 需为 RFC3339 时间（如 2026-08-15T09:00:00+08:00）: %w", err)
		}
	default:
		return fmt.Errorf("未知计划类型 %q（daily/weekly/interval/once）", s.Type)
	}
	return nil
}

// Next 计算严格晚于 from 的下一次运行时刻。daily/weekly 的钟点在 loc 时区解释；
// once 的 RunAt 自带偏移，解析后原样返回。
func (s Schedule) Next(from time.Time, loc *time.Location) (time.Time, error) {
	if err := s.Validate(); err != nil {
		return time.Time{}, err
	}
	t := from.In(loc)
	switch s.Type {
	case ScheduleDaily:
		h, m, _ := parseHHMM(s.At)
		cand := time.Date(t.Year(), t.Month(), t.Day(), h, m, 0, 0, loc)
		if !cand.After(t) {
			cand = cand.AddDate(0, 0, 1)
		}
		return cand, nil
	case ScheduleWeekly:
		h, m, _ := parseHHMM(s.At)
		days := sortedDays(s.Days)
		for i := 0; i <= 7; i++ {
			day := t.AddDate(0, 0, i)
			if !intIn(days, int(day.Weekday())) {
				continue
			}
			cand := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, loc)
			if cand.After(t) {
				return cand, nil
			}
		}
		return time.Time{}, fmt.Errorf("weekly 无法计算下次运行时间") // 不可达：7 天内必有指定 weekday
	case ScheduleInterval:
		return from.Add(time.Duration(s.EveryMins) * time.Minute), nil
	default: // ScheduleOnce
		return time.Parse(time.RFC3339, s.RunAt)
	}
}

// Humanize 把计划转成人话（/task 列表、创建确认时展示）。
func (s Schedule) Humanize() string {
	switch s.Type {
	case ScheduleDaily:
		return "每天 " + s.At
	case ScheduleWeekly:
		return describeDays(s.Days) + " " + s.At
	case ScheduleInterval:
		return "每" + humanMins(s.EveryMins)
	case ScheduleOnce:
		if t, err := time.Parse(time.RFC3339, s.RunAt); err == nil {
			return "单次 " + t.Format("01-02 15:04")
		}
		return "单次 " + s.RunAt
	}
	return s.Type
}

// describeDays 把 weekly 的 days 转成「工作日 / 周末 / 每周一/三/五」。
func describeDays(days []int) string {
	d := sortedDays(days)
	switch {
	case len(d) == 5 && d[0] == 1 && d[4] == 5:
		return "工作日"
	case len(d) == 2 && d[0] == 0 && d[1] == 6:
		return "周末"
	}
	names := []string{"日", "一", "二", "三", "四", "五", "六"}
	parts := make([]string, len(d))
	for i, x := range d {
		parts[i] = "周" + names[x]
	}
	return "每" + strings.Join(parts, "/")
}

// humanMins 把分钟数转成人话（30分钟 / 1小时 / 1小时30分钟 / 1天）。
func humanMins(mins int) string {
	switch {
	case mins < 60:
		return strconv.Itoa(mins) + "分钟"
	case mins%1440 == 0:
		return strconv.Itoa(mins/1440) + "天"
	case mins%60 == 0:
		return strconv.Itoa(mins/60) + "小时"
	default:
		return strconv.Itoa(mins/60) + "小时" + strconv.Itoa(mins%60) + "分钟"
	}
}

// parseHHMM 解析 "HH:MM"（容忍 "8:05" 这类非补零写法）。
func parseHHMM(s string) (int, int, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("at 需为 HH:MM: %q", s)
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("at 需为 HH:MM（00:00-23:59）: %q", s)
	}
	return h, m, nil
}

func sortedDays(days []int) []int {
	d := append([]int(nil), days...)
	sort.Ints(d)
	return d
}

func intIn(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// LoadLoc 按名加载时区（空名/失败逐级回退 Asia/Shanghai → UTC+8 固定区），
// 保证任何环境都能得到可用时区。
func LoadLoc(name string) *time.Location {
	if name == "" {
		name = "Asia/Shanghai"
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return loc
	}
	return time.FixedZone("CST", 8*3600)
}
