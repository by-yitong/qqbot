package v1

// 群（非频道）管理接口（本地 fork 补充：官方 2026-08/09 新增，上游 botgo v0.2.1
// 未收录）。含群信息、机器人群内状态、禁言、成员、黑名单、入群审批。
//
// ⚠️ 除注明外均为白名单机制：非白名单机器人返回错误码 11253。带 next_cursor 的
// 列表接口在此自动翻页聚合（上限 maxGroupListPages 页，防服务端异常死循环）；
// 需要单页控制的用 xxxPage 方法。

import (
	"context"
	"errors"
	"fmt"

	"qqbot/pkg/botgo/dto"
)

// maxGroupListPages 列表聚合翻页安全上限：成员每页 30、黑名单每页 100、申请每页 50，
// 100 页分别够 3000/10000/5000 条。
const maxGroupListPages = 100

// GetGroupInfo 获取群基本信息（名称/简介/分类/标签/人数）。30 QPM，白名单接口。
func (o *openAPI) GetGroupInfo(ctx context.Context, groupOpenID string) (*dto.GroupInfo, error) {
	rsp, err := o.request(ctx).
		SetPathParam("group_openid", groupOpenID).
		SetResult(dto.GroupInfo{}).
		Get(o.getURL(groupInfoURI))
	if err != nil {
		return nil, err
	}
	return rsp.Result().(*dto.GroupInfo), nil
}

// GetGroupBotState 获取机器人在指定群内的状态（角色/入群时间/主动推送与收消息设置）。
// 30 QPM，白名单接口。
func (o *openAPI) GetGroupBotState(ctx context.Context, groupOpenID string) (*dto.GroupBotState, error) {
	rsp, err := o.request(ctx).
		SetPathParam("group_openid", groupOpenID).
		SetResult(dto.GroupBotState{}).
		Get(o.getURL(groupBotStateURI))
	if err != nil {
		return nil, err
	}
	return rsp.Result().(*dto.GroupBotState), nil
}

// --- 禁言 ---

// SetGroupMemberMutes 批量设置群成员禁言（增/改/删）。60 QPM；机器人需是群管理员；
// 单次最多 10 人；只能操作普通成员（群主/管理员/机器人本身不行）。
func (o *openAPI) SetGroupMemberMutes(ctx context.Context, groupOpenID string, members []dto.MemberMuteState) error {
	if len(members) == 0 {
		return errors.New("禁言列表为空")
	}
	if len(members) > 10 {
		return fmt.Errorf("单次最多设置 10 人，当前 %d 人", len(members))
	}
	_, err := o.request(ctx).
		SetPathParam("group_openid", groupOpenID).
		SetBody(dto.SetGroupMuteReq{Members: members}).
		Post(o.getURL(groupMuteURI))
	return err
}

// GetGroupMuteState 查询群禁言状态（全员禁言规则 + 禁言中成员名单，不含已过期）。
// 30 QPM；机器人需是群管理员。
func (o *openAPI) GetGroupMuteState(ctx context.Context, groupOpenID string) (*dto.GroupMuteState, error) {
	rsp, err := o.request(ctx).
		SetPathParam("group_openid", groupOpenID).
		SetResult(dto.GroupMuteState{}).
		Get(o.getURL(groupMuteURI))
	if err != nil {
		return nil, err
	}
	return rsp.Result().(*dto.GroupMuteState), nil
}

// --- 成员 ---

// GetGroupMemberPage 拉取群成员列表单页（每页最多 30 条）。60 QPM，白名单接口。
// cursor 传空取第一页；NextCursor 为空表示末页。
func (o *openAPI) GetGroupMemberPage(ctx context.Context, groupOpenID, cursor string) (*dto.GroupMemberPage, error) {
	req := o.request(ctx).
		SetPathParam("group_openid", groupOpenID).
		SetResult(dto.GroupMemberPage{})
	if cursor != "" {
		req.SetQueryParam("cursor", cursor)
	}
	rsp, err := req.Get(o.getURL(groupMembersURI))
	if err != nil {
		return nil, err
	}
	return rsp.Result().(*dto.GroupMemberPage), nil
}

