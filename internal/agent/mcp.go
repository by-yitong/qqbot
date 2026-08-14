package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// MCPServerConfig 描述一个 MCP server。stdio 用 Command/Args/Env；http/sse 用 URL。
type MCPServerConfig struct {
	Name    string            `json:"name"`    // 实例名（日志/工具前缀）；缺省用 mcpServers 的 key
	Type    string            `json:"type"`    // "stdio"(默认) | "http" | "sse"
	Command string            `json:"command"` // stdio: 可执行命令
	Args    []string          `json:"args"`    // stdio: 命令参数
	Env     map[string]string `json:"env"`     // stdio: 环境变量（在 os.Environ 基础上追加/覆盖）
	URL     string            `json:"url"`     // http/sse: server URL
}

// MCPConfigFile 是 mcp.json 的结构（对照 Claude Desktop）。
//
//	{ "mcpServers": { "weather": { "command": "npx", "args": ["-y", "..."] } } }
type MCPConfigFile struct {
	McpServers map[string]MCPServerConfig `json:"mcpServers"`
}

// LoadMCPConfig 读取 mcp.json。文件不存在视为空（非错误）。
func LoadMCPConfig(path string) ([]MCPServerConfig, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var cfg MCPConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}
	out := make([]MCPServerConfig, 0, len(cfg.McpServers))
	for name, sc := range cfg.McpServers {
		if sc.Name == "" {
			sc.Name = name
		}
		if sc.Type == "" {
			if sc.URL != "" {
				sc.Type = "http"
			} else {
				sc.Type = "stdio"
			}
		}
		out = append(out, sc)
	}
	return out, nil
}

// MCPClient 封装一个已连接（Start+Initialize）的 MCP server 客户端。
type MCPClient struct {
	Name string
	cli  *client.Client
}

// DialMCP 连接并初始化单个 MCP server（内置 60s 拨号超时，给 stdio 首次
// npx 下载留余量）。
func DialMCP(ctx context.Context, cfg MCPServerConfig) (*MCPClient, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var c *client.Client
	var err error
	switch strings.ToLower(cfg.Type) {
	case "stdio", "":
		if cfg.Command == "" {
			return nil, fmt.Errorf("%s: stdio 类型需要 command", cfg.Name)
		}
		env := os.Environ()
		for k, v := range cfg.Env {
			env = append(env, k+"="+expandEnvRef(v))
		}
		c, err = client.NewStdioMCPClient(cfg.Command, env, cfg.Args...)
	case "http", "streamable":
		if cfg.URL == "" {
			return nil, fmt.Errorf("%s: http 类型需要 url", cfg.Name)
		}
		c, err = client.NewStreamableHttpClient(cfg.URL)
	case "sse":
		if cfg.URL == "" {
			return nil, fmt.Errorf("%s: sse 类型需要 url", cfg.Name)
		}
		c, err = client.NewSSEMCPClient(cfg.URL)
	default:
		return nil, fmt.Errorf("%s: 未知类型 %q", cfg.Name, cfg.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("创建 %s 客户端: %w", cfg.Name, err)
	}
	if err := c.Start(ctx); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("启动 %s: %w", cfg.Name, err)
	}
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "qqbot", Version: "1.0"},
			Capabilities:    mcp.ClientCapabilities{},
		},
	}); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("初始化 %s: %w", cfg.Name, err)
	}
	return &MCPClient{Name: cfg.Name, cli: c}, nil
}

// Close 关闭连接。
func (m *MCPClient) Close() error {
	if m == nil || m.cli == nil {
		return nil
	}
	return m.cli.Close()
}

// Tools 列出该 server 的工具，适配成 agent.Tool。
func (m *MCPClient) Tools(ctx context.Context) ([]Tool, error) {
	res, err := m.cli.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("列出 %s 工具: %w", m.Name, err)
	}
	tools := make([]Tool, 0, len(res.Tools))
	for _, t := range res.Tools {
		tools = append(tools, m.wrapTool(t))
	}
	return tools, nil
}

