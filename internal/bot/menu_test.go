package bot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// menuServer 记录调用并按路由返回的假 QQ API（菜单/面板端点）。
type menuServer struct {
	mu       sync.Mutex
	calls    []string
	putBody  string
	postBody string
}

func (m *menuServer) record(call string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, call)
}

func (m *menuServer) has(call string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.calls {
		if c == call {
			return true
		}
	}
	return false
}

func (m *menuServer) countPrefix(prefix string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func readBody(r *http.Request) string {
	buf := make([]byte, 8192)
	n, _ := r.Body.Read(buf)
	return string(buf[:n])
}

func (m *menuServer) handler(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPut && r.URL.Path == "/v2/menu":
		m.mu.Lock()
		m.putBody = readBody(r)
		m.mu.Unlock()
		m.record("PUT /v2/menu")
		_, _ = w.Write([]byte(`{"version":7}`))
	case r.Method == http.MethodGet && r.URL.Path == "/v2/menu":
		m.record("GET /v2/menu")
		_, _ = w.Write([]byte(`{"menu":{"items":[{"name":"帮助","type":"send_message","send_message":".help"}]},"version":1}`))
	case r.Method == http.MethodGet && r.URL.Path == "/v2/panels":
		m.record("GET /v2/panels " + r.URL.Query().Get("scope"))
		_, _ = w.Write([]byte(`{"records":[` +
			`{"panel_id":"p_auto","scope":"group","target_type":"all","panel":{"remark":"` + AutoPanelRemark + `"}},` +
			`{"panel_id":"p_manual","scope":"group","target_type":"all","panel":{"remark":"我的手面板"}}` +
			`],"next_cursor":"","is_end":true}`))
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v2/panels/"):
		m.record("DELETE " + r.URL.Path)
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPost && r.URL.Path == "/v2/panels":
		m.mu.Lock()
		m.postBody = readBody(r)
		m.mu.Unlock()
		m.record("POST /v2/panels")
		_, _ = w.Write([]byte(`{"panel_id":"p_new"}`))
	default:
		http.NotFound(w, r)
	}
}

func withMenuServer(t *testing.T, fn func(m *menuServer, b *Bot)) {
	t.Helper()
	m := &menuServer{}
	srv := httptest.NewServer(http.HandlerFunc(m.handler))
	t.Cleanup(srv.Close)
	old := qqAPIBase
	qqAPIBase = srv.URL
	t.Cleanup(func() { qqAPIBase = old })
	fn(m, &Bot{tokenSource: &fakeTS{tok: "tok"}, appID: "APP"})
}

func TestApplyMenusLifecycle(t *testing.T) {
	withMenuServer(t, func(m *menuServer, b *Bot) {
		sum, err := b.ApplyMenus(context.Background(), DefaultMenuConfig())
		if err != nil {
			t.Fatalf("ApplyMenus: %v", err)
		}
		if !strings.Contains(sum, "新建 1 个") || !strings.Contains(sum, "删除旧 1 个") {
			t.Fatalf("摘要=%q", sum)
		}

		// 菜单 PUT 的 body 应包 menu 字段且含默认菜单项
		if !strings.Contains(m.putBody, `{"menu":{"items":[`) {
			t.Errorf("PUT body 应为 {menu:{items:[...]}} 包裹: %s", m.putBody)
		}
		if !strings.Contains(m.putBody, `"send_message":"帮助"`) {
			t.Errorf("PUT body 缺「帮助」: %s", m.putBody)
		}

		// 只删了自动面板，手动面板不动
		if !m.has("DELETE /v2/panels/p_auto") {
			t.Error("应删除自动面板 p_auto")
		}
		if m.has("DELETE /v2/panels/p_manual") {
			t.Error("不应删除手动面板 p_manual")
		}

		// 创建的面板带自动标记、scope 正确
		if !strings.Contains(m.postBody, `"scope":"group"`) {
			t.Errorf("POST body scope 错: %s", m.postBody)
		}
		if !strings.Contains(m.postBody, `"remark":"`+AutoPanelRemark+`"`) {
			t.Errorf("POST body 应带自动标记: %s", m.postBody)
		}

		// 再次 apply：又删 1 建 1（模拟重启，不堆积）
		if _, err := b.ApplyMenus(context.Background(), DefaultMenuConfig()); err != nil {
			t.Fatalf("二次 ApplyMenus: %v", err)
		}
		if got := m.countPrefix("POST /v2/panels"); got != 2 {
			t.Errorf("两次 apply 应 POST 2 次，实际 %d", got)
		}
	})
}

func TestGetMenu(t *testing.T) {
	withMenuServer(t, func(m *menuServer, b *Bot) {
		menu, err := b.GetMenu(context.Background())
		if err != nil {
			t.Fatalf("GetMenu: %v", err)
		}
		if len(menu.Items) != 1 || menu.Items[0].SendMessage != ".help" {
			t.Fatalf("menu=%+v", menu)
		}
	})
}

