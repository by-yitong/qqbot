package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestLoadMCPConfigMissing(t *testing.T) {
	cfgs, err := LoadMCPConfig("/nonexistent/mcp.json")
	if err != nil {
		t.Fatalf("不存在的文件应返回 (nil, nil): %v", err)
	}
	if cfgs != nil {
		t.Errorf("应返回 nil，得到 %v", cfgs)
	}
}

func TestLoadMCPConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	body := `{"mcpServers":{
		"weather":{"command":"npx","args":["-y","w"],"env":{"K":"V"}},
		"remote":{"url":"http://x/mcp"}
	}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgs, err := LoadMCPConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 2 {
		t.Fatalf("len=%d", len(cfgs))
	}
	m := map[string]MCPServerConfig{}
	for _, c := range cfgs {
		m[c.Name] = c
	}
	if m["weather"].Type != "stdio" || m["weather"].Command != "npx" {
		t.Errorf("weather: %+v", m["weather"])
	}
	if len(m["weather"].Args) != 2 || m["weather"].Env["K"] != "V" {
		t.Errorf("weather args/env: %+v", m["weather"])
	}
	if m["remote"].Type != "http" || m["remote"].URL != "http://x/mcp" {
		t.Errorf("remote: %+v", m["remote"])
	}
}

func TestExpandEnvRef(t *testing.T) {
	t.Setenv("AI_API_KEY", "sk-test")
	cases := map[string]string{
		"":                             "",
		"plain":                        "plain",
		"${AI_API_KEY}":                "sk-test",
		"Bearer ${AI_API_KEY}":         "Bearer sk-test",
		"$AI_API_KEY":                  "$AI_API_KEY", // 仅 ${} 形式展开
		"cost$5":                       "cost$5",      // 字面 $ 不动
		"${UNSET_VAR_X}":               "",
		"k=${AI_API_KEY};x=${UNSET_Y}": "k=sk-test;x=", // 未设置的变量展开为空串
	}
	for in, want := range cases {
		if got := expandEnvRef(in); got != want {
			t.Errorf("expandEnvRef(%q)=%q want %q", in, got, want)
		}
	}
}

func TestParseMCPSnippet(t *testing.T) {
	// Claude Desktop 整段（含代码围栏，模拟 LLM 转述）
	snippet := "```json\n{\"mcpServers\":{\"zai\":{\"type\":\"stdio\",\"command\":\"npx\",\"args\":[\"-y\",\"@z_ai/mcp-server\"],\"env\":{\"K\":\"V\"}}}}\n```"
	cfgs, err := ParseMCPSnippet(snippet)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 1 || cfgs[0].Name != "zai" || cfgs[0].Command != "npx" {
		t.Fatalf("mcpServers 形态解析错: %+v", cfgs)
	}
	if len(cfgs[0].Args) != 2 || cfgs[0].Env["K"] != "V" {
		t.Fatalf("args/env 解析错: %+v", cfgs[0])
	}

	// 单个 server 配置（带 name）
	cfgs, err = ParseMCPSnippet(`{"name":"my-server","url":"http://x/mcp"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 1 || cfgs[0].Name != "my-server" || cfgs[0].URL != "http://x/mcp" {
		t.Fatalf("单配置形态解析错: %+v", cfgs)
	}

	// 无名单 server：名字从 args 的包名推导
	cfgs, err = ParseMCPSnippet(`{"type":"stdio","command":"npx","args":["-y","@scope/some-mcp"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfgs[0].Name != "some-mcp" {
		t.Fatalf("无名配置应推导包名: %+v", cfgs[0])
	}

	for _, bad := range []string{"", "not json", "[1,2]", `{"foo":1}`} {
		if _, err := ParseMCPSnippet(bad); err == nil {
			t.Errorf("%q 应报错", bad)
		}
	}
}

func TestDefaultMCPName(t *testing.T) {
	cases := []struct {
		cfg  MCPServerConfig
		want string
	}{
		{MCPServerConfig{Name: "显式名"}, "显式名"},
		{MCPServerConfig{Command: "npx", Args: []string{"-y", "@z_ai/mcp-server"}}, "mcp-server"},
		{MCPServerConfig{Command: "uvx", Args: []string{"mcp-fetch"}}, "mcp-fetch"},
		{MCPServerConfig{Command: "uvx", Args: []string{"--from", "git+https://x/y", "srv"}}, "srv"},
		{MCPServerConfig{Command: "/usr/local/bin/weather"}, "weather"},
		{MCPServerConfig{URL: "http://x"}, "mcp"},
	}
	for _, tc := range cases {
		if got := defaultMCPName(tc.cfg); got != tc.want {
			t.Errorf("defaultMCPName(%+v)=%q want %q", tc.cfg, got, tc.want)
		}
	}
}

func TestSaveRuntimeMCP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_runtime.json")
	if err := SaveRuntimeMCP(path, []MCPServerConfig{
		{Name: "s1", Type: "stdio", Command: "npx", Args: []string{"-y", "p1"}},
		{Name: "s2", Type: "http", URL: "http://x/mcp"},
	}); err != nil {
		t.Fatal(err)
	}
	cfgs, err := LoadMCPConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 2 {
		t.Fatalf("应存 2 个, got %d", len(cfgs))
	}
	// 同名覆盖：s1 换 command，s2 不动
	if err := SaveRuntimeMCP(path, []MCPServerConfig{
		{Name: "s1", Type: "stdio", Command: "node", Args: []string{"s.js"}},
	}); err != nil {
		t.Fatal(err)
	}
	cfgs, err = LoadMCPConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 2 {
		t.Fatalf("同名应覆盖而非追加, got %d", len(cfgs))
	}
	for _, c := range cfgs {
		if c.Name == "s1" && c.Command != "node" {
			t.Errorf("s1 应已更新为 node: %+v", c)
		}
	}
	// 空路径 = 不持久化（非错误）
	if err := SaveRuntimeMCP("", []MCPServerConfig{{Name: "x"}}); err != nil {
		t.Errorf("空路径应 no-op: %v", err)
	}
}

func TestAddMCPFromConfigDedupe(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&ToolFunc{NameStr: "mcp_zai_ui_to_artifact", DescStr: "x"})
	a := &Agent{cfg: Config{Tools: reg}}
	_, err := a.AddMCPFromConfig(context.Background(), MCPServerConfig{Name: "zai", Type: "stdio", Command: "npx"})
	if err == nil || !strings.Contains(err.Error(), "已连接过") {
		t.Fatalf("同名应拒绝重复连接: %v", err)
	}
}

// TestAddMCPFromConfigStdioE2E 真机端到端：实际拉起 npx stdio MCP server。
// 默认跳过；设置 ZAI_E2E=1 和 ZAI_TEST_KEY=<bigmodel key> 后运行：
//
//	ZAI_E2E=1 ZAI_TEST_KEY=xxx go test ./internal/agent -run StdioE2E -v
func TestAddMCPFromConfigStdioE2E(t *testing.T) {
	if os.Getenv("ZAI_E2E") != "1" {
		t.Skip("设置 ZAI_E2E=1 启用真机端到端")
	}
	key := os.Getenv("ZAI_TEST_KEY")
	if key == "" {
		t.Skip("设置 ZAI_TEST_KEY 提供 API key")
	}
	path := filepath.Join(t.TempDir(), "runtime.json")
	reg := NewRegistry()
	a := &Agent{cfg: Config{Tools: reg, RuntimeMCPCfg: path}}
	defer a.Close()
	cfgs, err := ParseMCPSnippet(fmt.Sprintf(`{"mcpServers":{"zai-e2e":{"type":"stdio","command":"npx","args":["-y","@z_ai/mcp-server"],"env":{"Z_AI_API_KEY":%q,"Z_AI_MODE":"ZHIPU"}}}}`, key))
	if err != nil {
		t.Fatal(err)
	}
	n, err := a.AddMCPFromConfig(context.Background(), cfgs[0])
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("应接入至少 1 个工具")
	}
	found := false
	for _, name := range reg.Names() {
		if strings.HasPrefix(name, "mcp_zai-e2e_") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("工具应带 mcp_zai-e2e_ 前缀: %v", reg.Names())
	}
	// 持久化文件应已落盘且可回读
	saved, err := LoadMCPConfig(path)
	if err != nil || len(saved) != 1 || saved[0].Name != "zai-e2e" {
		t.Fatalf("持久化错: %v %+v", err, saved)
	}
}

