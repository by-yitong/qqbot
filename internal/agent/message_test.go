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
