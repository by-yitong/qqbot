package bot

// 入群申请管理（协议实现在 pkg/botgo，本层只做连接保护与类型别名）。
//
//   - 审批：POST /v2/groups/{group_openid}/approval_join_request/{member_openid}
//     op=approve 通过 / decline 拒绝；join_request_id 从事件回传。60 QPM，需机器人是群管理员。
//   - 列表：GET /v2/groups/{group_openid}/join_request_list（分页）。30 QPM，需群管理员。
//   - 自动审批策略列表：GET /v2/groups/join_approval_strategy（分页，不带群 id，机器人维度）。60 QPM。

import (
	"context"

	"qqbot/pkg/botgo/dto"
)

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
