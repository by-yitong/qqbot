package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s1, err := NewSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess := s1.Get("U123")
	sess.Lock()
	sess.Append(NewUser("你好"))
	sess.Append(Message{Role: RoleAssistant, Content: "在"})
	if err := sess.save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	sess.Unlock()

	// 新 store 从磁盘加载，验证历史持久化。
	s2, err := NewSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess2 := s2.Get("U123")
	if len(sess2.history) != 2 {
		t.Fatalf("history len=%d", len(sess2.history))
	}
	if sess2.history[0].Content != "你好" || sess2.history[1].Content != "在" {
		t.Errorf("历史内容: %v", sess2.history)
	}
	// 文件名规整为 <id>.jsonl。
	if _, err := os.Stat(filepath.Join(dir, "U123.jsonl")); err != nil {
		t.Errorf("文件不存在: %v", err)
	}
}

func TestSessionPerUserIsolation(t *testing.T) {
	s, _ := NewSessionStore("") // 纯内存
	a, b := s.Get("A"), s.Get("B")
	a.Lock()
	a.Append(NewUser("a-msg"))
	a.Unlock()
	b.Lock()
	b.Append(NewUser("b-msg"))
	b.Unlock()

	if len(a.history) != 1 || len(b.history) != 1 {
		t.Fatalf("用户历史未隔离: a=%d b=%d", len(a.history), len(b.history))
	}
	if a.history[0].Content != "a-msg" || b.history[0].Content != "b-msg" {
		t.Errorf("a=%v b=%v", a.history, b.history)
	}
}

func TestSessionMaxHistory(t *testing.T) {
	s, _ := NewSessionStore("")
	sess := s.Get("X")
	sess.Lock()
	defer sess.Unlock()
	for i := 0; i < SessionMaxHistory+10; i++ {
		sess.Append(NewUser("m"))
	}
	if len(sess.history) != SessionMaxHistory {
		t.Fatalf("len=%d want %d", len(sess.history), SessionMaxHistory)
	}
}

func TestSessionGetSameInstance(t *testing.T) {
	// 同一 userID 多次 Get 应返回同一 *Session（共享锁与历史）。
	s, _ := NewSessionStore("")
	a1, a2 := s.Get("A"), s.Get("A")
	if a1 != a2 {
		t.Fatal("Get 应返回同一实例")
	}
}

func TestTrimLeadingOrphans(t *testing.T) {
	msgs := []Message{
		{Role: RoleTool, ToolCallID: "1", Content: "orphan1"},
		{Role: RoleTool, ToolCallID: "1", Content: "orphan2"},
		{Role: RoleUser, Content: "u"},
		{Role: RoleAssistant, Content: "a"},
	}
	got := trimLeadingOrphans(msgs)
	if len(got) != 2 || got[0].Role != RoleUser {
		t.Fatalf("应丢掉开头两条孤立 tool，got=%v", got)
	}
	// 全是 tool → 清空。
	if got := trimLeadingOrphans([]Message{{Role: RoleTool}, {Role: RoleTool}}); len(got) != 0 {
		t.Fatalf("应清空，got=%v", got)
	}
	// 开头非 tool → 原样。
	in := []Message{{Role: RoleUser, Content: "u"}}
	if got := trimLeadingOrphans(in); len(got) != 1 {
		t.Fatalf("got=%v", got)
	}
}

func TestSessionLoadTrimsOrphanTool(t *testing.T) {
	// 模拟历史文件被外部截断，第一行成了孤立的 tool 消息（无对应 assistant）。
	dir := t.TempDir()
	store, _ := NewSessionStore(dir)
	sess := store.Get("U")
	sess.Lock()
	sess.Append(NewUser("你好"))
	sess.Append(Message{Role: RoleAssistant, Content: "在"})
	if err := sess.save(); err != nil {
		t.Fatal(err)
	}
	sess.Unlock()

	// 在文件最前面插一行孤立 tool。
	path := filepath.Join(dir, "U.jsonl")
	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	orphan, _ := json.Marshal(Message{Role: RoleTool, ToolCallID: "ghost", Name: "x", Content: "孤儿"})
	prefixed := append(orphan, '\n')
	prefixed = append(prefixed, orig...)
	if err := os.WriteFile(path, prefixed, 0o644); err != nil {
		t.Fatal(err)
	}

	// 重新加载：开头那条孤立 tool 应被丢掉。
	store2, _ := NewSessionStore(dir)
	sess2 := store2.Get("U")
	if len(sess2.history) != 2 {
		t.Fatalf("len=%d want 2（孤儿 tool 应被丢掉）: %v", len(sess2.history), sess2.history)
	}
	if sess2.history[0].Role == RoleTool {
		t.Errorf("历史不应以 tool 开头: %v", sess2.history)
	}
}

func TestTrimByAge(t *testing.T) {
	now := time.Now()
	old := now.Add(-25 * time.Hour)
	msgs := []Message{
		{Role: RoleUser, Content: "旧问题", Ts: old},
		{Role: RoleAssistant, Content: "旧回答", Ts: old},
		{Role: RoleUser, Content: "新问题", Ts: now},
		{Role: RoleAssistant, Content: "新回答", Ts: now},
	}
	// cutoff 在新旧之间：只保留新的一轮。
	got := TrimByAge(msgs, now.Add(-time.Hour))
	if len(got) != 2 || got[0].Content != "新问题" {
		t.Fatalf("应只保留新消息，got=%v", got)
	}

	// 全部早于 cutoff → 清空（旧事走 search_history）。
	if got := TrimByAge(msgs, now.Add(time.Hour)); len(got) != 0 {
		t.Fatalf("应清空，got=%v", got)
	}

	// 全部新于 cutoff → 原样。
	if got := TrimByAge(msgs, now.Add(-48*time.Hour)); len(got) != 4 {
		t.Fatalf("应原样保留，got=%v", got)
	}

	// 零值 Ts（升级前存量数据）视为陈旧。
	legacy := []Message{
		{Role: RoleUser, Content: "存量旧消息"},
		{Role: RoleUser, Content: "新消息", Ts: now},
	}
	got = TrimByAge(legacy, now.Add(-time.Minute))
	if len(got) != 1 || got[0].Content != "新消息" {
		t.Fatalf("零值 Ts 应被视为陈旧，got=%v", got)
	}

	// 切点落在 assistant 与其 tool 结果之间（长工具跨过 cutoff）：孤儿 tool 被丢弃，
	// 保留部分从下一条 user 起，不以无主 tool 开头。
	mixed := []Message{
		{Role: RoleUser, Content: "旧问题", Ts: now.Add(-32 * time.Minute)},
		{Role: RoleAssistant, Content: "旧调用", ToolCalls: []ToolCall{{ID: "1", Name: "x"}}, Ts: now.Add(-31 * time.Minute)},
		{Role: RoleTool, ToolCallID: "1", Content: "刚出的结果", Ts: now.Add(-29 * time.Minute)},
		{Role: RoleUser, Content: "新问题", Ts: now.Add(-time.Minute)},
	}
	got = TrimByAge(mixed, now.Add(-30*time.Minute))
	if len(got) != 1 || got[0].Content != "新问题" {
		t.Fatalf("孤儿 tool 不应残留，got=%v", got)
	}
}

func TestSafeFilename(t *testing.T) {
	cases := map[string]string{
		"ABC123_-": "ABC123_-",
		"a/b\\c":   "a_b_c",
		"../etc":   "___etc",
		"":         "anonymous",
	}
	for in, want := range cases {
		if got := safeFilename(in); got != want {
			t.Errorf("safeFilename(%q)=%q want %q", in, got, want)
		}
	}
}
