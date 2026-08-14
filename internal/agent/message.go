package agent

// Role 是消息角色。
type Role string

const (
	RoleSystem    Role = "system"    // 系统提示（不入历史，每轮由 Agent 重建）
	RoleUser      Role = "user"      // 用户消息
	RoleAssistant Role = "assistant" // 模型回复
	RoleTool      Role = "tool"      // 工具执行结果
)

// ToolCall 是一次工具调用请求（由 assistant 发起）。
// ID 由 LLM 分配，回传 tool 结果时用它锚定配对。
type ToolCall struct {
	ID        string // 调用 id
	Name      string // 工具名
	Arguments string // 原始 JSON 字符串参数
}

// Message 是 agent 内部的消息表示。
// system 不入历史，每轮由 Agent 从 base 人设 + skills 重建。
type Message struct {
	Role       Role       // user / assistant / tool
	Content    string     // 文本内容
	ToolCalls  []ToolCall // 仅 assistant 可携带（请求调用工具）
	ToolCallID string     // role==tool 时，对应的 ToolCall.ID
	Name       string     // role==tool 时，工具名

	// ephemeral 标记「临时上下文消息」（如增量群聊动态）：随请求发给 LLM，
	// 但 persist 时剥离、永不落 session。放在问题消息紧前面——动态内容位于
	// 请求尾部，不破坏 system+历史 的前缀，LLM 上下文缓存可整段命中。
	// 小写字段不参与 JSON 序列化，即便意外落盘也不会带标记复活。
	ephemeral bool
}

// NewUser 构造一条 user 消息。
func NewUser(s string) Message { return Message{Role: RoleUser, Content: s} }

// newEphemeralUser 构造一条临时 user 消息（发 LLM、不落 session）。
func newEphemeralUser(s string) Message { return Message{Role: RoleUser, Content: s, ephemeral: true} }

// stripEphemeral 剥离临时消息（persist 前调用）。
func stripEphemeral(msgs []Message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if !m.ephemeral {
			out = append(out, m)
		}
	}
	return out
}

// ToOpenAIMessages 把内部消息序列转换为 OpenAI Chat Completions 的 messages 数组。
//   - system / user：{role, content}
//   - assistant：{role, content, tool_calls?}
//   - tool：{role:"tool", content, tool_call_id, name?}
func ToOpenAIMessages(msgs []Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case RoleSystem, RoleUser:
			out = append(out, map[string]any{"role": string(m.Role), "content": m.Content})
		case RoleAssistant:
			mm := map[string]any{"role": "assistant"}
			if m.Content != "" {
				mm["content"] = m.Content
			}
			if len(m.ToolCalls) > 0 {
				tcs := make([]map[string]any, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					tcs = append(tcs, map[string]any{
						"id":   tc.ID,
						"type": "function",
						"function": map[string]any{
							"name":      tc.Name,
							"arguments": tc.Arguments,
						},
					})
				}
				mm["tool_calls"] = tcs
			}
			out = append(out, mm)
		case RoleTool:
			mm := map[string]any{
				"role":         "tool",
				"content":      m.Content,
				"tool_call_id": m.ToolCallID,
			}
			if m.Name != "" {
				mm["name"] = m.Name
			}
			out = append(out, mm)
		}
	}
	return out
}
