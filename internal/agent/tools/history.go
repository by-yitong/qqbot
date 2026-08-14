package tools

import (
	"context"
	"fmt"
	"time"

	"qqbot/internal/memory"

	"github.com/tidwall/gjson"
)

// HistorySearch 让 AI 查询本会话的长期聊天记录（SQLite 记忆库）。
// 查询范围经 ctx 注入（memory.WithScope）：群聊只能查本群、私聊只能查该用户——群隔离是硬约束。
type HistorySearch struct{ Store *memory.Store }

// Name 返回工具名。
func (t *HistorySearch) Name() string { return "search_history" }

// Description 返回工具描述。
func (t *HistorySearch) Description() string {
	return "查询本会话的长期聊天记录（群聊=本群全部消息，私聊=与用户的往来）。" +
		"要回忆之前聊过什么、谁说过什么、某话题的讨论时使用；支持关键词、说话人、时间范围过滤。"
}

// Parameters 返回参数 JSON Schema。
func (t *HistorySearch) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "关键词（消息正文的子串）；空=不按内容过滤，只按时间/说话人翻"},
			"user":  map[string]any{"type": "string", "description": "按说话人过滤（昵称，支持子串）"},
			"days":  map[string]any{"type": "number", "description": "查最近多少天（默认 7，上限 90）"},
			"limit": map[string]any{"type": "integer", "description": "返回条数上限（默认 20，上限 50）"},
		},
	}
}

// Guidelines 贡献使用要点。
func (t *HistorySearch) Guidelines() []string {
	return []string{
		"要回忆之前聊过的内容、谁说过什么、某话题的结论，用 search_history 查（可按关键词/说话人/天数过滤）",
	}
}

// Execute 执行检索。
func (t *HistorySearch) Execute(ctx context.Context, arguments string) (string, error) {
	sc, ok := memory.ScopeFrom(ctx)
	if !ok {
		return "当前会话不支持查询历史记录", nil
	}
	days := gjson.Get(arguments, "days").Float()
	if days <= 0 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	q := memory.SearchQuery{
		Query: gjson.Get(arguments, "query").String(),
		User:  gjson.Get(arguments, "user").String(),
		Since: time.Duration(days) * 24 * time.Hour,
		Limit: int(gjson.Get(arguments, "limit").Int()),
	}
	msgs, err := t.Store.Search(sc, q)
	if err != nil {
		return "", fmt.Errorf("查询历史失败: %w", err)
	}
	if len(msgs) == 0 {
		return "没有查到相关记录（可放宽关键词或扩大时间范围再试）", nil
	}
	return fmt.Sprintf("查到 %d 条记录（时间正序）：\n%s", len(msgs), memory.FormatMessages(msgs)), nil
}
