package tools

// selfdev 工具：让 AI（仅 bot 主人指令时）开发/修改 bot 自己的 Go 插件并部署上线。
//
// 权限模型：Requester（发起人 openid + 是否主人）由 plugins/agent.go 对所有聊天来源
// （群/私聊/频道）注入；五个工具一律 fail-closed——未注入或非主人直接拒绝（返回说明
// 文本而非 error，让模型把拒绝原因转告用户）。注册侧同样有门禁：setup.go 仅在
// AGENT_ENABLE_SELFDEV=1 且配置了 BOT_OWNERS 时注册。
//
// 文件边界：
//   - 读：仓库内任意路径，黑名单除外（.env*、data/、.git/、mcp.json、menu.json、
//     go.sum、qqbot 二进制——防密钥与运行时状态进 LLM 上下文）；
//   - 写：仅 plugins/**/*.go 与 main.go（新插件要进 main.go 的 UsePlugin 注册清单）。
//
// deploy：gofmt → go vet → go build → go test 全过才异步重启——同步 systemctl restart
// 会杀掉 bot 自身（本工具就跑在 bot 进程里），工具调用将无法返回；改用 setsid 分离
// 进程 sleep 15s 再调 scripts/build-run.sh restart，本工具先同步返回、agent 循环把
// 结果回复给用户后进程才退出。重启完成主人会收到「机器人已上线」私聊通知
// （plugins/online.go 的 OnStart）。
//
// 改动追踪：plugin_write/plugin_edit 记录 AI 写过的文件，deploy 时 git 只提交这些
// （不裹挟仓库里主人自己的未提交改动），提交消息带 selfdev: 前缀便于回滚定位。

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tidwall/gjson"
)

// Requester 是本次 AI Run 的发起人身份（selfdev 工具的权限门禁依据）。
// 由 plugins/agent.go 对所有聊天来源注入；未注入时 selfdev 工具一律拒绝。
type Requester struct {
	UserID  string // 发起人 openid
	IsOwner bool   // 是否 bot 主人（BOT_OWNERS）
}

type requesterKey struct{}

// WithRequester 把发起人身份塞进 ctx（仿 WithGroupActor）。
func WithRequester(ctx context.Context, r Requester) context.Context {
	return context.WithValue(ctx, requesterKey{}, r)
}

// RequesterFrom 取出发起人身份（未注入返回 ok=false）。
func RequesterFrom(ctx context.Context) (Requester, bool) {
	r, ok := ctx.Value(requesterKey{}).(Requester)
	return r, ok
}

// requireOwner 校验发起人是主人；不满足时返回拒绝话术（调用方原样返回给模型，
// 模型会转告用户）。fail-closed：未注入身份 = 拒绝。
func requireOwner(ctx context.Context) (Requester, string, bool) {
	r, ok := RequesterFrom(ctx)
	if !ok {
		return Requester{}, "该能力仅 bot 主人可用，当前会话未携带发起人身份，已拒绝。", false
	}
	if !r.IsOwner {
		return r, "修改 bot 自身源码是高危操作，仅 bot 主人（BOT_OWNERS 配置的身份）可用，当前发起人不是主人，已拒绝。", false
	}
	return r, "", true
}

// DevEnv 是 selfdev 工具组的共享环境：仓库根 + 改动文件追踪。
type DevEnv struct {
	Root string // 仓库根（systemd WorkingDirectory=仓库根，传 "." 即可）

	mu      sync.Mutex
	changed map[string]bool // 本次运行以来 AI 写过的仓库相对路径
}

// NewDevEnv 创建 selfdev 共享环境。
func NewDevEnv(root string) *DevEnv {
	return &DevEnv{Root: root, changed: map[string]bool{}}
}

// track 记录一个 AI 改过的文件。
func (d *DevEnv) track(rel string) {
	d.mu.Lock()
	d.changed[rel] = true
	d.mu.Unlock()
}