func TestMCPResultText(t *testing.T) {
	res := &mcp.CallToolResult{Content: []mcp.Content{mcp.NewTextContent("hello")}}
	if got := mcpResultText(res); got != "hello" {
		t.Errorf("got %q want hello", got)
	}

	errRes := &mcp.CallToolResult{
		Content: []mcp.Content{mcp.NewTextContent("boom")},
		IsError: true,
	}
	got := mcpResultText(errRes)
	if !strings.Contains(got, "错误") || !strings.Contains(got, "boom") {
		t.Errorf("错误结果应带前缀: %q", got)
	}

	if got := mcpResultText(nil); got != "" {
		t.Errorf("nil 应返回空串，得到 %q", got)
	}
}

func TestSanitizeToolName(t *testing.T) {
	cases := map[string]string{
		"mcp_my-server_search": "mcp_my-server_search",
		"mcp_s.search":         "mcp_s_search",
		"":                     "mcp_tool",
	}
	for in, want := range cases {
		if got := sanitizeToolName(in); got != want {
			t.Errorf("sanitizeToolName(%q)=%q want %q", in, got, want)
		}
	}
	// 超长截断
	long := strings.Repeat("a", 100)
	if got := sanitizeToolName(long); len(got) != 64 {
		t.Errorf("超长应截到 64，得到 %d", len(got))
	}
}
