package bot

// 群管理 API 门面：群信息、成员与黑名单、禁言、入群审批（协议实现在 pkg/botgo，
// 本层只做连接保护与类型别名）。群事件监听（成员入群/入群申请）见 groupevent.go。
//
// ⚠️ 除注明外均为白名单机制：非白名单机器人返回错误码 11253（应用无接口访问权限，
// 需联系平台运营申请）。

import (
	"context"
	"fmt"
	"time"

	"qqbot/pkg/botgo/dto"
)

// --- 群信息与机器人状态 ---
//
//	GET /v2/groups/{group_openid}/info（30 QPM）
//	GET /v2/groups/{group_openid}/bot_state（30 QPM）

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

// --- 成员与黑名单 ---
//
//	成员列表   GET  /v2/groups/{group_openid}/members（每页最多 30 条，cursor 分页，60 QPM）
//	成员信息   GET  /v2/groups/{group_openid}/members/{member_openid}（30 QPM）
//	批量移除   POST /v2/groups/{group_openid}/batch_remove_members（单次最多 20 人，可同时拉入黑名单，30 QPM）
//	黑名单查询 GET  /v2/groups/{group_openid}/member_blacklist（limit 默认 20 最大 100，30 QPM）
//	黑名单操作 POST /v2/groups/{group_openid}/member_blacklist（op=add/del，单次最多 20 人，60 QPM）
//
// 黑名单 add 限制：目标用户不在群中才能加入（在群里的先移除）。

// 群成员角色（GroupMember.MemberRole）。转发 dto 常量，保持旧引用兼容。
const (
	MemberRoleMember = dto.MemberRoleMember // 普通成员
	MemberRoleOwner  = dto.MemberRoleOwner  // 群主
	MemberRoleAdmin  = dto.MemberRoleAdmin  // 管理员
)

// 黑名单操作（SetGroupBlacklist 的 op）。
const (
	BlacklistOpAdd = dto.BlacklistOpAdd // 加入黑名单（目标须不在群中）
	BlacklistOpDel = dto.BlacklistOpDel // 移出黑名单
)

// GroupMember 群成员（列表项与详情共用同一结构）。
type GroupMember = dto.GroupMember

// RemoveMembersResult 批量移除的响应：移除成功返回 success，
// add_to_member_blacklist=true 时可能部分成员拉黑失败（openid 列表）。
type RemoveMembersResult = dto.RemoveMembersResult

// GroupBlacklistUser 群黑名单用户。
type GroupBlacklistUser = dto.GroupBlacklistUser

// ListGroupMembers 拉取群成员列表（自动翻页聚合）。白名单接口：非白名单机器人返回 11253。
func (b *Bot) ListGroupMembers(ctx context.Context, groupOpenID string) ([]GroupMember, error) {
	api, err := b.openapi()
	if err != nil {
		return nil, err
	}
	return api.ListGroupMembers(ctx, groupOpenID)
}

// GetGroupMember 获取指定群成员的详细信息。白名单接口：非白名单机器人返回 11253。
func (b *Bot) GetGroupMember(ctx context.Context, groupOpenID, memberOpenID string) (*GroupMember, error) {
	api, err := b.openapi()
	if err != nil {
		return nil, err
	}
	return api.GetGroupMember(ctx, groupOpenID, memberOpenID)
}

// RemoveGroupMembers 批量移除群成员（单次最多 20 人），addToBlacklist=true 同时拉入群黑名单。
// 白名单接口：非白名单机器人返回 11253。
func (b *Bot) RemoveGroupMembers(ctx context.Context, groupOpenID string, memberOpenIDs []string, addToBlacklist bool) (*RemoveMembersResult, error) {
	api, err := b.openapi()
	if err != nil {
		return nil, err
	}
	return api.BatchRemoveGroupMembers(ctx, groupOpenID, memberOpenIDs, addToBlacklist)
}

