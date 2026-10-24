package dto

// 群（非频道）管理相关 dto：群信息、机器人群内状态、禁言、成员、黑名单、入群审批。
// 官方 2026-08/09 新增的群接口，上游 botgo v0.2.1 未收录，本地 fork 补充。
//
// ⚠️ 除注明外均为白名单机制：非白名单机器人返回错误码 11253（应用无接口访问权限，
// 需联系平台运营申请）。QPM 限制见各接口注释。

// GroupInfo 群基本信息（GET /v2/groups/{group_openid}/info，30 QPM，白名单）。
type GroupInfo struct {
	GroupOpenID     string   `json:"group_openid"`
	GroupName       string   `json:"group_name"`        // 群名称
	GroupFingerMemo string   `json:"group_finger_memo"` // 群简介
	GroupClassText  string   `json:"group_class_text"`  // 群分类
	GroupTags       []string `json:"group_tags"`        // 群标签列表
	GroupMemberNum  int      `json:"group_member_num"`  // 群成员人数
}

// GroupBotState 机器人在指定群内的状态（GET /v2/groups/{group_openid}/bot_state，30 QPM，白名单）。
type GroupBotState struct {
	MemberOpenID      string `json:"member_openid"`       // 机器人自己的 openid
	JoinedAt          string `json:"joined_at"`           // 入群时间（RFC3339）
	AllowProactiveMsg bool   `json:"allow_proactive_msg"` // 是否接收主动推送
	RecvMsgSetting    string `json:"recv_msg_setting"`    // all / only_mention / mention_and_context
	MemberRole        string `json:"member_role"`         // MemberRole 常量
}

// 禁言操作（MemberMuteState.Op）。
const (
	MuteOpAdd    = "add"    // 新增禁言
	MuteOpUpdate = "update" // 更新禁言到期时间
	MuteOpDel    = "del"    // 解除禁言
)

// MemberMuteState 是禁言设置项：Op 控制增/改/删，到期时间 RFC3339（del 时可空）。
// POST /v2/groups/{group_openid}/restrict_chat_setting 的 members 数组元素。
type MemberMuteState struct {
	Op           string `json:"op"`
	MemberOpenID string `json:"member_openid"`
	MuteExpireAt string `json:"mute_expire_at,omitempty"`
}

// SetGroupMuteReq 禁言设置接口请求体。60 QPM；机器人需是群管理员；
// 单次最多 10 人；禁言最长 30 天；只能操作普通成员（群主/管理员/机器人本身不行）。
type SetGroupMuteReq struct {
	Members []MemberMuteState `json:"members"`
}

// 全员禁言模式（GlobalMuteRule.Mode）。
const (
	MuteModeNone     = "none"     // 未开启
	MuteModeAlways   = "always"   // 始终禁言
	MuteModeSchedule = "schedule" // 定时或周期禁言
)

// MuteScheduleRule 定时全员禁言规则。
type MuteScheduleRule struct {
	TaskID  string `json:"task_id"`
	StartAt string `json:"start_at"` // 开始时间（RFC3339）
	EndAt   string `json:"end_at"`   // 结束时间（RFC3339）
	Enabled bool   `json:"enabled"`
}

// MuteRecurringRule 周期全员禁言规则。
type MuteRecurringRule struct {
	TaskID    string `json:"task_id"`
	Weekdays  []int  `json:"weekdays"`   // 生效星期 1~7（1=周一，7=周日）
	StartTime string `json:"start_time"` // HH:mm（北京时间）
	EndTime   string `json:"end_time"`   // HH:mm；小于 StartTime 表示跨天到次日
	Enabled   bool   `json:"enabled"`
}

// GlobalMuteRule 群级全员禁言配置。
type GlobalMuteRule struct {
	Mode           string              `json:"mode"` // MuteMode* 常量
	ScheduleRules  []MuteScheduleRule  `json:"schedule_rules"`
	RecurringRules []MuteRecurringRule `json:"recurring_rules"`
}

// MutedMember 查询禁言状态返回的单个被禁言成员（不含已过期记录）。
type MutedMember struct {
	MemberOpenID string `json:"member_openid"`
	MuteExpireAt string `json:"mute_expire_at"` // 到期时间（RFC3339）
	Username     string `json:"username"`       // 昵称
	UnionOpenID  string `json:"union_openid"`   // 统一标识（如有）
}

// GroupMuteState 查询群禁言状态（GET restrict_chat_setting，30 QPM）的响应。
type GroupMuteState struct {
	GlobalRule GlobalMuteRule `json:"global_rule"` // 全员禁言配置
	Members    []MutedMember  `json:"members"`     // 当前禁言中的成员列表
}

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

// GroupMemberPage 成员列表单页（GET /v2/groups/{group_openid}/members，
// 每页最多 30 条，cursor 分页，60 QPM，白名单）。
type GroupMemberPage struct {
	Members    []GroupMember `json:"members"`
	NextCursor string        `json:"next_cursor"` // 空 = 没有下一页
}

// BatchRemoveMembersReq 批量移除群成员请求（POST batch_remove_members，
// 单次最多 20 人，30 QPM，白名单）。
type BatchRemoveMembersReq struct {
	MemberOpenIDs        []string `json:"member_openids"`
	AddToMemberBlacklist bool     `json:"add_to_member_blacklist,omitempty"` // 同时拉入群黑名单
}

// RemoveMembersResult 批量移除的响应：移除成功返回 success，
// add_to_member_blacklist=true 时可能部分成员拉黑失败（openid 列表）。
type RemoveMembersResult struct {
	RemoveMembersResult             string   `json:"remove_members_result"` // 成功时为 "success"
	AddToMemberBlacklistFailOpenIDs []string `json:"add_to_member_blacklist_fail_openids"`
}

