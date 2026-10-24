package bot

// 群成员禁言（协议实现在 pkg/botgo，本层提供类型别名与便捷组合）。
//
//	POST /v2/groups/{group_openid}/restrict_chat_setting（增/改/删禁言，60 QPM）
//	GET  /v2/groups/{group_openid}/restrict_chat_setting（查询状态，30 QPM）
//
// 限制：机器人需是群管理员；单次最多 10 人；禁言最长 30 天；
// 只能操作普通成员（群主/管理员/机器人本身不行）。

import (
	"context"
	"fmt"
	"time"

	"qqbot/pkg/botgo/dto"
)

// 禁言操作（MemberMuteState.Op）。转发 dto 常量，保持旧引用兼容。
const (
	MuteOpAdd    = dto.MuteOpAdd    // 新增禁言
	MuteOpUpdate = dto.MuteOpUpdate // 更新禁言到期时间
	MuteOpDel    = dto.MuteOpDel    // 解除禁言
)

// 全员禁言模式（GlobalMuteRule.Mode）。
const (
	MuteModeNone     = dto.MuteModeNone     // 未开启
	MuteModeAlways   = dto.MuteModeAlways   // 始终禁言
	MuteModeSchedule = dto.MuteModeSchedule // 定时或周期禁言
)

// MuteMaxDuration 单次禁言时长上限（官方限制 30 天）。
const MuteMaxDuration = 30 * 24 * time.Hour

// MemberMuteState 是禁言设置项：op 控制增/改/删，到期时间 RFC3339（del 时可空）。
type MemberMuteState = dto.MemberMuteState

// MuteScheduleRule 定时全员禁言规则。
type MuteScheduleRule = dto.MuteScheduleRule

// MuteRecurringRule 周期全员禁言规则。
type MuteRecurringRule = dto.MuteRecurringRule

// GlobalMuteRule 群级全员禁言配置。
type GlobalMuteRule = dto.GlobalMuteRule

// MutedMember 查询禁言状态返回的单个被禁言成员（不含已过期记录）。
type MutedMember = dto.MutedMember

// GroupMuteState 查询群禁言状态的响应。
type GroupMuteState = dto.GroupMuteState

// SetGroupMemberMutes 批量设置群成员禁言（增/改/删，单次最多 10 人）。
// 需机器人是群管理员；只能操作普通成员。
func (b *Bot) SetGroupMemberMutes(ctx context.Context, groupOpenID string, states []MemberMuteState) error {
	api, err := b.openapi()
	if err != nil {
		return err
	}
	return api.SetGroupMemberMutes(ctx, groupOpenID, states)
}

// MuteGroupMember 禁言一名群成员到 expireAt。官方限制：最长 30 天；
// 只能禁言普通成员（群主/管理员/机器人不行）；机器人需是群管理员。
func (b *Bot) MuteGroupMember(ctx context.Context, groupOpenID, memberOpenID string, expireAt time.Time) error {
	d := time.Until(expireAt)
	if d <= 0 {
		return fmt.Errorf("禁言到期时间必须在未来")
	}
	if d > MuteMaxDuration {
		return fmt.Errorf("禁言时长不能超过 30 天")
	}
	return b.SetGroupMemberMutes(ctx, groupOpenID, []MemberMuteState{{
		Op:           MuteOpAdd,
		MemberOpenID: memberOpenID,
		MuteExpireAt: expireAt.Format(time.RFC3339),
	}})
}

// UnmuteGroupMember 解除一名群成员的禁言。
func (b *Bot) UnmuteGroupMember(ctx context.Context, groupOpenID, memberOpenID string) error {
	return b.SetGroupMemberMutes(ctx, groupOpenID, []MemberMuteState{{
		Op:           MuteOpDel,
		MemberOpenID: memberOpenID,
	}})
}

// GetGroupMuteState 查询群禁言状态（全员禁言规则 + 禁言中成员名单，不含已过期）。
// 需机器人是群管理员。
func (b *Bot) GetGroupMuteState(ctx context.Context, groupOpenID string) (*GroupMuteState, error) {
	api, err := b.openapi()
	if err != nil {
		return nil, err
	}
	return api.GetGroupMuteState(ctx, groupOpenID)
}
