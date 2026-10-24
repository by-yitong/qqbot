package tools

// selfdev 工具测试：门禁 fail-closed、读写范围守卫、精确替换唯一性、
// 改动追踪、deploy 检查链（check_only，绝不触发重启分支）。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// newTestDev 建一个临时仓库根（plugins/ + main.go）与对应 DevEnv。
func newTestDev(t *testing.T) *DevEnv {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return NewDevEnv(root)
}

func ownerCtx() context.Context {
	return WithRequester(context.Background(), Requester{UserID: "owner-1", IsOwner: true})
}

func nonOwnerCtx() context.Context {
	return WithRequester(context.Background(), Requester{UserID: "user-1", IsOwner: false})
}

// 各工具对无身份/非主人一律拒绝（fail-closed），主人放行。
func TestSelfdevGate(t *testing.T) {
	dev := newTestDev(t)
	tools := []interface {
		Name() string
		Execute(ctx context.Context, arguments string) (string, error)
	}{
		&PluginList{Dev: dev},
		&PluginRead{Dev: dev},
		&PluginWrite{Dev: dev},
		&PluginEdit{Dev: dev},
		&Deploy{Dev: dev},
	}
	for _, tt := range tools {
		for name, ctx := range map[string]context.Context{
			"无身份": context.Background(),
			"非主人": nonOwnerCtx(),
		} {
			out, err := tt.Execute(ctx, `{"path":"main.go"}`)
			if err != nil {
				t.Fatalf("%s %s：不应返回 error: %v", tt.Name(), name, err)
			}
			if !strings.Contains(out, "主人") {
				t.Fatalf("%s %s：应拒绝并说明仅主人可用，实际: %q", tt.Name(), name, out)
			}
		}
	}
	// 主人可用：plugin_list 在空 plugins/ 上正常返回。
	out, err := (&PluginList{Dev: dev}).Execute(ownerCtx(), "")
	if err != nil || strings.Contains(out, "主人") {
		t.Fatalf("主人应放行，实际 out=%q err=%v", out, err)
	}
}