// changedFiles 返回改动清单（有序，不清空）。
func (d *DevEnv) changedFiles() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.changed))
	for p := range d.changed {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// clearChanged 清空改动清单（git 提交成功后调用）。
func (d *DevEnv) clearChanged() {
	d.mu.Lock()
	d.changed = map[string]bool{}
	d.mu.Unlock()
}

// selfdevRel 清洗成仓库相对路径（/ 分隔）。
func selfdevRel(p string) string {
	return filepath.ToSlash(filepath.Clean(p))
}

// selfdevReadDenied 读黑名单：密钥（.env*）、运行时状态（data/）、版本库（.git/）、
// 可能含密钥的 MCP/菜单配置、无读取价值的 go.sum、编译产物 qqbot。
func selfdevReadDenied(rel string) bool {
	switch rel {
	case ".env", "data", ".git", "mcp.json", "menu.json", "go.sum", "qqbot":
		return true
	}
	return strings.HasPrefix(rel, ".env.") || strings.HasPrefix(rel, "data/") || strings.HasPrefix(rel, ".git/")
}

// selfdevWriteAllowed 写白名单：仅 plugins/ 下的 .go（含 _test.go）与 main.go。
func selfdevWriteAllowed(rel string) bool {
	if rel == "main.go" {
		return true
	}
	return strings.HasPrefix(rel, "plugins/") && strings.HasSuffix(rel, ".go")
}

// PluginList 列出 plugins/ 下的插件文件。
type PluginList struct{ Dev *DevEnv }

// Name 返回工具名。
func (t *PluginList) Name() string { return "plugin_list" }

// Description 返回工具描述。
func (t *PluginList) Description() string {
	return "列出 plugins/ 目录下的全部 Go 插件文件（文件名、行数、是否测试文件）。给 bot 开发/修改插件前先用它了解现状。仅 bot 主人可用。"
}

// Parameters 返回参数 JSON Schema（无参数）。
func (t *PluginList) Parameters() map[string]any { return nil }

// Execute 列出插件文件。
func (t *PluginList) Execute(ctx context.Context, _ string) (string, error) {
	if _, deny, ok := requireOwner(ctx); !ok {
		return deny, nil
	}
	full, err := resolveUnder(t.Dev.Root, "plugins")
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return "", fmt.Errorf("读取 plugins/ 失败: %w", err)
	}
	var lines []string
	tests := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		isTest := strings.HasSuffix(name, "_test.go")
		if isTest {
			tests++
		}
		n := 0
		if data, err := os.ReadFile(filepath.Join(full, name)); err == nil {
			n = strings.Count(string(data), "\n") + 1
		}
		mark := ""
		if isTest {
			mark = " [测试]"
		}
		lines = append(lines, fmt.Sprintf("- %s（%d 行）%s", name, n, mark))
	}
	if len(lines) == 0 {
		return "plugins/ 下没有 .go 文件。", nil
	}
	return fmt.Sprintf("plugins/ 共 %d 个 .go 文件（%d 个测试）：\n%s\n新插件需要在 main.go 的 UsePlugin 清单注册（用 plugin_edit 改）。",
		len(lines), tests, strings.Join(lines, "\n")), nil
}

// PluginRead 读取仓库内文件（带行号、可分段）。
type PluginRead struct{ Dev *DevEnv }

// Name 返回工具名。
func (t *PluginRead) Name() string { return "plugin_read" }

// Description 返回工具描述。
func (t *PluginRead) Description() string {
	return "读取 bot 仓库内的文件，内容带行号。path 相对仓库根：文件→返回该段内容；目录→列文件清单。长文件用 offset（起始行，默认 1）+ limit（行数，默认 300、上限 500）分段读。写插件常读：plugins/ 下的相似插件、docs/development.md（插件框架教程）、internal/bot/ctx.go 等。.env*、data/、.git/ 等敏感路径不可读。仅 bot 主人可用。"
}

