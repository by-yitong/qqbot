package bot

// 群成员禁言接口（官方 2026-08 新增，botgo v0.2.1 未覆盖，经 qqAPI 自行 HTTP 调用）。
//
//	POST /v2/groups/{group_openid}/restrict_chat_setting
//	body: {"members":[{"op":"add|update|del","member_openid":"...","mute_expire_at":"RFC3339"}]}
//
// 限制：60 QPM；机器人需是群管理员；单次最多 10 人；禁言最长 30 天；
// 只能操作普通成员（群主/管理员/机器人本身不行）。
//
// 文档：https://bot.q.qq.com/wiki/develop/api-v2/autogen/api/v2_groups_group_openid_restrict_chat_setting.post.html

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// 禁言操作（members[].op）。
const (
	MuteOpAdd    = "add"    // 新增禁言
	MuteOpUpdate = "update" // 更新禁言到期时间
	MuteOpDel    = "del"    // 解除禁言
)

// MuteMaxDuration 单次禁言时长上限（官方限制 30 天）。
const MuteMaxDuration = 30 * 24 * time.Hour

// MemberMuteState 是禁言设置项：op 控制增/改/删，到期时间 RFC3339（del 时可空）。
type MemberMuteState struct {
	Op           string `json:"op"`
	MemberOpenID string `json:"member_openid"`
	MuteExpireAt string `json:"mute_expire_at,omitempty"`
}

// SetGroupMemberMutes 批量设置群成员禁言（增/改/删，单次最多 10 人）。
// 需机器人是群管理员；只能操作普通成员。
func (b *Bot) SetGroupMemberMutes(ctx context.Context, groupOpenID string, states []MemberMuteState) error {
	if len(states) == 0 {
		return fmt.Errorf("禁言列表为空")
	}
	if len(states) > 10 {
		return fmt.Errorf("单次最多设置 10 人，当前 %d 人", len(states))
	}
	_, err := b.qqAPI(ctx, http.MethodPost,
		"/v2/groups/"+url.PathEscape(groupOpenID)+"/restrict_chat_setting",
		struct {
			Members []MemberMuteState `json:"members"`
		}{Members: states})
	return err
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

// --- 查询群禁言状态（GET /v2/groups/{group_openid}/restrict_chat_setting，30 QPM）---

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
// 与设置接口的请求项 MemberMuteState 字段不同：多了昵称/union_openid，没有 op。
type MutedMember struct {
	MemberOpenID string `json:"member_openid"`
	MuteExpireAt string `json:"mute_expire_at"` // 到期时间（RFC3339）
	Username     string `json:"username"`       // 昵称
	UnionOpenID  string `json:"union_openid"`   // 统一标识（如有）
}

// GroupMuteState 查询群禁言状态的响应。
type GroupMuteState struct {
	GlobalRule GlobalMuteRule `json:"global_rule"` // 全员禁言配置
	Members    []MutedMember  `json:"members"`     // 当前禁言中的成员列表
}

// GetGroupMuteState 查询群禁言状态（全员禁言规则 + 禁言中成员名单，不含已过期）。
// 需机器人是群管理员。
func (b *Bot) GetGroupMuteState(ctx context.Context, groupOpenID string) (*GroupMuteState, error) {
	body, err := b.qqAPI(ctx, http.MethodGet, "/v2/groups/"+url.PathEscape(groupOpenID)+"/restrict_chat_setting", nil)
	if err != nil {
		return nil, err
	}
	var st GroupMuteState
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, fmt.Errorf("解析群禁言状态: %w", err)
	}
	return &st, nil
}
