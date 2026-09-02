package bot

// 群成员/入群申请事件（官方 2026-08 新增，botgo v0.2.1 未覆盖，走通用 dispatch 注册）。
//
//   - GROUP_MEMBER_ADD（intent GROUP_MEMBER_EVENT = 1<<24）：有新成员加入群聊。
//     事件没有昵称字段（只有 openid），昵称可从 GROUP_JOIN_REQUEST 事件（在审批前推送）缓存。
//   - GROUP_JOIN_REQUEST（intent GROUP_AND_C2C_EVENT = 1<<25，与群消息同位，无需额外订阅）：
//     用户申请加群。**只有机器人是群管理员时才能收到**。verify_info 里带验证消息或问答答案，
//     答案正确与否由调用方判断，通过 ApproveGroupJoinRequest 放行/拒绝。
//
// 文档：https://bot.q.qq.com/wiki/develop/api-v2/autogen/event/group_member_add.html
//       https://bot.q.qq.com/wiki/develop/api-v2/autogen/event/group_join_request.html

import (
	"log"

	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/event"
)

// IntentGroupMemberEvent 是群成员事件（GROUP_MEMBER_ADD / GROUP_MEMBER_REMOVE）的 intent 位。
// botgo v0.2.1 没有这个常量（官方 2026-08 新增），注册了群成员监听器时由 Run 补进 intent。
const IntentGroupMemberEvent dto.Intent = 1 << 24

// GroupMemberAddEvent 是 GROUP_MEMBER_ADD 的事件体。
type GroupMemberAddEvent struct {
	Timestamp    int64  `json:"timestamp"`     // 事件时间戳（Unix 秒）
	GroupOpenID  string `json:"group_openid"`  // 群 openid
	MemberOpenID string `json:"member_openid"` // 新加入成员的 openid
	UserOpenID   string `json:"user_openid"`   // 新成员的用户 openid（跨应用统一标识，可能为空）
}

// GroupJoinRequestEvent 是 GROUP_JOIN_REQUEST 的事件体；结构与
// ListGroupJoinRequests 返回的申请项一致（列表项缺 group_openid，事件里有）。
type GroupJoinRequestEvent struct {
	GroupOpenID   string                 `json:"group_openid"`
	JoinRequestID string                 `json:"join_request_id"` // 审批接口回传用
	RiskTips      string                 `json:"risk_tips"`       // 安全提示语（非空 = 命中风控，建议人工处理）
	UnionOpenID   string                 `json:"union_openid"`
	MemberOpenID  string                 `json:"member_openid"` // 申请人 openid
	Username      string                 `json:"username"`      // 申请人昵称
	ApplyAt       string                 `json:"apply_at"`      // 申请时间（RFC3339）
	ApplySource   string                 `json:"apply_source"`  // self_apply 主动申请 / invited 被邀请
	InvitedBy     string                 `json:"invited_by"`    // 邀请人 openid（invited 时有效）
	Bot           bool                   `json:"bot"`           // 是否机器人账号
	VerifyInfo    *JoinRequestVerifyInfo `json:"verify_info"`
	AutoApproved  *JoinAutoApproved      `json:"auto_approved"` // 官方自动审批策略已通过时携带
}

// JoinAutoApproved 是官方自动审批通过的扩展信息。
type JoinAutoApproved struct {
	StrategyID string `json:"strategy_id"`
}

// JoinRequestVerifyInfo 是入群验证信息。
type JoinRequestVerifyInfo struct {
	Method        string                `json:"method"` // verify_message 验证消息 / admin_review_qa 问答
	VerifyMessage string                `json:"verify_message"`
	ReviewQAList  []JoinRequestReviewQA `json:"review_qa_list"`
}

