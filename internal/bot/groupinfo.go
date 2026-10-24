package bot

// 群信息与机器人群内状态（协议实现在 pkg/botgo，本层只做连接保护与类型别名）。
//
//   - 群信息：GET /v2/groups/{group_openid}/info（30 QPM）
//   - 机器人状态：GET /v2/groups/{group_openid}/bot_state（30 QPM）
//
// ⚠️ 两接口均为白名单机制：非白名单机器人返回错误码 11253（应用无接口访问权限，
// 需联系平台运营申请）。

import (
	"context"

	"qqbot/pkg/botgo/dto"
)

// GroupInfo 是群基本信息。
type GroupInfo = dto.GroupInfo

// GroupBotState 是机器人在指定群内的状态。
type GroupBotState = dto.GroupBotState

// GetGroupInfo 获取群基本信息（名称/简介/分类/标签/人数）。
// 白名单接口：非白名单机器人返回 11253。
func (b *Bot) GetGroupInfo(ctx context.Context, groupOpenID string) (*GroupInfo, error) {
	api, err := b.openapi()
	if err != nil {
		return nil, err
	}
	return api.GetGroupInfo(ctx, groupOpenID)
}

// GetGroupBotState 获取机器人在指定群内的状态（角色/入群时间/主动推送与收消息设置）。
// 白名单接口：非白名单机器人返回 11253。
func (b *Bot) GetGroupBotState(ctx context.Context, groupOpenID string) (*GroupBotState, error) {
	api, err := b.openapi()
	if err != nil {
		return nil, err
	}
	return api.GetGroupBotState(ctx, groupOpenID)
}
