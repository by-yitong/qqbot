package v1

import (
	"fmt"

	"qqbot/pkg/botgo/constant"
)

type uri string

// 目前提供的接口的 uri
const (
	guildURI            uri = "/guilds/{guild_id}"
	guildMembersURI     uri = "/guilds/{guild_id}/members"
	guildMemberURI      uri = "/guilds/{guild_id}/members/{user_id}"
	guildRoleMemberURI  uri = "/guilds/{guild_id}/roles/{role_id}/members"
	guildMuteURI        uri = "/guilds/{guild_id}/mute"                   // 频道禁言
	guildMembersMuteURI uri = "/guilds/{guild_id}/members/{user_id}/mute" // 频道指定成员禁言

	channelsURI uri = "/guilds/{guild_id}/channels"
	channelURI  uri = "/channels/{channel_id}"

	channelPermissionsURI      uri = "/channels/{channel_id}/members/{user_id}/permissions"
	channelRolesPermissionsURI uri = "/channels/{channel_id}/roles/{role_id}/permissions"

	messagesURI       uri = "/channels/{channel_id}/messages"
	groupMessagesURI  uri = "/v2/groups/{group_id}/messages"
	groupRichMediaURI uri = "/v2/groups/{group_id}/files"

	c2cMessagesURI  uri = "/v2/users/{user_id}/messages"
	c2cRichMediaURI uri = "/v2/users/{user_id}/files"

	retractC2cMessageURI   uri = "/v2/users/{user_id}/messages/{message_id}"
	retractGroupMessageURI uri = "/v2/groups/{group_id}/messages/{message_id}"

	// --- 群管理（本地 fork 补充：官方 2026-08/09 新增，上游 botgo v0.2.1 未收录）---
	groupInfoURI                uri = "/v2/groups/{group_openid}/info"                    // 群信息（30 QPM，白名单）
	groupBotStateURI                = "/v2/groups/{group_openid}/bot_state"               // 机器人群内状态（30 QPM，白名单）
	groupMuteURI                uri = "/v2/groups/{group_openid}/restrict_chat_setting"   // 禁言设置/查询
	groupMembersURI                 = "/v2/groups/{group_openid}/members"                 // 成员列表（cursor 分页，白名单）
	groupMemberURI                  = "/v2/groups/{group_openid}/members/{member_openid}" // 成员详情（白名单）
	groupBatchRemoveMembersURI      = "/v2/groups/{group_openid}/batch_remove_members"
	groupBlacklistURI               = "/v2/groups/{group_openid}/member_blacklist" // 黑名单查询/操作
	groupApprovalJoinRequestURI     = "/v2/groups/{group_openid}/approval_join_request/{member_openid}"
	groupJoinRequestListURI         = "/v2/groups/{group_openid}/join_request_list"
	joinApprovalStrategyURI         = "/v2/groups/join_approval_strategy" // 机器人维度，不带群 id

	// --- 自定义菜单与指令面板（本地 fork 补充）---
	menuURI   uri = "/v2/menu"
	panelsURI     = "/v2/panels"
	panelURI      = "/v2/panels/{panel_id}"

	// --- 富媒体上传（本地 fork 补充：分片上传官方 chunked-upload 流程）。占位名
	// {openid} 与既有消息端点的 {group_id}/{user_id} 不同，见 richmedia.go 的端点表。---
	groupUploadPrepareURI    uri = "/v2/groups/{openid}/upload_prepare"
	groupUploadPartFinishURI uri = "/v2/groups/{openid}/upload_part_finish"
	userUploadPrepareURI     uri = "/v2/users/{openid}/upload_prepare"
	userUploadPartFinishURI  uri = "/v2/users/{openid}/upload_part_finish"

	messageURI uri = "/channels/{channel_id}/messages/{message_id}"

	userMeURI       uri = "/users/@me"
	userMeGuildsURI uri = "/users/@me/guilds"
	userMeDMURI     uri = "/users/@me/dms"

	gatewayURI    uri = "/gateway" // nolint
	gatewayBotURI uri = "/gateway/bot"

	audioControlURI uri = "/channels/{channel_id}/audio"
	micURI          uri = "/channels/{channel_id}/mic"

	rolesURI uri = "/guilds/{guild_id}/roles"
	roleURI  uri = "/guilds/{guild_id}/roles/{role_id}"

	memberRoleURI uri = "/guilds/{guild_id}/members/{user_id}/roles/{role_id}"

	dmsURI        uri = "/dms/{guild_id}/messages"
	dmsMessageURI uri = "/dms/{guild_id}/messages/{message_id}"

	channelAnnouncesURI = "/channels/{channel_id}/announces"
	channelAnnounceURI  = "/channels/{channel_id}/announces/{message_id}"

	guildAnnouncesURI = "/guilds/{guild_id}/announces"
	guildAnnounceURI  = "/guilds/{guild_id}/announces/{message_id}"

	schedulesURI uri = "/channels/{channel_id}/schedules"
	scheduleURI  uri = "/channels/{channel_id}/schedules/{schedule_id}"

	apiPermissionURI       uri = "/guilds/{guild_id}/api_permission"
	apiPermissionDemandURI uri = "/guilds/{guild_id}/api_permission/demand"

	pinsURI = "/channels/{channel_id}/pins"
	pinURI  = "/channels/{channel_id}/pins/{message_id}"

	messageReactionURI uri = "/channels/{channel_id}/messages/{message_id}/reactions/{emoji_type}/{emoji_id}"

	interactionsURI = "/interactions/{interaction_id}"

	httpSessionsURI uri = "/gateway/webhook/sessions"
	httpSessionURI  uri = "/gateway/webhook/sessions/{session_id}"

	messageSettingURI uri = "/guilds/{guild_id}/message/setting"

	voiceChannelMembersURI uri = "/channels/{channel_id}/voice/members"

	settingGuideURI   uri = "/channels/{channel_id}/settingguide"
	dmSettingGuideURI uri = "/dms/{guild_id}/settingguide"
)

// getURL 获取接口地址，会处理沙箱环境判断
func (o *openAPI) getURL(endpoint uri) string {
	d := constant.APIDomain
	if o.sandbox {
		d = constant.SandBoxAPIDomain
	}
	return fmt.Sprintf("%s%s", d, endpoint)
}