// 读黑名单：.env*、data/、.git/、mcp.json 等不可读；正常文件带行号分段读。
func TestPluginReadScope(t *testing.T) {
	dev := newTestDev(t)
	rd := &PluginRead{Dev: dev}
	if err := os.WriteFile(filepath.Join(dev.Root, ".env"), []byte("AI_API_KEY=secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dev.Root, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dev.Root, "mcp.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".env", ".env.example", "data/x.json", "data", "mcp.json", ".git/config", "go.sum"} {
		if _, err := rd.Execute(ownerCtx(), `{"path":"`+p+`"}`); err != nil {
			t.Fatalf("%s 应返回拒绝文本而非 error: %v", p, err)
		} else if out, _ := rd.Execute(ownerCtx(), `{"path":"`+p+`"}`); !strings.Contains(out, "拒绝") && !strings.Contains(out, "黑名单") {
			t.Fatalf("%s 应被黑名单拒绝，实际: %q", p, out)
		}
	}

	// 正常读：行号 + 分段。
	body := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(dev.Root, "plugins", "a.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := rd.Execute(ownerCtx(), `{"path":"plugins/a.go"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1| package main") || !strings.Contains(out, "3| func main() {}") {
		t.Fatalf("应带行号，实际: %q", out)
	}

	// 越界与不存在。
	if _, err := rd.Execute(ownerCtx(), `{"path":"../../etc/passwd"}`); err == nil {
		t.Fatal("../ 越界应报错")
	}

	// 目录列表。
	out, err = rd.Execute(ownerCtx(), `{"path":"plugins"}`)
	if err != nil || !strings.Contains(out, "a.go") {
		t.Fatalf("目录应列文件，实际: %q err=%v", out, err)
	}
}

// 写白名单：仅 plugins/*.go 与 main.go；其余拒绝；空内容拒绝；改动被追踪。
func TestPluginWriteScope(t *testing.T) {
	dev := newTestDev(t)
	w := &PluginWrite{Dev: dev}

	for _, p := range []string{"internal/bot/x.go", ".env", "scripts/a.sh", "plugins/x.py", "docs/a.md", "../escape.go", "qqbot"} {
		out, err := w.Execute(ownerCtx(), `{"path":"`+p+`","content":"x"}`)
		if err != nil {
			t.Fatalf("%s 拒绝应返回文本而非 error: %v", p, err)
		}
		if !strings.Contains(out, "白名单") {
			t.Fatalf("%s 应被白名单拒绝，实际: %q", p, out)
		}
	}

	if _, err := w.Execute(ownerCtx(), `{"path":"plugins/b.go","content":"  "}`); err == nil {
		t.Fatal("空内容应报错")
	}

	out, err := w.Execute(ownerCtx(), `{"path":"plugins/b.go","content":"package plugins\n"}`)
	if err != nil || !strings.Contains(out, "已写入") {
		t.Fatalf("plugins/b.go 应写入成功，out=%q err=%v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dev.Root, "plugins", "b.go")); err != nil {
		t.Fatal("文件应落盘")
	}
	if got := dev.changedFiles(); len(got) != 1 || got[0] != "plugins/b.go" {
		t.Fatalf("改动应被追踪，实际: %v", got)
	}

	// main.go 允许写。
	if _, err := w.Execute(ownerCtx(), `{"path":"main.go","content":"package main\n"}`); err != nil {
		t.Fatal("main.go 应允许写")
	}
	dev.clearChanged()
	if got := dev.changedFiles(); len(got) != 0 {
		t.Fatalf("clearChanged 后应为空，实际: %v", got)
	}
}

// 精确替换：0 次/多次拒绝，唯一匹配替换成功。
func TestPluginEditUnique(t *testing.T) {
	dev := newTestDev(t)
	e := &PluginEdit{Dev: dev}
	target := filepath.Join(dev.Root, "main.go")
	body := "package main\n\nfunc main() {\n\tprintln(1)\n}\n"
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := e.Execute(ownerCtx(), `{"path":"main.go","old_string":"不存在"}`); err == nil {
		t.Fatal("0 次匹配应报错")
	}
	dup := "line\nline\n"
	if err := os.WriteFile(filepath.Join(dev.Root, "plugins", "dup.go"), []byte(dup), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(ownerCtx(), `{"path":"plugins/dup.go","old_string":"line"}`); err == nil {
		t.Fatal("多次匹配应报错")
	}

	out, err := e.Execute(ownerCtx(), `{"path":"main.go","old_string":"println(1)","new_string":"println(2)"}`)
	if err != nil || !strings.Contains(out, "已替换") {
		t.Fatalf("唯一替换应成功，out=%q err=%v", out, err)
	}
	data, _ := os.ReadFile(target)
	if !strings.Contains(string(data), "println(2)") {
		t.Fatal("替换结果应落盘")
	}
	if got := dev.changedFiles(); len(got) != 1 || got[0] != "main.go" {
		t.Fatalf("改动应被追踪，实际: %v", got)
	}

	// 白名单外的路径同样拒绝。
	if out, _ := e.Execute(ownerCtx(), `{"path":"internal/x.go","old_string":"a","new_string":"b"}`); !strings.Contains(out, "白名单") {
		t.Fatalf("白名单外应拒绝，实际: %q", out)
	}
}

// deploy check_only：坏代码检查失败（服务不受影响），好代码检查通过；不触发重启。
func TestDeployCheckOnly(t *testing.T) {
	dev := newTestDev(t)
	d := &Deploy{Dev: dev}

	// 非主人直接拒绝。
	if out, err := d.Execute(nonOwnerCtx(), `{"check_only":true}`); err != nil || !strings.Contains(out, "主人") {
		t.Fatalf("非主人应拒绝，out=%q err=%v", out, err)
	}

	// 坏代码：语法错误 → 检查链失败。
	if err := os.WriteFile(filepath.Join(dev.Root, "main.go"), []byte("package main\nfunc main( {\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dev.Root, "go.mod"), []byte("module tmp\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := d.Execute(ownerCtx(), `{"check_only":true}`)
	if err != nil {
		t.Fatalf("检查失败应返回报告而非 error: %v", err)
	}
	if !strings.Contains(out, "❌") || !strings.Contains(out, "未受影响") {
		t.Fatalf("坏代码应报告检查失败，实际: %q", out)
	}

	// 好代码：检查全过，check_only 不重启。
	if err := os.WriteFile(filepath.Join(dev.Root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = d.Execute(ownerCtx(), `{"check_only":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "check_only=true：检查全部通过") {
		t.Fatalf("好代码应检查通过，实际: %q", out)
	}
	if strings.Contains(out, "已触发重启") {
		t.Fatalf("check_only 不应触发重启，实际: %q", out)
	}
}

// 参数 schema 基本健全（JSON 可序列化结构正确）。
func TestSelfdevSchemas(t *testing.T) {
	dev := newTestDev(t)
	for _, tt := range []struct {
		name string
		p    map[string]any
	}{
		{"plugin_read", (&PluginRead{Dev: dev}).Parameters()},
		{"plugin_write", (&PluginWrite{Dev: dev}).Parameters()},
		{"plugin_edit", (&PluginEdit{Dev: dev}).Parameters()},
		{"deploy", (&Deploy{Dev: dev}).Parameters()},
	} {
		props, ok := tt.p["properties"].(map[string]any)
		if !ok || len(props) == 0 {
			t.Fatalf("%s 参数 schema 应有 properties", tt.name)
		}
	}
	// gjson 解析 deploy 参数兼容布尔缺省。
	if gjson.Get(`{}`, "check_only").Bool() {
		t.Fatal("缺省 check_only 应为 false")
	}
}