// ListGroupMembers 拉取群成员列表（自动翻页聚合）。白名单接口：非白名单机器人返回 11253。
func (o *openAPI) ListGroupMembers(ctx context.Context, groupOpenID string) ([]dto.GroupMember, error) {
	var out []dto.GroupMember
	cursor := ""
	for i := 0; i < maxGroupListPages; i++ {
		page, err := o.GetGroupMemberPage(ctx, groupOpenID, cursor)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Members...)
		if page.NextCursor == "" || len(page.Members) == 0 {
			return out, nil
		}
		cursor = page.NextCursor
	}
	return out, nil
}

// GetGroupMember 获取指定群成员的详细信息。30 QPM，白名单接口。
func (o *openAPI) GetGroupMember(ctx context.Context, groupOpenID, memberOpenID string) (*dto.GroupMember, error) {
	rsp, err := o.request(ctx).
		SetPathParam("group_openid", groupOpenID).
		SetPathParam("member_openid", memberOpenID).
		SetResult(dto.GroupMember{}).
		Get(o.getURL(groupMemberURI))
	if err != nil {
		return nil, err
	}
	return rsp.Result().(*dto.GroupMember), nil
}

// BatchRemoveGroupMembers 批量移除群成员（单次最多 20 人），addToBlacklist=true
// 同时拉入群黑名单。30 QPM，白名单接口。
func (o *openAPI) BatchRemoveGroupMembers(ctx context.Context, groupOpenID string, memberOpenIDs []string, addToBlacklist bool) (*dto.RemoveMembersResult, error) {
	if len(memberOpenIDs) == 0 {
		return nil, errors.New("移除列表为空")
	}
	if len(memberOpenIDs) > 20 {
		return nil, fmt.Errorf("单次最多移除 20 人，当前 %d 人", len(memberOpenIDs))
	}
	rsp, err := o.request(ctx).
		SetPathParam("group_openid", groupOpenID).
		SetBody(dto.BatchRemoveMembersReq{
			MemberOpenIDs:        memberOpenIDs,
			AddToMemberBlacklist: addToBlacklist,
		}).
		SetResult(dto.RemoveMembersResult{}).
		Post(o.getURL(groupBatchRemoveMembersURI))
	if err != nil {
		return nil, err
	}
	return rsp.Result().(*dto.RemoveMembersResult), nil
}

// --- 黑名单 ---

// GetGroupBlacklistPage 拉取群黑名单单页（limit 固定 100）。30 QPM，白名单接口。
func (o *openAPI) GetGroupBlacklistPage(ctx context.Context, groupOpenID, cursor string) (*dto.GroupBlacklistPage, error) {
	req := o.request(ctx).
		SetPathParam("group_openid", groupOpenID).
		SetQueryParam("limit", "100").
		SetResult(dto.GroupBlacklistPage{})
	if cursor != "" {
		req.SetQueryParam("cursor", cursor)
	}
	rsp, err := req.Get(o.getURL(groupBlacklistURI))
	if err != nil {
		return nil, err
	}
	return rsp.Result().(*dto.GroupBlacklistPage), nil
}

// ListGroupBlacklist 拉取群黑名单（自动翻页聚合）。白名单接口：非白名单机器人返回 11253。
func (o *openAPI) ListGroupBlacklist(ctx context.Context, groupOpenID string) ([]dto.GroupBlacklistUser, error) {
	var out []dto.GroupBlacklistUser
	cursor := ""
	for i := 0; i < maxGroupListPages; i++ {
		page, err := o.GetGroupBlacklistPage(ctx, groupOpenID, cursor)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Users...)
		if page.NextCursor == "" || len(page.Users) == 0 {
			return out, nil
		}
		cursor = page.NextCursor
	}
	return out, nil
}