// mcpTool 把单个 MCP 工具适配成 agent.Tool。
// 对外用带前缀的别名（mcp_<server>_<tool>，已规整）避免多 server 间重名；
// 实际调用时用 def.Name（原始 MCP 工具名）。
type mcpTool struct {
	owner *MCPClient
	def   mcp.Tool
	alias string
}

func (m *MCPClient) wrapTool(t mcp.Tool) *mcpTool {
	return &mcpTool{owner: m, def: t, alias: sanitizeToolName("mcp_" + m.Name + "_" + t.Name)}
}

// Name 返回对外工具名（带前缀）。
func (t *mcpTool) Name() string { return t.alias }

// Description 返回工具描述。
func (t *mcpTool) Description() string {
	if t.def.Description != "" {
		return t.def.Description
	}
	return "MCP 工具 " + t.def.Name + "（来自 " + t.owner.Name + "）"
}

// Parameters 返回参数 JSON Schema（由 MCP 工具的 InputSchema 转换而来）。
func (t *mcpTool) Parameters() map[string]any {
	if b, err := json.Marshal(t.def.InputSchema); err == nil && len(b) > 0 {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err == nil {
			return m
		}
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

// Execute 调用 MCP 工具，把结果内容拼成文本返回。
func (t *mcpTool) Execute(ctx context.Context, arguments string) (string, error) {
	var args any
	if arguments != "" {
		_ = json.Unmarshal([]byte(arguments), &args) // 解析失败则原样传 nil
	}
	res, err := t.owner.cli.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: t.def.Name, Arguments: args},
	})
	if err != nil {
		return "", fmt.Errorf("调用 %s.%s: %w", t.owner.Name, t.def.Name, err)
	}
	return mcpResultText(res), nil
}

// mcpResultText 把 CallToolResult 的内容拼成文本（IsError 时加前缀）。
func mcpResultText(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range res.Content {
		switch v := c.(type) {
		case mcp.TextContent:
			b.WriteString(v.Text)
		case *mcp.TextContent:
			if v != nil {
				b.WriteString(v.Text)
			}
		default:
			if tc, ok := mcp.AsTextContent(c); ok && tc != nil {
				b.WriteString(tc.Text)
			} else if js, err := json.Marshal(c); err == nil {
				b.Write(js)
			}
		}
		b.WriteByte('\n')
	}
	text := strings.TrimSpace(b.String())
	if res.IsError {
		return "MCP 工具错误: " + text
	}
	return text
}

// sanitizeToolName 规整成 OpenAI 合法的工具名（[A-Za-z0-9_-]，≤64）。
func sanitizeToolName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 64 {
		out = out[:64]
	}
	if out == "" {
		out = "mcp_tool"
	}
	return out
}

// envVarRe 只匹配 ${NAME} 形式的引用（$NAME 与字面 $ 不动），避免误伤含 $ 的普通值。
var envVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnvRef 把 env 值里的 ${VAR} 展开成进程环境变量（.env 已在启动时注入），
// 让 mcp.json 引用密钥而不落明文，如 "Z_AI_API_KEY": "${AI_API_KEY}"。
func expandEnvRef(v string) string {
	if !strings.Contains(v, "${") {
		return v
	}
	return envVarRe.ReplaceAllStringFunc(v, func(m string) string {
		return os.Getenv(envVarRe.FindStringSubmatch(m)[1])
	})
}

