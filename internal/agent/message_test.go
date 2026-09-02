package agent

import "testing"

func TestToOpenAIMessages(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "你好"},
		{Role: RoleAssistant, Content: "在", ToolCalls: []ToolCall{{ID: "1", Name: "f", Arguments: `{"a":1}`}}},
		{Role: RoleTool, Content: "结果", ToolCallID: "1", Name: "f"},
	}
	out := ToOpenAIMessages(msgs)
	if len(out) != 3 {
		t.Fatalf("len=%d", len(out))
	}
	if out[0]["role"] != "user" || out[0]["content"] != "你好" {
		t.Errorf("user msg: %v", out[0])
	}

	asst := out[1]
	if asst["role"] != "assistant" || asst["content"] != "在" {
		t.Errorf("assistant msg: %v", asst)
	}
	tcs, ok := asst["tool_calls"].([]map[string]any)
	if !ok || len(tcs) != 1 {
		t.Fatalf("tool_calls: %v", asst["tool_calls"])
	}
	if tcs[0]["id"] != "1" || tcs[0]["type"] != "function" {
		t.Errorf("tool_call header: %v", tcs[0])
	}
	fn, ok := tcs[0]["function"].(map[string]any)
	if !ok || fn["name"] != "f" || fn["arguments"] != `{"a":1}` {
		t.Errorf("function: %v", tcs[0]["function"])
	}

	tool := out[2]
	if tool["role"] != "tool" || tool["content"] != "结果" ||
		tool["tool_call_id"] != "1" || tool["name"] != "f" {
		t.Errorf("tool msg: %v", tool)
	}
}

func TestToOpenAIMessagesAssistantContentOnly(t *testing.T) {
	// assistant 只有文本、无 tool_calls：不带 tool_calls 字段。
	out := ToOpenAIMessages([]Message{{Role: RoleAssistant, Content: "纯文本"}})
	if _, exists := out[0]["tool_calls"]; exists {
		t.Errorf("不应带 tool_calls: %v", out[0])
	}
	if out[0]["content"] != "纯文本" {
		t.Errorf("content: %v", out[0])
	}
}

func TestToOpenAIMessagesWithImages(t *testing.T) {
	// 多模态：user 带图片时 content 是 [text, image_url...] 数组。
	out := ToOpenAIMessages([]Message{
		NewUserWithImages("这是什么", []string{"https://x/a.png", "https://x/b.jpg"}),
	})
	m := out[0]
	if m["role"] != "user" {
		t.Fatalf("role: %v", m["role"])
	}
	parts, ok := m["content"].([]map[string]any)
	if !ok || len(parts) != 3 {
		t.Fatalf("content parts: %#v", m["content"])
	}
	if parts[0]["type"] != "text" || parts[0]["text"] != "这是什么" {
		t.Errorf("text part: %v", parts[0])
	}
	for i, want := range []string{"https://x/a.png", "https://x/b.jpg"} {
		iu, ok := parts[i+1]["image_url"].(map[string]any)
		if !ok || parts[i+1]["type"] != "image_url" || iu["url"] != want {
			t.Errorf("image part %d: %v", i, parts[i+1])
		}
	}

	// 无文本只有图：补一个占位 text part（部分网关拒绝纯图片 content）。
	out = ToOpenAIMessages([]Message{NewUserWithImages("", []string{"https://x/a.png"})})
	parts = out[0]["content"].([]map[string]any)
	if len(parts) != 2 || parts[0]["type"] != "text" || parts[0]["text"] == "" {
		t.Errorf("空文本应有占位 text part: %#v", parts)
	}
}
