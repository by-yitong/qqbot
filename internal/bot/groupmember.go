package bot

// 群成员与群黑名单（协议实现在 pkg/botgo，本层只做连接保护与类型别名）。
//
//   - 成员列表：GET /v2/groups/{group_openid}/members（每页最多 30 条，cursor 分页，60 QPM）
//   - 成员信息：GET /v2/groups/{group_openid}/members/{member_openid}（30 QPM）
//   - 批量移除：POST /v2/groups/{group_openid}/batch_remove_members（单次最多 20 人，
//     可同时拉入群黑名单；30 QPM）
//   - 黑名单查询：GET /v2/groups/{group_openid}/member_blacklist（limit 默认 20 最大 100，30 QPM）
//   - 黑名单操作：POST /v2/groups/{group_openid}/member_blacklist（op=add/del，单次最多 20 人；60 QPM）
//
// ⚠️ 五个接口均为白名单机制：非白名单机器人返回错误码 11253（应用无接口访问权限，
// 需联系平台运营申请）。黑名单 add 限制：目标用户不在群中才能加入（在群里的先移除）。

import (
	"context"

	"qqbot/pkg/botgo/dto"
)

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
