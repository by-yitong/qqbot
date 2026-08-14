package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"qqbot/internal/memory"
)

// newHistoryStore 测试用记忆库（临时文件 sqlite）。
func newHistoryStore(t *testing.T) *memory.Store {
	t.Helper()
	s, err := memory.Open(t.TempDir() + "/h.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestHistorySearchTool(t *testing.T) {
	s := newHistoryStore(t)
	now := time.Now()
	s.Record(memory.Message{MsgID: "a", Ts: now.Add(-time.Hour), Source: "group", GroupID: "g1", UserID: "u1", Username: "小明", Content: "周末去露营吗"})
	s.Record(memory.Message{MsgID: "b", Ts: now.Add(-30 * time.Minute), Source: "group", GroupID: "g1", UserID: "u2", Username: "小红", Content: "露营带帐篷"})
	s.Record(memory.Message{MsgID: "c", Ts: now, Source: "group", GroupID: "g2", UserID: "u3", Username: "小刚", Content: "别的群的露营"})
	// 等异步落库可见
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if res, _ := s.Search(memory.Scope{Source: "group", GroupID: "g1"}, memory.SearchQuery{Limit: 10, Since: 24 * time.Hour}); len(res) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	tool := &HistorySearch{Store: s}
	ctx := memory.WithScope(context.Background(), memory.Scope{Source: "group", GroupID: "g1"})

	// 关键词检索 + 群隔离（g2 的不出现）
	out, err := tool.Execute(ctx, `{"query":"露营","days":7}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "小明") || !strings.Contains(out, "露营带帐篷") {
		t.Errorf("应查到 g1 两条: %q", out)
	}
	if strings.Contains(out, "别的群") {
		t.Errorf("不应跨群泄漏 g2 记录: %q", out)
	}
	if !strings.Contains(out, "查到 2 条") {
		t.Errorf("应报告条数: %q", out)
	}

	// 按说话人过滤
	out, _ = tool.Execute(ctx, `{"user":"小红"}`)
	if !strings.Contains(out, "露营带帐篷") || strings.Contains(out, "周末去露营吗") {
		t.Errorf("按说话人过滤失效: %q", out)
	}

	// 无结果
	out, _ = tool.Execute(ctx, `{"query":"不存在的词xyz"}`)
	if !strings.Contains(out, "没有查到") {
		t.Errorf("无结果应提示: %q", out)
	}

	// 未绑定 scope
	if out, _ = tool.Execute(context.Background(), `{}`); !strings.Contains(out, "不支持") {
		t.Errorf("未绑定 scope 应提示不支持: %q", out)
	}
}
