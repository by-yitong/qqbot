// Package agent 实现一个轻量的对话 agent harness：工具循环（及后续的多轮记忆、
// 上下文压缩、技能、MCP）。设计参考 earendil-works/pi 的 coding-agent。
//
// 核心：内部 Message 类型独立，只在调用 LLM 时转换（对应 pi 的 convertToLlm）；
// Agent.Run 是「调 LLM → 执行工具 → 再调 LLM」的循环，直到模型不再请求工具即
// 返回最终文本（对应 pi 的 agent-loop.ts）。
package agent
