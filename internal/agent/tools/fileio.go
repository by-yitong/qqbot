package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tidwall/gjson"
)

// FileRead 读取 Root 目录下的文件（限制不能越界到 Root 之外）。
// ⚠️ 危险：仅应在 AGENT_ENABLE_FS=1 时注册。
type FileRead struct {
	Root string // 可读写根目录
}

// Name 返回工具名。
func (f *FileRead) Name() string { return "file_read" }

// Description 返回工具描述。
func (f *FileRead) Description() string {
	return "读取根目录下的文本文件内容（路径相对根目录）。"
}

// Parameters 返回参数 JSON Schema。
func (f *FileRead) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "相对根目录的文件路径"},
		},
		"required": []string{"path"},
	}
}

// Guidelines 贡献使用要点。
func (f *FileRead) Guidelines() []string {
	return []string{
		"file_read / file_write 仅限根目录（AGENT_FS_ROOT）内的相对路径，不能越界访问其它目录",
	}
}

// Execute 读取文件。
func (f *FileRead) Execute(_ context.Context, arguments string) (string, error) {
	full, err := resolveUnder(f.Root, gjson.Get(arguments, "path").String())
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("读取失败: %w", err)
	}
	return truncate(string(data), 4000), nil
}

// FileWrite 向 Root 目录下写入文本文件（覆盖）。
// ⚠️ 危险：仅应在 AGENT_ENABLE_FS=1 时注册。
type FileWrite struct {
	Root string
}

// Name 返回工具名。
func (f *FileWrite) Name() string { return "file_write" }

// Description 返回工具描述。
func (f *FileWrite) Description() string {
	return "向根目录下写入文本文件（存在则覆盖）。"
}

// Parameters 返回参数 JSON Schema。
func (f *FileWrite) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": "相对根目录的文件路径"},
			"content": map[string]any{"type": "string", "description": "要写入的文本内容"},
		},
		"required": []string{"path", "content"},
	}
}

// Guidelines 贡献使用要点（与 FileRead 共享同一条根目录约束）。
func (f *FileWrite) Guidelines() []string {
	return []string{
		"file_read / file_write 仅限根目录（AGENT_FS_ROOT）内的相对路径，不能越界访问其它目录",
	}
}

// Execute 写入文件。
func (f *FileWrite) Execute(_ context.Context, arguments string) (string, error) {
	full, err := resolveUnder(f.Root, gjson.Get(arguments, "path").String())
	if err != nil {
		return "", err
	}
	content := gjson.Get(arguments, "content").String()
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", fmt.Errorf("创建目录失败: %w", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("写入失败: %w", err)
	}
	return fmt.Sprintf("已写入 %d 字节到 %s", len(content), gjson.Get(arguments, "path").String()), nil
}

// resolveUnder 把相对路径解析到 root 之下，禁止用 ../ 等越界到 root 外。
func resolveUnder(root, rel string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("未配置根目录")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	full, err := filepath.Abs(filepath.Join(rootAbs, rel))
	if err != nil {
		return "", err
	}
	sep := string(filepath.Separator)
	if full != rootAbs && !strings.HasPrefix(full+sep, rootAbs+sep) {
		return "", fmt.Errorf("路径越界（禁止超出根目录）: %s", rel)
	}
	return full, nil
}