// Parameters 返回参数 JSON Schema。
func (t *PluginRead) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":   map[string]any{"type": "string", "description": "仓库根相对路径，如 plugins/music.go、main.go、docs/development.md"},
			"offset": map[string]any{"type": "integer", "description": "起始行号（1 起，默认 1）"},
			"limit":  map[string]any{"type": "integer", "description": "读取行数（默认 300，上限 500）"},
		},
		"required": []string{"path"},
	}
}

// Execute 读取文件或列目录。
func (t *PluginRead) Execute(ctx context.Context, arguments string) (string, error) {
	if _, deny, ok := requireOwner(ctx); !ok {
		return deny, nil
	}
	rel := strings.TrimSpace(gjson.Get(arguments, "path").String())
	if rel == "" {
		return "", fmt.Errorf("缺少 path 参数")
	}
	if selfdevReadDenied(selfdevRel(rel)) {
		return fmt.Sprintf("%s 在读黑名单里（密钥/运行时状态，不可进上下文），已拒绝。", rel), nil
	}
	full, err := resolveUnder(t.Dev.Root, rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(full)
	if err != nil {
		return "", fmt.Errorf("读取失败: %w", err)
	}
	if info.IsDir() {
		entries, err := os.ReadDir(full)
		if err != nil {
			return "", fmt.Errorf("列目录失败: %w", err)
		}
		var lines []string
		for i, e := range entries {
			if i >= 200 {
				lines = append(lines, "...（超过 200 项，截断）")
				break
			}
			if e.IsDir() {
				lines = append(lines, e.Name()+"/")
			} else if info, ierr := e.Info(); ierr == nil {
				lines = append(lines, fmt.Sprintf("%s（%d B）", e.Name(), info.Size()))
			} else {
				lines = append(lines, e.Name())
			}
		}
		return rel + "/ 下：" + strings.Join(lines, "\n"), nil
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("读取失败: %w", err)
	}
	all := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	offset := int(gjson.Get(arguments, "offset").Int())
	limit := int(gjson.Get(arguments, "limit").Int())
	if offset < 1 {
		offset = 1
	}
	if limit <= 0 {
		limit = 300
	}
	if limit > 500 {
		limit = 500
	}
	end := offset + limit - 1
	if end > len(all) {
		end = len(all)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s 共 %d 行，展示 %d-%d 行：\n", rel, len(all), offset, end)
	for i := offset; i <= end; i++ {
		fmt.Fprintf(&b, "%4d| %s\n", i, all[i-1])
	}
	if end < len(all) {
		fmt.Fprintf(&b, "...（还有 %d 行，继续读传 offset=%d）", len(all)-end, end+1)
	}
	return truncate(b.String(), 16000), nil
}

// PluginWrite 新建/整文件覆盖写入插件源码。
type PluginWrite struct{ Dev *DevEnv }

// Name 返回工具名。
func (t *PluginWrite) Name() string { return "plugin_write" }

// Description 返回工具描述。
func (t *PluginWrite) Description() string {
	return "新建或整文件覆盖写入：path 仅允许 plugins/ 下的 .go（含 _test.go）或 main.go，其余路径一律拒绝；content 为完整文件内容（按 gofmt 规范书写，deploy 会再自动格式化兜底）。小改动优先用 plugin_edit 精确替换，不必整文件重写。仅 bot 主人可用。"
}

// Parameters 返回参数 JSON Schema。
func (t *PluginWrite) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": "目标文件，如 plugins/dice.go、plugins/dice_test.go、main.go"},
			"content": map[string]any{"type": "string", "description": "完整文件内容（Go 源码）"},
		},
		"required": []string{"path", "content"},
	}
}

