package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// Bash 执行 shell 命令。⚠️ 危险：公开 bot 上启用等于把服务器 shell 暴露给所有能发消息的人。
//
// 默认不注册；仅在 main.go 读到 AGENT_ENABLE_BASH=1 时才注册。
//
// 安全档位：
//   - Allow 为空：允许任意命令（最危险）。
//   - Allow 非空：只允许首 token 命中白名单，且禁止 shell 元字符（防 ; | & 等拼接）。
type Bash struct {
	WorkDir string        // 工作目录，默认当前
	Timeout time.Duration // 默认 10s
	Allow   []string      // 命令白名单（按首 token 匹配）；为空表示允许任意
}

// Name 返回工具名。
func (b *Bash) Name() string { return "bash" }

// Description 返回工具描述。
func (b *Bash) Description() string {
	return "执行 shell 命令并返回合并的 stdout/stderr（默认 10s 超时）。谨慎使用。"
}

// Parameters 返回参数 JSON Schema。
func (b *Bash) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string", "description": "要执行的 shell 命令"},
		},
		"required": []string{"command"},
	}
}

// Guidelines 贡献使用要点。
func (b *Bash) Guidelines() []string {
	return []string{
		"bash 仅执行白名单内的命令；带 shell 元字符的命令会被拒",
	}
}

// Execute 执行命令。非零退出码不算错误（输出+退出码作为信息返回给模型）。
func (b *Bash) Execute(ctx context.Context, arguments string) (string, error) {
	cmd := gjson.Get(arguments, "command").String()
	if cmd == "" {
		return "", fmt.Errorf("缺少 command 参数")
	}
	if len(b.Allow) > 0 {
		first := firstToken(cmd)
		if !containsCI(b.Allow, first) {
			return "", fmt.Errorf("命令 %q 不在白名单 %v", first, b.Allow)
		}
		if hasShellMetachar(cmd) {
			return "", fmt.Errorf("白名单模式下禁止 shell 元字符（;|&`$()< 等）")
		}
	}

	timeout := b.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c := exec.CommandContext(runCtx, "sh", "-c", cmd)
	if b.WorkDir != "" {
		c.Dir = b.WorkDir
	}
	out, err := c.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			s += fmt.Sprintf("\n[exit code %d]", ee.ExitCode())
			return truncate(s, 2000), nil // 非零退出：信息，非错误
		}
		if runCtx.Err() == context.DeadlineExceeded {
			return truncate(s+"\n[超时]", 2000), nil
		}
		return "", fmt.Errorf("执行失败: %w", err)
	}
	return truncate(s, 2000), nil
}

// firstToken 取命令首个空白分隔的 token（无则返回空串）。
func firstToken(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// containsCI 大小写不敏感地判断列表是否包含 x。
func containsCI(list []string, x string) bool {
	for _, v := range list {
		if strings.EqualFold(v, x) {
			return true
		}
	}
	return false
}

// hasShellMetachar 检测 shell 元字符（白名单模式下用于阻止命令拼接）。
func hasShellMetachar(s string) bool {
	return strings.ContainsAny(s, ";|&`$()<>\\\n")
}
