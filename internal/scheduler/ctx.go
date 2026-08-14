package scheduler

import "context"

// ChatInfo 是随消息 ctx 注入的「当前聊天」信息：agent 工具（create_task）经它拿到
// 新建任务的默认发送目标与创建者。仿 memory.WithScope 的注入模式，由 plugins/agent.go
// 在每次 Run 前注入。
type ChatInfo struct {
	Target   Target // 当前聊天（群/私聊/频道）作为任务结果的默认发送目标
	UserID   string // 说话人 openid（任务的 Author）
	Username string // 群昵称（可能为空）
}

type chatInfoKey struct{}

// WithChatInfo 把当前聊天信息塞进 ctx。
func WithChatInfo(ctx context.Context, info ChatInfo) context.Context {
	return context.WithValue(ctx, chatInfoKey{}, info)
}

// ChatInfoFrom 取出注入的聊天信息。
func ChatInfoFrom(ctx context.Context) (ChatInfo, bool) {
	info, ok := ctx.Value(chatInfoKey{}).(ChatInfo)
	return info, ok
}