// Execute 写入文件。
func (t *PluginWrite) Execute(ctx context.Context, arguments string) (string, error) {
	if _, deny, ok := requireOwner(ctx); !ok {
		return deny, nil
	}
	rel := selfdevRel(strings.TrimSpace(gjson.Get(arguments, "path").String()))
	if rel == "" {
		return "", fmt.Errorf("缺少 path 参数")
	}
	if !selfdevWriteAllowed(rel) {
		return fmt.Sprintf("写入范围仅限 plugins/ 下的 .go 与 main.go（%s 不在白名单），已拒绝。", rel), nil
	}
	content := gjson.Get(arguments, "content").String()
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("content 不能为空")
	}
	full, err := resolveUnder(t.Dev.Root, rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", fmt.Errorf("创建目录失败: %w", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("写入失败: %w", err)
	}
	t.Dev.track(rel)
	note := ""
	if !strings.HasSuffix(rel, "_test.go") && rel != "main.go" {
		note = "记得：新插件要在 main.go 的 UsePlugin 清单注册，并补一个 _test.go。"
	}
	return fmt.Sprintf("已写入 %s（%d 字节），改动尚未生效。%s全部完成后用 deploy 检查并部署（可先 check_only 自检）。", rel, len(content), note), nil
}

// PluginEdit 精确替换修改文件（唯一匹配）。
type PluginEdit struct{ Dev *DevEnv }

// Name 返回工具名。
func (t *PluginEdit) Name() string { return "plugin_edit" }

// Description 返回工具描述。
func (t *PluginEdit) Description() string {
	return "精确替换修改（不整文件重写）：old_string 必须在文件中恰好出现一次（0 次或多次都拒绝并说明），替换为 new_string（可为空=删除）。old_string 带足上下文（前后几行）保证唯一。适合改 main.go 注册清单、微调插件逻辑。path 范围同 plugin_write。仅 bot 主人可用。"
}

// Parameters 返回参数 JSON Schema。
func (t *PluginEdit) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":       map[string]any{"type": "string", "description": "目标文件（plugins/ 下的 .go 或 main.go）"},
			"old_string": map[string]any{"type": "string", "description": "要替换的原文（含足够上下文，须在文件中唯一）"},
			"new_string": map[string]any{"type": "string", "description": "替换后的新内容（空字符串=删除该段）"},
		},
		"required": []string{"path", "old_string"},
	}
}

// Execute 精确替换。
func (t *PluginEdit) Execute(ctx context.Context, arguments string) (string, error) {
	if _, deny, ok := requireOwner(ctx); !ok {
		return deny, nil
	}
	rel := selfdevRel(strings.TrimSpace(gjson.Get(arguments, "path").String()))
	if rel == "" {
		return "", fmt.Errorf("缺少 path 参数")
	}
	if !selfdevWriteAllowed(rel) {
		return fmt.Sprintf("写入范围仅限 plugins/ 下的 .go 与 main.go（%s 不在白名单），已拒绝。", rel), nil
	}
	oldStr := gjson.Get(arguments, "old_string").String()
	if oldStr == "" {
		return "", fmt.Errorf("缺少 old_string 参数")
	}
	newStr := gjson.Get(arguments, "new_string").String()
	full, err := resolveUnder(t.Dev.Root, rel)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("读取失败: %w", err)
	}
	if n := strings.Count(string(data), oldStr); n != 1 {
		return "", fmt.Errorf("old_string 在 %s 中出现 %d 次（必须恰好 1 次）；请带上更多上下文重新选取", rel, n)
	}
	out := strings.Replace(string(data), oldStr, newStr, 1)
	if err := os.WriteFile(full, []byte(out), 0o644); err != nil {
		return "", fmt.Errorf("写入失败: %w", err)
	}
	t.Dev.track(rel)
	return fmt.Sprintf("已替换 %s（%d → %d 字节），改动尚未生效；完成后用 deploy 检查并部署。", rel, len(data), len(out)), nil
}

// Deploy 检查链全过后异步编译重启（selfdev 改动的部署入口）。
type Deploy struct{ Dev *DevEnv }

