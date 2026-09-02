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

// skillCreateTool 让 AI 自主创建/更新技能（写 SKILL.md 落盘 + 立即生效）。
type skillCreateTool struct{ ag *Agent }

// Name 返回工具名。
func (t *skillCreateTool) Name() string { return "skill_create" }

// Description 返回工具描述。
func (t *skillCreateTool) Description() string {
	return "创建（或同名更新）一个属于你自己的技能：写清它的 name、description 和正文指引，落盘保存并立即生效。技能会在之后的对话中按 description 自动触发（配合 read_skill 按需加载正文）。当用户让你沉淀/记住一套做法，或你发现某个流程值得复用时使用。"
}

// Parameters 返回参数 JSON Schema。
func (t *skillCreateTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":        map[string]any{"type": "string", "description": "技能名：小写字母/数字/连字符，如 baozang-roulette"},
			"description": map[string]any{"type": "string", "description": "何时使用该技能（一句话，写清触发场景与用户措辞，这是自动触发的唯一依据，要具体）"},
			"content":     map[string]any{"type": "string", "description": "SKILL.md 正文：markdown 格式的操作指引，写给未来的你自己看。用祈使句直接给步骤；开头一句话说清目标，然后分步骤/要点，含关键命令、参数格式、边界情况与反例"},
		},
		"required": []string{"name", "description", "content"},
	}
}

// Guidelines 贡献使用要点（教模型把重复流程沉淀成技能）。
func (t *skillCreateTool) Guidelines() []string {
	return []string{
		"同一套操作流程被第二次走通时，考虑用 skill_create 把它沉淀成技能（description 写清触发场景），下次同类请求直接 read_skill 复用，不必从头摸索",
		"skill_create 同名调用即覆盖更新：迭代改进已有技能时直接用新内容再创建一次",
	}
}

// Execute 创建/更新技能。
func (t *skillCreateTool) Execute(_ context.Context, arguments string) (string, error) {
	var p struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Content     string `json:"content"`
	}
	if err := json.Unmarshal([]byte(arguments), &p); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	sk, err := t.ag.CreateSkill(p.Name, p.Description, p.Content)
	if err != nil {
		return "", err
	}
	where := "（未配置技能目录，仅本次运行有效，重启后丢失）"
	if sk.FilePath != "" {
		where = "，已写入 " + sk.FilePath + "（重启后自动加载）"
	}
	return fmt.Sprintf("技能「%s」已创建并生效%s。它已出现在 available_skills 索引里，之后匹配到相关请求时用 read_skill 加载正文执行。", sk.Name, where), nil
}

// skillDeleteTool 让 AI 自主删除一个技能（运行时 + 落盘文件）。
type skillDeleteTool struct{ ag *Agent }

// Name 返回工具名。
func (t *skillDeleteTool) Name() string { return "skill_delete" }

// Description 返回工具描述。
func (t *skillDeleteTool) Description() string {
	return "删除一个技能：从运行时与技能目录中一并移除。当用户说不要某个技能/技能写坏了要重来时使用（要改写直接用 skill_create 同名覆盖更方便）。"
}

// Parameters 返回参数 JSON Schema。
func (t *skillDeleteTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "description": "技能名（available_skills 中列出的 name）"},
		},
		"required": []string{"name"},
	}
}

// Execute 删除技能。
func (t *skillDeleteTool) Execute(_ context.Context, arguments string) (string, error) {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(arguments), &p); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	if err := t.ag.DeleteSkill(p.Name); err != nil {
		return "", err
	}
	return fmt.Sprintf("技能「%s」已删除", p.Name), nil
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
	return "按名称加载一个技能的详细指引正文。当某个任务匹配 available_skills 里某技能的描述时，先用它读取该技能的完整内容，再按其中指引行事。带引用材料的目录型技能（如 hallmark）：name 传「技能名」读 SKILL.md 正文，传「技能名/相对路径」（如 hallmark/references/color.md）读其中引用的参考文件——SKILL.md 里提到的 references/xxx.md 就这样按需加载。"
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
	// 「技能名/相对路径」：读该技能目录下的引用材料（references/ 等）。
	if strings.Contains(p.Name, "/") {
		return t.ag.ReadSkillAsset(p.Name)
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
	reg.Register(&skillCreateTool{ag: a})
	reg.Register(&skillDeleteTool{ag: a})
	reg.Register(&mcpAddTool{ag: a})
	reg.Register(&sendImageTool{})
}