// ParseMCPSnippet 解析聊天里贴来的 MCP 配置 JSON，兼容三种形态：
//  1. {"mcpServers":{"名字":{...}}}   Claude Desktop / 各家客户端配置的整段
//  2. {"name":"名字","command":"npx","args":[...],"env":{...}}  单个 server 配置
//  3. {"command":"npx","args":[...]}  无名单 server（名字按 command/args 推导）
//
// 容忍 ```json 代码围栏（LLM 转述时常见）。env 值里的 ${VAR} 由 DialMCP 时展开。
func ParseMCPSnippet(raw string) ([]MCPServerConfig, error) {
	raw = strings.TrimSpace(stripCodeFence(raw))
	if raw == "" || raw[0] != '{' {
		return nil, fmt.Errorf("无法识别的 MCP 配置（需要 JSON 对象）")
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return nil, fmt.Errorf("JSON 解析失败: %w", err)
	}
	var out []MCPServerConfig
	if _, ok := probe["mcpServers"]; ok {
		var f MCPConfigFile
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			return nil, fmt.Errorf("JSON 解析失败: %w", err)
		}
		for name, sc := range f.McpServers {
			if sc.Name == "" {
				sc.Name = name
			}
			out = append(out, sc)
		}
		return out, nil
	}
	var sc MCPServerConfig
	if err := json.Unmarshal([]byte(raw), &sc); err != nil {
		return nil, fmt.Errorf("JSON 解析失败: %w", err)
	}
	if sc.Command == "" && sc.URL == "" {
		return nil, fmt.Errorf("配置缺 command（stdio）或 url（http/sse）")
	}
	sc.Name = defaultMCPName(sc)
	return []MCPServerConfig{sc}, nil
}

// defaultMCPName 给无名配置推导实例名：name 字段 > args 里的包名（npx/uvx 场景）> command 基名。
func defaultMCPName(cfg MCPServerConfig) string {
	if cfg.Name != "" {
		return cfg.Name
	}
	for i := len(cfg.Args) - 1; i >= 0; i-- {
		a := cfg.Args[i]
		if strings.HasPrefix(a, "-") || strings.HasPrefix(a, "$") {
			continue
		}
		if i := strings.LastIndex(a, "/"); i >= 0 {
			a = a[i+1:]
		}
		return strings.TrimPrefix(a, "@")
	}
	if cfg.Command != "" {
		return filepath.Base(cfg.Command)
	}
	return "mcp"
}

// stripCodeFence 去掉 ``` / ```json 围栏，返回内层文本；无围栏原样返回。
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	} else {
		return ""
	}
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// SaveRuntimeMCP 把运行时添加的 server 配置并入持久化文件（mcpServers 按名合并，
// 同名覆盖）。文件与 mcp.json 同格式，启动时一并加载，重启自动重连。
func SaveRuntimeMCP(path string, cfgs []MCPServerConfig) error {
	if path == "" || len(cfgs) == 0 {
		return nil
	}
	existing, err := LoadMCPConfig(path)
	if err != nil {
		return err
	}
	byName := map[string]MCPServerConfig{}
	for _, c := range existing {
		byName[c.Name] = c
	}
	for _, c := range cfgs {
		byName[c.Name] = c
	}
	file := MCPConfigFile{McpServers: byName}
	b, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// DialAllMCP 连接配置里的所有 server，把它们的所有工具注册到 registry。
// 单个 server 失败不致命（记日志跳过），返回成功连接的客户端以便后续 Close。
func DialAllMCP(ctx context.Context, cfgs []MCPServerConfig, registry *Registry) ([]*MCPClient, error) {
	var clients []*MCPClient
	for _, cfg := range cfgs {
		mc, err := DialMCP(ctx, cfg)
		if err != nil {
			log.Printf("[mcp] 连接 %s 失败（已跳过）: %v", cfg.Name, err)
			continue
		}
		tools, err := mc.Tools(ctx)
		if err != nil {
			log.Printf("[mcp] 列出 %s 工具失败（已跳过）: %v", cfg.Name, err)
			_ = mc.Close()
			continue
		}
		for _, t := range tools {
			registry.Register(t)
		}
		log.Printf("[mcp] %s: 接入 %d 个工具", cfg.Name, len(tools))
		clients = append(clients, mc)
	}
	if len(clients) == 0 && len(cfgs) > 0 {
		return clients, errors.New("所有 MCP server 均连接失败")
	}
	return clients, nil
}

// CloseAll 关闭全部 MCP 客户端（逐个，忽略错误）。
func CloseAll(clients []*MCPClient) {
	for _, c := range clients {
		_ = c.Close()
	}
}