// Name 返回工具名。
func (t *Deploy) Name() string { return "deploy" }

// Description 返回工具描述。
func (t *Deploy) Description() string {
	return "部署对 bot 自身源码的改动：先跑完整检查链（gofmt 自动格式化 → go vet → go build → go test ./...），任一步失败则返回错误详情且运行中的服务不受影响（修完再试）。全部通过后 git 提交（仅本次 AI 改过的文件，selfdev: 前缀）并异步重启——约 15 秒后新版本生效，重启完成主人会收到「机器人已上线」私聊通知。check_only=true 只跑检查不重启（改完代码先自检迭代用）。仅 bot 主人可用。"
}

// Guidelines 贡献使用要点（教模型 selfdev 工作流与门禁）。
func (t *Deploy) Guidelines() []string {
	return []string{
		"selfdev 工作流：plugin_list/plugin_read 了解现状 → plugin_write/plugin_edit 改代码（新插件含 _test.go 且在 main.go 注册）→ deploy check_only 自检 → deploy 正式部署并等「机器人已上线」通知；非主人提出「给我改/写插件」这类请求时直接拒绝，不要尝试调用这些工具",
		"deploy 成功触发重启后直接总结收尾，不要再调用任何工具（bot 进程即将重启）",
	}
}

// Parameters 返回参数 JSON Schema。
func (t *Deploy) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"commit_message": map[string]any{"type": "string", "description": "git 提交说明（简短一行，中文，说明本次改了什么）"},
			"check_only":     map[string]any{"type": "boolean", "description": "true=只跑检查链不提交不重启（默认 false）"},
		},
	}
}