func TestClearMenus(t *testing.T) {
	withMenuServer(t, func(m *menuServer, b *Bot) {
		if err := b.ClearMenus(context.Background()); err != nil {
			t.Fatalf("ClearMenus: %v", err)
		}
		// 菜单置空 PUT + 各场景查询、只删自动面板
		if !strings.Contains(m.putBody, `"items":null`) && !strings.Contains(m.putBody, `"items":[]`) {
			t.Errorf("清空菜单 PUT body 应为空 items: %s", m.putBody)
		}
		if !m.has("GET /v2/panels c2c") || !m.has("GET /v2/panels group") {
			t.Error("ClearMenus 应查询各场景面板")
		}
		if !m.has("DELETE /v2/panels/p_auto") || m.has("DELETE /v2/panels/p_manual") {
			t.Error("ClearMenus 只应删自动面板")
		}
	})
}

func TestMenuNotConnected(t *testing.T) {
	b := &Bot{} // 无 tokenSource
	if _, err := b.SetMenu(context.Background(), Menu{}); err == nil {
		t.Fatal("未连接时 SetMenu 应报错")
	}
}

func TestListPanelsPagination(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		if page == 1 {
			_, _ = w.Write([]byte(`{"records":[{"panel_id":"p1","scope":"group","panel":{"remark":"r"}}],"next_cursor":"c2","is_end":false}`))
			return
		}
		if r.URL.Query().Get("cursor") != "c2" {
			t.Errorf("第二页应带 cursor=c2，实际 %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"records":[{"panel_id":"p2","scope":"group","panel":{}}],"next_cursor":"","is_end":true}`))
	}))
	t.Cleanup(srv.Close)
	old := qqAPIBase
	qqAPIBase = srv.URL
	t.Cleanup(func() { qqAPIBase = old })

	b := &Bot{tokenSource: &fakeTS{tok: "t"}, appID: "A"}
	entries, err := b.ListPanels(context.Background(), "group")
	if err != nil {
		t.Fatalf("ListPanels: %v", err)
	}
	if len(entries) != 2 || entries[0].PanelID != "p1" || entries[1].PanelID != "p2" {
		t.Fatalf("entries=%+v", entries)
	}
}

func TestLoadMenuConfig(t *testing.T) {
	dir := t.TempDir()

	// 不存在 → nil, nil（调用方用内置默认）
	if cfg, err := LoadMenuConfig(filepath.Join(dir, "nope.json")); err != nil || cfg != nil {
		t.Fatalf("不存在文件应返回 nil,nil，实际 cfg=%v err=%v", cfg, err)
	}

	// 合法 JSON → 解析字段
	path := filepath.Join(dir, "menu.json")
	content := `{
		"menu": {"items": [{"name": "帮助", "type": "send_message", "send_message": ".help"}]},
		"panels": [{"scope": "c2c", "target_type": "all", "panel": {"items": [
			{"type": "command", "name": ".status", "desc": "状态"}
		]}}]
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadMenuConfig(path)
	if err != nil {
		t.Fatalf("LoadMenuConfig: %v", err)
	}
	if len(cfg.Menu.Items) != 1 || cfg.Menu.Items[0].SendMessage != ".help" {
		t.Fatalf("menu=%+v", cfg.Menu)
	}
	if len(cfg.Panels) != 1 || cfg.Panels[0].Scope != "c2c" || cfg.Panels[0].Panel.Items[0].Name != ".status" {
		t.Fatalf("panels=%+v", cfg.Panels)
	}

	// 坏 JSON → 报错
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte("{not json"), 0o644)
	if _, err := LoadMenuConfig(bad); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
}

func TestDefaultMenuConfigSanity(t *testing.T) {
	cfg := DefaultMenuConfig()
	if len(cfg.Menu.Items) == 0 || len(cfg.Menu.Items) > 10 {
		t.Fatalf("一级菜单数量应在 1..10，实际 %d", len(cfg.Menu.Items))
	}
	for _, it := range cfg.Menu.Items {
		// send_message 按钮填入的是中文别名（如「状态」），须非空且无命令前缀
		if it.Type == "send_message" && (it.SendMessage == "" || strings.HasPrefix(it.SendMessage, ".")) {
			t.Errorf("菜单项 %q 的 send_message 应为中文别名: %q", it.Name, it.SendMessage)
		}
	}
	for _, p := range cfg.Panels {
		if len(p.Panel.Items) > 20 {
			t.Errorf("面板元素超 20: %d", len(p.Panel.Items))
		}
		for _, item := range p.Panel.Items {
			if item.Type == "command" && (item.Name == "" || strings.HasPrefix(item.Name, ".")) {
				t.Errorf("command 元素 name 应为中文别名: %q", item.Name)
			}
		}
	}
}
