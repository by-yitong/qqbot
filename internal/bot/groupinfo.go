package bot

// 群信息与机器人群内状态接口（官方 2026-08 新增，botgo v0.2.1 未覆盖，经 qqAPI 自行 HTTP 调用）。
//
//   - 群信息：GET /v2/groups/{group_openid}/info（30 QPM）
//   - 机器人状态：GET /v2/groups/{group_openid}/bot_state（30 QPM）
//
// ⚠️ 两接口均为白名单机制：非白名单机器人返回错误码 11253（应用无接口访问权限，
// 需联系平台运营申请）。
//
// 文档：https://bot.q.qq.com/wiki/develop/api-v2/autogen/api/v2_groups_group_openid_info.get.html
//       https://bot.q.qq.com/wiki/develop/api-v2/autogen/api/v2_groups_group_openid_bot_state.get.html

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// GroupInfo 是群基本信息。
type GroupInfo struct {
	GroupOpenID     string   `json:"group_openid"`
	GroupName       string   `json:"group_name"`        // 群名称
	GroupFingerMemo string   `json:"group_finger_memo"` // 群简介
	GroupClassText  string   `json:"group_class_text"`  // 群分类
	GroupTags       []string `json:"group_tags"`        // 群标签列表
	GroupMemberNum  int      `json:"group_member_num"`  // 群成员人数
}

// GetGroupInfo 获取群基本信息（名称/简介/分类/标签/人数）。
// 白名单接口：非白名单机器人返回 11253。
func (b *Bot) GetGroupInfo(ctx context.Context, groupOpenID string) (*GroupInfo, error) {
	body, err := b.qqAPI(ctx, http.MethodGet, "/v2/groups/"+url.PathEscape(groupOpenID)+"/info", nil)
	if err != nil {
		return nil, err
	}
	var gi GroupInfo
	if err := json.Unmarshal(body, &gi); err != nil {
		return nil, fmt.Errorf("解析群信息: %w", err)
	}
	return &gi, nil
}

// GroupBotState 是机器人在指定群内的状态。
type GroupBotState struct {
	MemberOpenID      string `json:"member_openid"`       // 机器人自己的 openid
	JoinedAt          string `json:"joined_at"`           // 入群时间（RFC3339）
	AllowProactiveMsg bool   `json:"allow_proactive_msg"` // 是否接收主动推送
	RecvMsgSetting    string `json:"recv_msg_setting"`    // all / only_mention / mention_and_context
	MemberRole        string `json:"member_role"`         // member 普通成员 / owner 群主 / admin 管理员
}

// GetGroupBotState 获取机器人在指定群内的状态（角色/入群时间/主动推送与收消息设置）。
// 白名单接口：非白名单机器人返回 11253。
func (b *Bot) GetGroupBotState(ctx context.Context, groupOpenID string) (*GroupBotState, error) {
	body, err := b.qqAPI(ctx, http.MethodGet, "/v2/groups/"+url.PathEscape(groupOpenID)+"/bot_state", nil)
	if err != nil {
		return nil, err
	}
	var st GroupBotState
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, fmt.Errorf("解析机器人群内状态: %w", err)
	}
	return &st, nil
}
