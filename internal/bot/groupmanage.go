package bot

// 入群申请管理接口（官方 2026-08-10 新增，botgo v0.2.1 未覆盖，经 qqAPI 自行 HTTP 调用）。
//
//   - 审批：POST /v2/groups/{group_openid}/approval_join_request/{member_openid}
//     op=approve 通过 / decline 拒绝；join_request_id 从事件回传。60 QPM，需机器人是群管理员。
//   - 列表：GET /v2/groups/{group_openid}/join_request_list（分页）。30 QPM，需群管理员。
//
// 文档：https://bot.q.qq.com/wiki/develop/api-v2/autogen/api/v2_groups_group_openid_approval_join_request_member_openid.post.html

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// JoinReviewOpApprove / JoinReviewOpDecline 是审批动作。
const (
	JoinReviewOpApprove = "approve"
	JoinReviewOpDecline = "decline"
)

// joinReviewReq 是审批接口的请求体。
type joinReviewReq struct {
	Op            string `json:"op"`
	JoinRequestID string `json:"join_request_id,omitempty"`
	RejectReason  string `json:"reject_reason,omitempty"` // 仅 decline
}

// ApproveGroupJoinRequest 审批一条入群申请（通过或拒绝）。
// joinRequestID 建议从事件带回（官方标注可选，回传可精确定位申请）；
// decline 时可带 rejectReason（给申请人看的拒绝理由）。需机器人是群管理员。
func (b *Bot) ApproveGroupJoinRequest(ctx context.Context, groupOpenID, memberOpenID, joinRequestID, op, rejectReason string) error {
	if op != JoinReviewOpApprove && op != JoinReviewOpDecline {
		return fmt.Errorf("非法审批动作: %s", op)
	}
	_, err := b.qqAPI(ctx, http.MethodPost,
		"/v2/groups/"+url.PathEscape(groupOpenID)+"/approval_join_request/"+url.PathEscape(memberOpenID),
		joinReviewReq{Op: op, JoinRequestID: joinRequestID, RejectReason: rejectReason})
	return err
}

// GroupJoinRequestItem 是 join_request_list 返回的申请项（字段同 GroupJoinRequestEvent，
// 但列表项不带 group_openid——群已由请求路径指定）。
type GroupJoinRequestItem = GroupJoinRequestEvent

// ListGroupJoinRequests 拉取一个群的待处理入群申请列表（自动翻页聚合）。
// 需机器人是群管理员。
func (b *Bot) ListGroupJoinRequests(ctx context.Context, groupOpenID string) ([]GroupJoinRequestItem, error) {
	var out []GroupJoinRequestItem
	cursor := ""
	for {
		path := "/v2/groups/" + url.PathEscape(groupOpenID) + "/join_request_list?limit=50"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		body, err := b.qqAPI(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			List       []GroupJoinRequestItem `json:"list"`
			NextCursor string                 `json:"next_cursor"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("解析入群申请列表: %w", err)
		}
		out = append(out, page.List...)
		if page.NextCursor == "" || len(page.List) == 0 {
			return out, nil
		}
		cursor = page.NextCursor
	}
}