// SetGroupBlacklist 群黑名单操作（op=add/del，单次最多 20 人），返回失败的 openid 列表。
// add 限制：目标用户不在群中才能加入（在群里的先移除）。60 QPM，白名单接口。
func (o *openAPI) SetGroupBlacklist(ctx context.Context, groupOpenID, op string, memberOpenIDs []string) ([]string, error) {
	if op != dto.BlacklistOpAdd && op != dto.BlacklistOpDel {
		return nil, fmt.Errorf("非法黑名单操作: %s", op)
	}
	if len(memberOpenIDs) == 0 {
		return nil, errors.New("openid 列表为空")
	}
	if len(memberOpenIDs) > 20 {
		return nil, fmt.Errorf("单次最多操作 20 人，当前 %d 人", len(memberOpenIDs))
	}
	rsp, err := o.request(ctx).
		SetPathParam("group_openid", groupOpenID).
		SetBody(dto.SetBlacklistReq{Op: op, MemberOpenIDs: memberOpenIDs}).
		SetResult(dto.BlacklistOpResult{}).
		Post(o.getURL(groupBlacklistURI))
	if err != nil {
		return nil, err
	}
	return rsp.Result().(*dto.BlacklistOpResult).FailOpenIDs, nil
}

// --- 入群审批 ---

// ApproveGroupJoinRequest 审批一条入群申请（通过或拒绝）。
// joinRequestID 建议从事件带回（官方标注可选，回传可精确定位申请）；
// decline 时可带 rejectReason。60 QPM，需机器人是群管理员。
func (o *openAPI) ApproveGroupJoinRequest(ctx context.Context, groupOpenID, memberOpenID string, approval dto.JoinRequestApproval) error {
	if approval.Op != dto.JoinRequestApprove && approval.Op != dto.JoinRequestDecline {
		return fmt.Errorf("非法审批动作: %s", approval.Op)
	}
	_, err := o.request(ctx).
		SetPathParam("group_openid", groupOpenID).
		SetPathParam("member_openid", memberOpenID).
		SetBody(approval).
		Post(o.getURL(groupApprovalJoinRequestURI))
	return err
}

// GetGroupJoinRequestPage 拉取一个群的待处理入群申请单页（limit 固定 50）。
// 30 QPM，需群管理员。
func (o *openAPI) GetGroupJoinRequestPage(ctx context.Context, groupOpenID, cursor string) (*dto.GroupJoinRequestPage, error) {
	req := o.request(ctx).
		SetPathParam("group_openid", groupOpenID).
		SetQueryParam("limit", "50").
		SetResult(dto.GroupJoinRequestPage{})
	if cursor != "" {
		req.SetQueryParam("cursor", cursor)
	}
	rsp, err := req.Get(o.getURL(groupJoinRequestListURI))
	if err != nil {
		return nil, err
	}
	return rsp.Result().(*dto.GroupJoinRequestPage), nil
}

// ListGroupJoinRequests 拉取一个群的待处理入群申请列表（自动翻页聚合）。需群管理员。
func (o *openAPI) ListGroupJoinRequests(ctx context.Context, groupOpenID string) ([]dto.GroupJoinRequest, error) {
	var out []dto.GroupJoinRequest
	cursor := ""
	for i := 0; i < maxGroupListPages; i++ {
		page, err := o.GetGroupJoinRequestPage(ctx, groupOpenID, cursor)
		if err != nil {
			return nil, err
		}
		out = append(out, page.List...)
		if page.NextCursor == "" || len(page.List) == 0 {
			return out, nil
		}
		cursor = page.NextCursor
	}
	return out, nil
}

// ListJoinApprovalStrategies 拉取入群自动审批策略列表（自动翻页聚合）。
// 路径不带群 id——策略是机器人维度的，一个策略可关联多个群。60 QPM。
func (o *openAPI) ListJoinApprovalStrategies(ctx context.Context) ([]dto.JoinApprovalStrategy, error) {
	var out []dto.JoinApprovalStrategy
	cursor := ""
	for i := 0; i < maxGroupListPages; i++ {
		req := o.request(ctx).
			SetQueryParam("limit", "50").
			SetResult(dto.JoinApprovalStrategyPage{})
		if cursor != "" {
			req.SetQueryParam("cursor", cursor)
		}
		rsp, err := req.Get(o.getURL(joinApprovalStrategyURI))
		if err != nil {
			return nil, err
		}
		page := rsp.Result().(*dto.JoinApprovalStrategyPage)
		out = append(out, page.Strategies...)
		if page.NextCursor == "" || len(page.Strategies) == 0 {
			return out, nil
		}
		cursor = page.NextCursor
	}
	return out, nil
}