// JoinRequestReviewQA 是一条入群问答（管理员设置的问题 + 申请人填的答案）。
type JoinRequestReviewQA struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// Answers 返回申请人提交的全部验证答案（验证消息算一条；问答每题一条）。
// 没有验证信息（如被邀请入群）返回 nil。
func (e *GroupJoinRequestEvent) Answers() []string {
	if e.VerifyInfo == nil {
		return nil
	}
	switch e.VerifyInfo.Method {
	case "verify_message":
		if e.VerifyInfo.VerifyMessage != "" {
			return []string{e.VerifyInfo.VerifyMessage}
		}
		return nil
	case "admin_review_qa":
		var out []string
		for _, qa := range e.VerifyInfo.ReviewQAList {
			out = append(out, qa.Answer)
		}
		return out
	}
	return nil
}

// --- 监听器注册 ---

// GroupMemberAddHandler 处理一次群成员加入事件。
type GroupMemberAddHandler func(e *GroupMemberAddEvent)

// GroupJoinRequestHandler 处理一次用户申请加群事件。
type GroupJoinRequestHandler func(e *GroupJoinRequestEvent)

// OnGroupMemberAdd 注册群成员加入监听器。注册后 Run 会自动补 GROUP_MEMBER_EVENT intent。
func (b *Bot) OnGroupMemberAdd(h GroupMemberAddHandler) {
	b.mu.Lock()
	b.memberAddHandlers = append(b.memberAddHandlers, h)
	b.mu.Unlock()
}

// OnGroupJoinRequest 注册用户申请加群监听器（intent 与群消息同位，已包含）。
func (b *Bot) OnGroupJoinRequest(h GroupJoinRequestHandler) {
	b.mu.Lock()
	b.joinReqHandlers = append(b.joinReqHandlers, h)
	b.mu.Unlock()
}

// wantGroupMemberEvent 报告是否有群成员事件监听器（决定 Run 是否补 1<<24 intent，
// 避免没开通该权限的机器人因申请未授权 intent 而连不上 WSS）。
func (b *Bot) wantGroupMemberEvent() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.memberAddHandlers) > 0
}

// --- botgo 事件入口（Run 里经 RegisterDispatchEventHandler 注册）---

// onGroupMemberAdd 处理 GROUP_MEMBER_ADD：解析事件体后异步触发监听器
// （欢迎消息是主动 HTTP 发送，不能堵 botgo 的串行分发循环）。
func (b *Bot) onGroupMemberAdd(_ *dto.WSPayload, msg []byte) error {
	var e GroupMemberAddEvent
	if err := event.ParseData(msg, &e); err != nil {
		return err
	}
	go b.fireGroupMemberAdd(&e)
	return nil
}

// onGroupJoinRequest 处理 GROUP_JOIN_REQUEST：解析后异步触发监听器
// （审批是 HTTP 调用，同上不堵分发循环）。
func (b *Bot) onGroupJoinRequest(_ *dto.WSPayload, msg []byte) error {
	var e GroupJoinRequestEvent
	if err := event.ParseData(msg, &e); err != nil {
		return err
	}
	go b.fireGroupJoinRequest(&e)
	return nil
}

// fireGroupMemberAdd 同步触发所有群成员加入监听器（单个 panic 不影响其他）。
func (b *Bot) fireGroupMemberAdd(e *GroupMemberAddEvent) {
	b.mu.RLock()
	hs := append([]GroupMemberAddHandler(nil), b.memberAddHandlers...)
	b.mu.RUnlock()
	for _, h := range hs {
		func(h GroupMemberAddHandler) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("bot: GroupMemberAdd 监听器 panic: %v", r)
				}
			}()
			h(e)
		}(h)
	}
}

// fireGroupJoinRequest 同步触发所有入群申请监听器（单个 panic 不影响其他）。
func (b *Bot) fireGroupJoinRequest(e *GroupJoinRequestEvent) {
	b.mu.RLock()
	hs := append([]GroupJoinRequestHandler(nil), b.joinReqHandlers...)
	b.mu.RUnlock()
	for _, h := range hs {
		func(h GroupJoinRequestHandler) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("bot: GroupJoinRequest 监听器 panic: %v", r)
				}
			}()
			h(e)
		}(h)
	}
}