// Execute 跑检查链，全过后（可选）git 提交并异步重启。
func (t *Deploy) Execute(ctx context.Context, arguments string) (string, error) {
	r, deny, ok := requireOwner(ctx)
	if !ok {
		return deny, nil
	}
	commitMsg := strings.TrimSpace(gjson.Get(arguments, "commit_message").String())
	checkOnly := gjson.Get(arguments, "check_only").Bool()

	goBin, err := findGoTool("go")
	if err != nil {
		return "", err
	}
	gofmtBin, err := findGoTool("gofmt")
	if err != nil {
		return "", err
	}

	type step struct {
		name    string
		bin     string
		args    []string
		timeout time.Duration
	}
	steps := []step{
		{"gofmt -l -w .（自动格式化）", gofmtBin, []string{"-l", "-w", "."}, 60 * time.Second},
		{"go vet ./...", goBin, []string{"vet", "./..."}, 180 * time.Second},
		{"go build -o qqbot .", goBin, []string{"build", "-o", "qqbot", "."}, 300 * time.Second},
		{"go test ./...", goBin, []string{"test", "./..."}, 300 * time.Second},
	}
	var report []string
	for _, st := range steps {
		out, err := t.runStep(ctx, st.timeout, st.bin, st.args...)
		if err != nil {
			log.Printf("[selfdev] 主人 %s 部署检查失败：%s", r.UserID, st.name)
			report = append(report, fmt.Sprintf("❌ %s\n%s\n运行中的服务未受影响；请根据报错修改后重试。", st.name, truncate(strings.TrimSpace(out), 3000)))
			return strings.Join(report, "\n"), nil
		}
		if o := strings.TrimSpace(out); o != "" {
			report = append(report, fmt.Sprintf("✅ %s\n%s", st.name, truncate(o, 1500)))
		} else {
			report = append(report, "✅ "+st.name)
		}
	}

	if checkOnly {
		report = append(report, "check_only=true：检查全部通过，未提交未重启。")
		return strings.Join(report, "\n"), nil
	}

	// git 留档：只提交 AI 本次改过的文件（不裹挟仓库里主人自己的未提交改动）。
	files := t.Dev.changedFiles()
	committed := false
	if len(files) == 0 {
		report = append(report, "（没有本次新改动，部署当前代码）")
	} else if gitBin, gerr := exec.LookPath("git"); gerr != nil {
		report = append(report, "⚠️ 未找到 git，本次改动未提交留档（不影响部署）")
	} else {
		args := append([]string{"add"}, files...)
		if out, aerr := t.runStep(ctx, 30*time.Second, gitBin, args...); aerr != nil {
			report = append(report, "⚠️ git add 失败（不影响部署）："+truncate(strings.TrimSpace(out), 300))
			log.Printf("[selfdev] git add 失败: %v", aerr)
		} else {
			if commitMsg == "" {
				commitMsg = "update plugins"
			}
			commitMsg = "selfdev: " + strings.ReplaceAll(commitMsg, "\n", " ")
			out, cerr := t.runStep(ctx, 30*time.Second, gitBin, "commit", "-m", commitMsg)
			if cerr != nil {
				report = append(report, "⚠️ git commit 失败（不影响部署）："+truncate(strings.TrimSpace(out), 300))
				log.Printf("[selfdev] git commit 失败: %v", cerr)
			} else {
				committed = true
				t.Dev.clearChanged()
				report = append(report, fmt.Sprintf("✅ git 已提交（%s，%d 个文件）", commitMsg, len(files)))
			}
		}
	}

	// 异步重启：本工具跑在 bot 进程里，同步 systemctl restart 会杀掉自己、工具调用
	// 无法返回。setsid 分离进程先 sleep 15s 再执行 build-run.sh restart（restart 分支
	// 直接用上面 go build 产出的新二进制），期间 agent 循环把本结果回复给用户。
	if err := os.MkdirAll(filepath.Join(t.Dev.Root, "data"), 0o755); err != nil {
		return "", fmt.Errorf("创建 data 目录失败: %w", err)
	}
	rc := exec.Command("sh", "-c", "sleep 15 && ./scripts/build-run.sh restart >> data/selfdev-deploy.log 2>&1")
	rc.Dir = t.Dev.Root
	rc.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := rc.Start(); err != nil {
		return "", fmt.Errorf("触发重启失败: %w", err)
	}
	log.Printf("[selfdev] 主人 %s 触发部署：检查通过，改动 %d 文件（已提交=%v），15s 后重启（分离进程 pid=%d，日志 data/selfdev-deploy.log）",
		r.UserID, len(files), committed, rc.Process.Pid)
	report = append(report, "🚀 已触发重启：约 15 秒后新版本生效（本次回复如稍有延迟属正常）。重启完成后主人会收到「🤖 机器人已上线」私聊通知，届时可直接验证新功能。请直接总结收尾，不要再调用任何工具。")
	return strings.Join(report, "\n"), nil
}

// runStep 在仓库根执行一步检查命令；超时/失败返回 err（输出一并带回给调用方）。
func (t *Deploy) runStep(ctx context.Context, timeout time.Duration, bin string, args ...string) (string, error) {
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(stepCtx, bin, args...)
	cmd.Dir = t.Dev.Root
	out, err := cmd.CombinedOutput()
	if err != nil {
		if stepCtx.Err() != nil {
			return string(out), fmt.Errorf("超时（>%s）", timeout)
		}
		return string(out), fmt.Errorf("退出错误: %w", err)
	}
	return string(out), nil
}

// findGoTool 解析 go/gofmt 的绝对路径：systemd 服务的 PATH 未必含 Go 安装目录，
// LookPath 失败再试常见安装位置（与 go 同目录的 gofmt 由调用方分别解析）。
func findGoTool(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, dir := range []string{"/usr/local/go/bin", "/usr/lib/go/bin", "/usr/lib/go-1.25/bin"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, dir := range []string{filepath.Join(home, ".go/bin"), filepath.Join(home, "go/bin")} {
			p := filepath.Join(dir, name)
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("找不到 %s（PATH 与常见安装位置均无；服务器需安装 Go 工具链）", name)
}
