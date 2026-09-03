package bot

// 群成员与群黑名单接口（官方 2026-09 新增，botgo v0.2.1 未覆盖，经 qqAPI 自行 HTTP 调用）。
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
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// 群成员角色（GroupMember.MemberRole）。
const (
	MemberRoleMember = "member" // 普通成员
	MemberRoleOwner  = "owner"  // 群主
	MemberRoleAdmin  = "admin"  // 管理员
)

// GroupMember 群成员（列表项与详情共用同一结构）。
type GroupMember struct {
	MemberOpenID string `json:"member_openid"`
	Username     string `json:"username"`
	MemberRole   string `json:"member_role"` // MemberRole* 常量
	Bot          bool   `json:"bot"`
	JoinedAt     string `json:"joined_at"`    // 入群时间（RFC3339）
	UnionOpenID  string `json:"union_openid"` // 应用/开放平台统一标识（如有）
}

// memberPage 翻页安全上限：成员每页 30 条、黑名单每页 100 条，100 页分别够 3000/10000 条，
// 防服务端异常（next_cursor 永不为空）时翻页死循环。
const maxMemberPages = 100

// ListGroupMembers 拉取群成员列表（自动翻页聚合）。白名单接口：非白名单机器人返回 11253。
func (b *Bot) ListGroupMembers(ctx context.Context, groupOpenID string) ([]GroupMember, error) {
	var out []GroupMember
	cursor := ""
	for i := 0; i < maxMemberPages; i++ {
		path := "/v2/groups/" + url.PathEscape(groupOpenID) + "/members"
		if cursor != "" {
			path += "?cursor=" + url.QueryEscape(cursor)
		}
		body, err := b.qqAPI(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Members    []GroupMember `json:"members"`
			NextCursor string        `json:"next_cursor"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("解析群成员列表: %w", err)
		}
		out = append(out, page.Members...)
		if page.NextCursor == "" || len(page.Members) == 0 {
			return out, nil
		}
		cursor = page.NextCursor
	}
	return out, nil
}

// GetGroupMember 获取指定群成员的详细信息。白名单接口：非白名单机器人返回 11253。
func (b *Bot) GetGroupMember(ctx context.Context, groupOpenID, memberOpenID string) (*GroupMember, error) {
	body, err := b.qqAPI(ctx, http.MethodGet,
		"/v2/groups/"+url.PathEscape(groupOpenID)+"/members/"+url.PathEscape(memberOpenID), nil)
	if err != nil {
		return nil, err
	}
	var m GroupMember
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("解析群成员信息: %w", err)
	}
	return &m, nil
}

// RemoveMembersResult 批量移除的响应：移除成功返回 success，
// add_to_member_blacklist=true 时可能部分成员拉黑失败（openid 列表）。
type RemoveMembersResult struct {
	RemoveMembersResult             string   `json:"remove_members_result"` // 成功时为 "success"
	AddToMemberBlacklistFailOpenIDs []string `json:"add_to_member_blacklist_fail_openids"`
}

// RemoveGroupMembers 批量移除群成员（单次最多 20 人），addToBlacklist=true 同时拉入群黑名单。
// 白名单接口：非白名单机器人返回 11253。
func (b *Bot) RemoveGroupMembers(ctx context.Context, groupOpenID string, memberOpenIDs []string, addToBlacklist bool) (*RemoveMembersResult, error) {
	if len(memberOpenIDs) == 0 {
		return nil, fmt.Errorf("移除列表为空")
	}
	if len(memberOpenIDs) > 20 {
		return nil, fmt.Errorf("单次最多移除 20 人，当前 %d 人", len(memberOpenIDs))
	}
	body, err := b.qqAPI(ctx, http.MethodPost,
		"/v2/groups/"+url.PathEscape(groupOpenID)+"/batch_remove_members",
		struct {
			MemberOpenIDs        []string `json:"member_openids"`
			AddToMemberBlacklist bool     `json:"add_to_member_blacklist,omitempty"`
		}{MemberOpenIDs: memberOpenIDs, AddToMemberBlacklist: addToBlacklist})
	if err != nil {
		return nil, err
	}
	var res RemoveMembersResult
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("解析移除结果: %w", err)
	}
	return &res, nil
}

// 黑名单操作（SetGroupBlacklist 的 op）。
const (
	BlacklistOpAdd = "add" // 加入黑名单（目标须不在群中）
	BlacklistOpDel = "del" // 移出黑名单
)

// GroupBlacklistUser 群黑名单用户。
type GroupBlacklistUser struct {
	UnionOpenID  string `json:"union_openid"`
	MemberOpenID string `json:"member_openid"`
	Username     string `json:"username"`
	BannedAt     string `json:"banned_at"` // 拉黑时间（RFC3339）
	Bot          bool   `json:"bot"`
}

// ListGroupBlacklist 拉取群黑名单（自动翻页聚合）。白名单接口：非白名单机器人返回 11253。
func (b *Bot) ListGroupBlacklist(ctx context.Context, groupOpenID string) ([]GroupBlacklistUser, error) {
	var out []GroupBlacklistUser
	cursor := ""
	for i := 0; i < maxMemberPages; i++ {
		path := "/v2/groups/" + url.PathEscape(groupOpenID) + "/member_blacklist?limit=100"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		body, err := b.qqAPI(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Users      []GroupBlacklistUser `json:"users"`
			NextCursor string               `json:"next_cursor"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("解析群黑名单: %w", err)
		}
		out = append(out, page.Users...)
		if page.NextCursor == "" || len(page.Users) == 0 {
			return out, nil
		}
		cursor = page.NextCursor
	}
	return out, nil
}

// SetGroupBlacklist 群黑名单操作（add/del，单次最多 20 人），返回失败的 openid 列表。
// add 限制：目标用户不在群中才能加入（在群里的先移除）。白名单接口：非白名单机器人返回 11253。
func (b *Bot) SetGroupBlacklist(ctx context.Context, groupOpenID, op string, memberOpenIDs []string) ([]string, error) {
	if op != BlacklistOpAdd && op != BlacklistOpDel {
		return nil, fmt.Errorf("非法黑名单操作: %s", op)
	}
	if len(memberOpenIDs) == 0 {
		return nil, fmt.Errorf("openid 列表为空")
	}
	if len(memberOpenIDs) > 20 {
		return nil, fmt.Errorf("单次最多操作 20 人，当前 %d 人", len(memberOpenIDs))
	}
	body, err := b.qqAPI(ctx, http.MethodPost,
		"/v2/groups/"+url.PathEscape(groupOpenID)+"/member_blacklist",
		struct {
			Op            string   `json:"op"`
			MemberOpenIDs []string `json:"member_openids"`
		}{Op: op, MemberOpenIDs: memberOpenIDs})
	if err != nil {
		return nil, err
	}
	var res struct {
		FailOpenIDs []string `json:"fail_openids"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("解析黑名单操作结果: %w", err)
	}
	return res.FailOpenIDs, nil
}
