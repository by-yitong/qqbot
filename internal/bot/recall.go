package bot

// 消息撤回（DELETE /v2/groups|users/{openid}/messages/{message_id}）。
// 协议实现在 pkg/botgo 的 RetractGroupMessage / RetractC2CMessage（v0.2.1 即有），
// 本层只做连接保护。
//
// 成功返回 HTTP 200 无响应体。限制：发送超过 2 分钟的消息不可撤回；
// 普通成员机器人只能撤回自己发的消息（消息 id 从发送接口响应里取）；
// 群管理员机器人还可撤回普通群成员的消息（id 从群消息事件的 d.id 取）；10 QPS。

import (
	"context"
)

// DeleteGroupMessage 撤回一条群消息（本 bot 场景：撤回自己发的提示消息；
// msgID 来自 SendGroupText 等发送接口的返回）。超过 2 分钟的消息服务端会报错。
func (b *Bot) DeleteGroupMessage(ctx context.Context, groupOpenID, msgID string) error {
	api, err := b.openapi()
	if err != nil {
		return err
	}
	return api.RetractGroupMessage(ctx, groupOpenID, msgID)
}

// DeleteC2CMessage 撤回一条私聊消息（本 bot 场景：撤回自己发的提示消息）。
// 超过 2 分钟的消息服务端会报错。
func (b *Bot) DeleteC2CMessage(ctx context.Context, userOpenID, msgID string) error {
	api, err := b.openapi()
	if err != nil {
		return err
	}
	return api.RetractC2CMessage(ctx, userOpenID, msgID)
}
