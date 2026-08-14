package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// skillAddTool 让 AI 自主从一个 URL 加载技能（注入 system prompt）。
type skillAddTool struct{ ag *Agent }

// Name 返回工具名。
func (t *skillAddTool) Name() string { return "skill_add" }

// Description 返回工具描述。
func (t *skillAddTool) Description() string {
	return "从一个 SKILL.md 的 URL 下载并加载技能（注入你的 system prompt，之后按其中指引行事）。当用户给你一个技能文档的 http(s) 链接时使用。"
}

// Parameters 返回参数 JSON Schema。
func (t *skillAddTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{"type": "string", "description": "SKILL.md 的 http(s) URL"},
		},
		"required": []string{"url"},
	}
}

// Execute 下载并加载技能。
func (t *skillAddTool) Execute(ctx context.Context, arguments string) (string, error) {
	var p struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(arguments), &p); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	if p.URL == "" {
		return "", fmt.Errorf("缺少 url 参数")
	}
	sk, err := t.ag.AddSkillFromURL(ctx, p.URL)
	if err != nil {
		return "", err
	}
	if sk.Description != "" {
		return fmt.Sprintf("已加载技能「%s」：%s", sk.Name, sk.Description), nil
	}
	return fmt.Sprintf("已加载技能「%s」", sk.Name), nil
}

// mcpAddTool 让 AI 自主连接一个 MCP server（stdio/http/sse），并入其工具集。
type mcpAddTool struct{ ag *Agent }

// Name 返回工具名。
func (t *mcpAddTool) Name() string { return "mcp_add" }

// Description 返回工具描述。
func (t *mcpAddTool) Description() string {
	return "连接一个 MCP server，把它的工具并入你可用的工具集。当用户发来 MCP 的 http(s) 链接，或贴来 MCP 配置 JSON（如 Claude Desktop 的 mcpServers 段，stdio 的 command/args/env）时使用。"
}

// Parameters 返回参数 JSON Schema。
func (t *mcpAddTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"config":      map[string]any{"type": "string", "description": "用户贴来的 MCP 配置 JSON 原文（优先用这个）：可以是 {\"mcpServers\":{\"名字\":{...}}} 整段，或单个 server 的 {\"type\":\"stdio\",\"command\":\"npx\",\"args\":[...],\"env\":{...}}；env 值支持 ${VAR} 引用 .env 变量"},
			"url":         map[string]any{"type": "string", "description": "MCP server 的 http/sse URL（用户只给了链接时用这个）"},
			"server_type": map[string]any{"type": "string", "description": "url 方式时的类型：http 或 sse，默认 http", "enum": []string{"http", "sse"}},
		},
	}
}

// Execute 连接 MCP server 并注册工具。
func (t *mcpAddTool) Execute(ctx context.Context, arguments string) (string, error) {
	var p struct {
		Config     string `json:"config"`
		URL        string `json:"url"`
		ServerType string `json:"server_type"`
	}
	if err := json.Unmarshal([]byte(arguments), &p); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	if p.Config != "" {
		cfgs, err := ParseMCPSnippet(p.Config)
		if err != nil {
			return "", fmt.Errorf("config 解析失败: %w", err)
		}
		var lines []string
		for _, cfg := range cfgs {
			n, err := t.ag.AddMCPFromConfig(ctx, cfg)
			if err != nil {
				lines = append(lines, fmt.Sprintf("%s: 失败（%v）", cfg.Name, err))
				continue
			}
			lines = append(lines, fmt.Sprintf("%s: 已连接，接入 %d 个工具", cfg.Name, n))
		}
		return strings.Join(lines, "\n") + "\n已保存，重启后自动重连；新工具现在就可调用", nil
	}
	if p.URL == "" {
		return "", fmt.Errorf("缺少 config 或 url 参数")
	}
	n, err := t.ag.AddMCPFromURL(ctx, p.ServerType, p.URL)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("已连接 MCP server，接入 %d 个工具，现在可以调用它们了", n), nil
}

// readSkillTool 让 AI 按需加载某技能的正文（配合 SkillsPrompt 的懒加载索引）。
type readSkillTool struct{ ag *Agent }

// Name 返回工具名。
func (t *readSkillTool) Name() string { return readSkillToolName }

// Description 返回工具描述。
func (t *readSkillTool) Description() string {
	return "按名称加载一个技能的详细指引正文。当某个任务匹配 available_skills 里某技能的描述时，先用它读取该技能的完整内容，再按其中指引行事。"
}

// Parameters 返回参数 JSON Schema。
func (t *readSkillTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "description": "技能名称（available_skills 中列出的 name）"},
		},
		"required": []string{"name"},
	}
}

// Execute 从 agent 的技能容器里按 name 取正文。
func (t *readSkillTool) Execute(_ context.Context, arguments string) (string, error) {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(arguments), &p); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	if p.Name == "" {
		return "", fmt.Errorf("缺少 name 参数")
	}
	for _, sk := range t.ag.Skills() {
		if sk.Name == p.Name {
			if sk.Content == "" {
				return fmt.Sprintf("技能「%s」没有正文。", sk.Name), nil
			}
			return sk.Content, nil
		}
	}
	return "", fmt.Errorf("未知技能 %q（请用 available_skills 里列出的 name）", p.Name)
}

// RegisterControlTools 把 read_skill / skill_add / mcp_add / send_image 注册进 registry，
// 让 AI 在对话中能按需读技能、自主添加技能/MCP、发图片。
func (a *Agent) RegisterControlTools(reg *Registry) {
	if reg == nil {
		return
	}
	reg.Register(&readSkillTool{ag: a})
	reg.Register(&skillAddTool{ag: a})
	reg.Register(&mcpAddTool{ag: a})
	reg.Register(&sendImageTool{})
}
