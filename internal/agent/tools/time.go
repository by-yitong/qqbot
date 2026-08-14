package tools

import (
	"context"
	"fmt"
	"time"
)

// Now 返回服务器当前时间（本地时区 + UTC）。
type Now struct {
	Location *time.Location // nil = 本地时区
}

// Name 返回工具名。
func (n *Now) Name() string { return "current_time" }

// Description 返回工具描述。
func (n *Now) Description() string {
	return "获取服务器当前时间（ISO8601，含本地时区与 UTC）。当用户问现在几点/今天日期时使用。"
}

// Parameters 返回参数 JSON Schema（无参数）。
func (n *Now) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

// Execute 返回当前时间字符串。
func (n *Now) Execute(_ context.Context, _ string) (string, error) {
	loc := n.Location
	if loc == nil {
		loc = time.Local
	}
	now := time.Now().In(loc)
	return fmt.Sprintf("本地时间(%s): %s\nUTC: %s",
		loc.String(), now.Format(time.RFC3339), now.UTC().Format(time.RFC3339)), nil
}