// 黑名单操作（SetBlacklistReq.Op）。
const (
	BlacklistOpAdd = "add" // 加入黑名单（目标须不在群中，在群里的先移除）
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

// GroupBlacklistPage 黑名单单页（GET member_blacklist，limit 默认 20 最大 100，
// 30 QPM，白名单）。
type GroupBlacklistPage struct {
	Users      []GroupBlacklistUser `json:"users"`
	NextCursor string               `json:"next_cursor"`
}

// SetBlacklistReq 黑名单操作请求（POST member_blacklist，op=add/del，
// 单次最多 20 人，60 QPM，白名单）。
type SetBlacklistReq struct {
	Op            string   `json:"op"`
	MemberOpenIDs []string `json:"member_openids"`
}

// BlacklistOpResult 黑名单操作响应。
type BlacklistOpResult struct {
	FailOpenIDs []string `json:"fail_openids"` // 操作失败的 openid 列表
}

// 入群审批动作（JoinRequestApproval.Op）。
const (
	JoinRequestApprove = "approve"
	JoinRequestDecline = "decline"
)

// JoinRequestApproval 审批接口请求体（POST approval_join_request/{member_openid}，
// 60 QPM，需机器人是群管理员）。
type JoinRequestApproval struct {
	Op            string `json:"op"`                        // JoinRequestApprove / JoinRequestDecline
	JoinRequestID string `json:"join_request_id,omitempty"` // 建议从事件回传（官方标注可选，回传可精确定位）
	RejectReason  string `json:"reject_reason,omitempty"`   // 仅 decline：给申请人看的拒绝理由
}

// GroupJoinRequest join_request_list 返回的申请项（字段同入群申请事件，但列表项
// 不带 group_openid——群已由请求路径指定）。GET join_request_list（分页），
// 30 QPM，需群管理员。
type GroupJoinRequest struct {
	JoinRequestID string                 `json:"join_request_id"`
	RiskTips      string                 `json:"risk_tips"` // 安全提示语（非空 = 命中风控）
	UnionOpenID   string                 `json:"union_openid"`
	MemberOpenID  string                 `json:"member_openid"`
	Username      string                 `json:"username"`
	ApplyAt       string                 `json:"apply_at"`     // 申请时间（RFC3339）
	ApplySource   string                 `json:"apply_source"` // self_apply 主动申请 / invited 被邀请
	InvitedBy     string                 `json:"invited_by"`   // 邀请人 openid（invited 时有效）
	Bot           bool                   `json:"bot"`
	VerifyInfo    *JoinRequestVerifyInfo `json:"verify_info"`
	AutoApproved  *JoinAutoApproved      `json:"auto_approved"` // 官方自动审批策略已通过时携带
}

// GroupJoinRequestPage 入群申请单页。
type GroupJoinRequestPage struct {
	List       []GroupJoinRequest `json:"list"`
	NextCursor string             `json:"next_cursor"`
}

// Answers 返回申请人提交的全部验证答案（验证消息算一条；问答每题一条）。
// 没有验证信息（如被邀请入群）返回 nil。
func (r *GroupJoinRequest) Answers() []string {
	if r.VerifyInfo == nil {
		return nil
	}
	switch r.VerifyInfo.Method {
	case "verify_message":
		if r.VerifyInfo.VerifyMessage != "" {
			return []string{r.VerifyInfo.VerifyMessage}
		}
		return nil
	case "admin_review_qa":
		var out []string
		for _, qa := range r.VerifyInfo.ReviewQAList {
			out = append(out, qa.Answer)
		}
		return out
	}
	return nil
}

// JoinAutoApproved 官方自动审批策略已通过的标记。
type JoinAutoApproved struct {
	StrategyID string `json:"strategy_id"`
}

// JoinRequestVerifyInfo 入群验证信息（事件与申请列表共用）。
type JoinRequestVerifyInfo struct {
	Method        string                `json:"method"` // verify_message 验证消息 / admin_review_qa 问答
	VerifyMessage string                `json:"verify_message"`
	ReviewQAList  []JoinRequestReviewQA `json:"review_qa_list"`
}

// JoinRequestReviewQA 一条入群问答（管理员设置的问题 + 申请人填的答案）。
type JoinRequestReviewQA struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// JoinApprovalStrategy 入群自动审批策略：把关联群中命中白名单 QQ 号的入群申请
// 自动审批通过。一个机器人最多 20 个策略；仅在机器人拥有关联群管理员身份时生效。
type JoinApprovalStrategy struct {
	StrategyID         string   `json:"strategy_id"`
	GroupOpenIDs       []string `json:"group_openids"`        // 关联群 openid（创建时用 group_openids 才返回）
	GroupIDs           []string `json:"group_ids"`            // 关联 QQ 群号（创建时用 group_ids 才返回）
	WhitelistUserCount int      `json:"whitelist_user_count"` // 白名单号码数量估算值（可能有少量误差）
	IsEnable           string   `json:"is_enable"`            // on 启用 / off 关闭
	ExpireAt           string   `json:"expire_at"`            // 过期时间（RFC3339）
	CreatedAt          string   `json:"created_at"`           // 创建时间（RFC3339）
	UpdatedAt          string   `json:"updated_at"`           // 最近更新（RFC3339）
	Remark             string   `json:"remark"`               // 备注
}

// JoinApprovalStrategyPage 自动审批策略单页（GET /v2/groups/join_approval_strategy，
// 机器人维度不带群 id，60 QPM）。
type JoinApprovalStrategyPage struct {
	Strategies []JoinApprovalStrategy `json:"strategies"`
	NextCursor string                 `json:"next_cursor"`
}
