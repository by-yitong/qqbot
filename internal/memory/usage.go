package memory

import (
	"fmt"
	"strconv"
	"time"
)

// UsageSummary 是一段时间的 LLM 用量聚合（.usage 命令的数据）。
type UsageSummary struct {
	Calls            int64
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CachedTokens     int64
	CacheSupported   bool // 任一记录的网关报告过缓存字段则为 true
	DurationMs       int64
}

// UsageSummary 聚合 since 起（zero=全部记录）的 LLM 用量。
func (s *Store) UsageSummary(since time.Time) (UsageSummary, error) {
	q := s.db.Table("llm_usages")
	if !since.IsZero() {
		q = q.Where("ts >= ?", since)
	}
	var row struct {
		Calls            int64
		PromptTokens     int64
		CompletionTokens int64
		TotalTokens      int64
		CachedTokens     int64
		DurationMs       int64
		CacheSupported   bool
	}
	err := q.Select(`
		COUNT(*) AS calls,
		COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
		COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
		COALESCE(SUM(total_tokens), 0) AS total_tokens,
		COALESCE(SUM(cached_tokens), 0) AS cached_tokens,
		COALESCE(SUM(duration_ms), 0) AS duration_ms,
		COALESCE(MAX(cache_supported), 0) AS cache_supported`).
		Scan(&row).Error
	if err != nil {
		return UsageSummary{}, err
	}
	return UsageSummary{
		Calls:            row.Calls,
		PromptTokens:     row.PromptTokens,
		CompletionTokens: row.CompletionTokens,
		TotalTokens:      row.TotalTokens,
		CachedTokens:     row.CachedTokens,
		CacheSupported:   row.CacheSupported,
		DurationMs:       row.DurationMs,
	}, nil
}

// String 格式化成一行可读文本：调用次数 / 输入输出总量 / 缓存命中率 / 平均输出速率。
func (u UsageSummary) String() string {
	b := "调用 " + strconv.FormatInt(u.Calls, 10) + " 次，输入 " + fmtTok(u.PromptTokens) +
		"，输出 " + fmtTok(u.CompletionTokens) + "，共 " + fmtTok(u.TotalTokens) + " token"
	if u.CacheSupported && u.PromptTokens > 0 {
		b += fmt.Sprintf("；缓存命中 %d%%", u.CachedTokens*100/u.PromptTokens)
	}
	if u.DurationMs > 0 && u.CompletionTokens > 0 {
		b += fmt.Sprintf("；平均输出 %d tok/s", u.CompletionTokens*1000/u.DurationMs)
	}
	return b
}

// fmtTok 大数缩短显示：1234 → 1234；45200 → 45.2k；1200000 → 1.2M。
func fmtTok(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return strconv.FormatInt(n, 10)
	}
}
