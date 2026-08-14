package memory

import (
	"strings"
	"testing"
	"time"
)

// waitUsage 轮询等用量记录落库（异步批量写，最多等 3s）。
func waitUsage(t *testing.T, s *Store, wantCalls int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sum, err := s.UsageSummary(time.Time{})
		if err != nil {
			t.Fatalf("UsageSummary: %v", err)
		}
		if sum.Calls >= wantCalls {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待 %d 条用量记录超时", wantCalls)
}

func TestRecordUsageAndSummary(t *testing.T) {
	s, err := Open(t.TempDir() + "/mem.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now()
	s.RecordUsage(LLMUsage{Ts: now, Day: now.Format("2006-01-02"), Model: "glm",
		PromptTokens: 1000, CompletionTokens: 100, TotalTokens: 1100,
		CachedTokens: 800, CacheSupported: true, DurationMs: 2000})
	s.RecordUsage(LLMUsage{Ts: now, Day: now.Format("2006-01-02"), Model: "glm",
		PromptTokens: 500, CompletionTokens: 50, TotalTokens: 550, DurationMs: 1000})
	waitUsage(t, s, 2)

	// 今日（本地零点起）应包含全部两条
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	sum, err := s.UsageSummary(today)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Calls != 2 {
		t.Fatalf("calls=%d want 2", sum.Calls)
	}
	if sum.PromptTokens != 1500 || sum.CompletionTokens != 150 || sum.TotalTokens != 1650 {
		t.Fatalf("token 聚合错: %+v", sum)
	}
	if !sum.CacheSupported || sum.CachedTokens != 800 {
		t.Fatalf("缓存聚合错: %+v", sum)
	}
	if sum.DurationMs != 3000 {
		t.Fatalf("duration=%d want 3000", sum.DurationMs)
	}

	// 30 天前起 = 全部；明天起 = 空
	if sum2, _ := s.UsageSummary(now.Add(-720 * time.Hour)); sum2.Calls != 2 {
		t.Fatalf("全部聚合 calls=%d want 2", sum2.Calls)
	}
	if sum3, _ := s.UsageSummary(now.Add(time.Hour)); sum3.Calls != 0 {
		t.Fatalf("未来窗口 calls=%d want 0", sum3.Calls)
	}

	// 展示格式（<1万 显示原数）
	txt := sum.String()
	for _, want := range []string{"2 次", "1500", "150", "1650", "缓存命中 53%", "50 tok/s"} {
		if !strings.Contains(txt, want) {
			t.Errorf("String()=%q 应含 %q", txt, want)
		}
	}
}

func TestUsageSummaryEmpty(t *testing.T) {
	s, err := Open(t.TempDir() + "/mem.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sum, err := s.UsageSummary(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Calls != 0 || sum.CacheSupported {
		t.Fatalf("空库应全零: %+v", sum)
	}
}