// ListGroupBlacklist 拉取群黑名单（自动翻页聚合）。白名单接口：非白名单机器人返回 11253。
func (b *Bot) ListGroupBlacklist(ctx context.Context, groupOpenID string) ([]GroupBlacklistUser, error) {
	api, err := b.openapi()
	if err != nil {
		return nil, err
	}
	return api.ListGroupBlacklist(ctx, groupOpenID)
}

// SetGroupBlacklist 群黑名单操作（add/del，单次最多 20 人），返回失败的 openid 列表。
// add 限制：目标用户不在群中才能加入（在群里的先移除）。白名单接口：非白名单机器人返回 11253。
func (b *Bot) SetGroupBlacklist(ctx context.Context, groupOpenID, op string, memberOpenIDs []string) ([]string, error) {
	api, err := b.openapi()
	if err != nil {
		return nil, err
	}
	return api.SetGroupBlacklist(ctx, groupOpenID, op, memberOpenIDs)
}

// --- 禁言 ---
//
//	POST /v2/groups/{group_openid}/restrict_chat_setting（增/改/删禁言，60 QPM）
//	GET  /v2/groups/{group_openid}/restrict_chat_setting（查询状态，30 QPM）
//
// 限制：机器人需是群管理员；单次最多 10 人；禁言最长 30 天；
// 只能操作普通成员（群主/管理员/机器人本身不行）。

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

// --- 入群审批 ---
//
//	审批     POST /v2/groups/{group_openid}/approval_join_request/{member_openid}（60 QPM，需群管理员）
//	申请列表 GET  /v2/groups/{group_openid}/join_request_list（每页 50 条，30 QPM，需群管理员）
//	策略列表 GET  /v2/groups/join_approval_strategy（机器人维度，不带群 id，60 QPM）

// JoinReviewOpApprove / JoinReviewOpDecline 是审批动作（转发 dto 常量，保持旧引用兼容）。
const (
	JoinReviewOpApprove = dto.JoinRequestApprove
	JoinReviewOpDecline = dto.JoinRequestDecline
)

// GroupJoinRequestItem 是 join_request_list 返回的申请项。
type GroupJoinRequestItem = dto.GroupJoinRequest

// JoinApprovalStrategy 入群自动审批策略：把关联群中命中白名单 QQ 号的入群申请
// 自动审批通过。一个机器人最多 20 个策略；仅在机器人拥有关联群管理员身份时生效。
// （另有创建/修改/删除/执行/白名单维护接口，见 pkg/botgo dto 注释里的官方文档。）
type JoinApprovalStrategy = dto.JoinApprovalStrategy

// ApproveGroupJoinRequest 审批一条入群申请（通过或拒绝）。
// joinRequestID 建议从事件带回（官方标注可选，回传可精确定位申请）；
// decline 时可带 rejectReason（给申请人看的拒绝理由）。需机器人是群管理员。
func (b *Bot) ApproveGroupJoinRequest(ctx context.Context, groupOpenID, memberOpenID, joinRequestID, op, rejectReason string) error {
	api, err := b.openapi()
	if err != nil {
		return err
	}
	return api.ApproveGroupJoinRequest(ctx, groupOpenID, memberOpenID, dto.JoinRequestApproval{
		Op:            op,
		JoinRequestID: joinRequestID,
		RejectReason:  rejectReason,
	})
}

// ListGroupJoinRequests 拉取一个群的待处理入群申请列表（自动翻页聚合）。
// 需机器人是群管理员。
func (b *Bot) ListGroupJoinRequests(ctx context.Context, groupOpenID string) ([]GroupJoinRequestItem, error) {
	api, err := b.openapi()
	if err != nil {
		return nil, err
	}
	return api.ListGroupJoinRequests(ctx, groupOpenID)
}

// ListGroupJoinApprovalStrategies 拉取入群自动审批策略列表（自动翻页聚合）。
// 注意路径不带群 id——策略是机器人维度的，一个策略可关联多个群；60 QPM。
func (b *Bot) ListGroupJoinApprovalStrategies(ctx context.Context) ([]JoinApprovalStrategy, error) {
	api, err := b.openapi()
	if err != nil {
		return nil, err
	}
	return api.ListJoinApprovalStrategies(ctx)
}
