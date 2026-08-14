package agent

import (
	"context"
	"fmt"
	"strings"
)

// summaryPrefix 是压缩摘要写入历史时加的前缀；Compact 用它识别「上一轮的摘要消息」，
// 从而做增量更新（把新对话并入旧摘要）而非每次从头重新生成。
const summaryPrefix = "[对话摘要] "

// EstimateTokens 粗略估算消息序列的 token 数（chars/4 兜底，无 tokenizer 依赖）。
// 把 content、tool 名与参数都计入字符数。
func EstimateTokens(msgs []Message) int {
	return estimateChars(msgs) / 4
}

func estimateChars(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
		for _, tc := range m.ToolCalls {
			n += len(tc.Name) + len(tc.Arguments)
		}
	}
	return n
}

// ShouldCompact 判断是否需要压缩（threshold<=0 视为关闭）。
func ShouldCompact(msgs []Message, threshold int) bool {
	if threshold <= 0 {
		return false
	}
	return EstimateTokens(msgs) >= threshold
}

// findCutPoint 返回旧前缀的结束下标 cut：msgs[:cut] 会被摘要，msgs[cut:] 保留。
//
// 规则：从 len-keepRecent 起，若落在 tool 消息上，则把 cut 前移到其所属 assistant
// （连同该 assistant 及其全部工具结果一起保留），避免保留窗口出现引用已摘要 assistant
// 的孤立 tool 结果。
//
// 若无可压缩的旧前缀（keepRecent 已覆盖全部、或 cut 退到 0）返回 0。
func findCutPoint(msgs []Message, keepRecent int) int {
	if keepRecent >= len(msgs) {
		return 0
	}
	cut := len(msgs) - keepRecent
	for cut > 0 && msgs[cut].Role == RoleTool {
		cut--
	}
	if cut <= 0 {
		return 0
	}
	return cut
}

// Compact 压缩历史：把旧前缀交给 LLM 摘要，返回 [摘要(assistant), ...recent]。
//
// 增量更新：若旧前缀开头就是上一轮的摘要消息（Content 以 summaryPrefix 起头），把它作为
// previous-summary 传给摘要器做「更新」而非「从头生成」——省 token、长会话更稳定。
// 若无可压缩部分（keepRecent 已覆盖全部、或 cut 退到 0），原样返回。
func Compact(ctx context.Context, llm Completer, msgs []Message, keepRecent int) ([]Message, error) {
	cut := findCutPoint(msgs, keepRecent)
	if cut <= 0 {
		return msgs, nil
	}
	old := msgs[:cut]
	recent := msgs[cut:]

	// 识别旧前缀开头的上一轮摘要，作为增量更新的基础。
	var prevSummary string
	if len(old) > 0 && old[0].Role == RoleAssistant && strings.HasPrefix(old[0].Content, summaryPrefix) {
		prevSummary = strings.TrimPrefix(old[0].Content, summaryPrefix)
		old = old[1:]
	}
	// 摘要被取走后没有新内容可压缩：直接保留 recent（丢掉过时的旧摘要）。
	if len(old) == 0 {
		return recent, nil
	}

	summary, err := summarize(ctx, llm, old, prevSummary)
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(recent)+1)
	out = append(out, Message{Role: RoleAssistant, Content: summaryPrefix + summary})
	out = append(out, recent...)
	return out, nil
}

// summaryFormatBlock 是强制要求摘要器遵循的结构化格式（对齐 pi 的 SUMMARIZATION_PROMPT）。
// 结构化摘要比自由浓缩更能保住关键信息（目标/进展状态/文件路径/函数名/错误原文）。
const summaryFormatBlock = `严格按以下格式（中文）输出：

## 目标
[用户想完成什么；可能多项]

## 约束与偏好
- [用户提到的约束/偏好/要求；没有就写 (无)]

## 进展
### 已完成
- [x] [已完成的事项]
### 进行中
- [ ] [当前在做的事]
### 受阻
- [阻碍进展的问题；没有就写 (无)]

## 关键决定
- **[决定]**：[简短理由]

## 下一步
1. [接下来该做什么的有序列表]

## 关键上下文
- [继续工作所需的数据、文件路径、函数名、错误信息原文等；没有就写 (无)]

各节保持简洁。务必保留精确的文件路径、函数名、错误信息原文。`

// summarize 让 LLM 把一段历史浓缩成「结构化中文摘要」。prevSummary 非空时做增量更新
// （把新对话并入旧摘要），否则从头生成。对齐 pi 的 SUMMARIZATION / UPDATE_SUMMARIZATION prompt。
func summarize(ctx context.Context, llm Completer, msgs []Message, prevSummary string) (string, error) {
	conv := serializeConversation(msgs)
	const sys = "你是对话摘要助手。阅读用户与 AI 助手之间的对话后，按指定格式产出结构化摘要。" +
		"不要继续对话，不要回答对话里的问题，只输出结构化摘要本身。"
	var user string
	if prevSummary != "" {
		user = "下面 <previous-summary> 是此前的摘要，<conversation> 是新增的对话。" +
			"把新对话并入已有摘要，更新成一份新的结构化摘要。规则：\n" +
			"- 保留已有摘要里的全部信息\n" +
			"- 加入新对话里的新进展、新决定、新上下文\n" +
			"- 更新「进展」：完成的项从「进行中」挪到「已完成」\n" +
			"- 基于已完成的事项更新「下一步」\n" +
			"- 务必保留精确的文件路径、函数名、错误信息原文\n" +
			"- 已不再相关的信息可以删除\n\n" +
			summaryFormatBlock + "\n\n" +
			"<previous-summary>\n" + prevSummary + "\n</previous-summary>\n\n" +
			"<conversation>\n" + conv + "\n</conversation>"
	} else {
		user = "下面 <conversation> 是需要摘要的对话。" +
			"产出一份结构化摘要，供另一个 LLM 继续这项工作。\n\n" +
			summaryFormatBlock + "\n\n" +
			"<conversation>\n" + conv + "\n</conversation>"
	}
	res, err := llm.Complete(ctx, sys, []Message{NewUser(user)}, nil)
	if err != nil {
		return "", fmt.Errorf("摘要失败: %w", err)
	}
	if res.Content == "" {
		return "（无摘要）", nil
	}
	return res.Content, nil
}

// serializeConversation 把消息序列渲染成纯文本（喂给摘要器）。
func serializeConversation(msgs []Message) string {
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			fmt.Fprintf(&b, "用户: %s\n", m.Content)
		case RoleAssistant:
			fmt.Fprintf(&b, "助手: %s\n", m.Content)
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "  [调用工具 %s 参数 %s]\n", tc.Name, tc.Arguments)
			}
		case RoleTool:
			fmt.Fprintf(&b, "  [工具 %s 结果]: %s\n", m.Name, m.Content)
		}
	}
	return b.String()
}
