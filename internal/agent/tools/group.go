package tools

// 群管 agent 工具：把官方 2026-09 群成员/黑名单接口开放给 AI 兜底
// （与 /成员 /踢 等命令同一批接口，均为内邀白名单，未接入的机器人调用报 11253）。
//
// 权限模型（发起人身份经 GroupActor 随 ctx 注入，见 plugins/agent.go）：
//   - 查询（group_members、黑名单 list）：所有群成员；
//   - 踢人（kick_group_member）：群主/管理员/主人直接执行；普通成员举报恶意内容
//     （色情/诈骗/刷屏广告等）时须携带 evidence=对方消息的原文片段，工具到聊天
//     记忆库核实「对方近 24h 确实发过这段内容」才执行——防诬告，也防 AI 幻觉
//     （引用消息事件不带作者 openid，内容与人的绑定只能靠记录库核实）；
//   - 黑名单增删：仅群主/管理员/主人。
//
// 目标校验：群主/管理员/机器人一律拒绝（官方接口也不允许移除群主）。

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"qqbot/internal/bot"
	"qqbot/internal/memory"

	"github.com/tidwall/gjson"
)

// GroupActor 是本次 AI Run 的发起人身份（群管工具的权限门禁依据）。
// 由 plugins/agent.go 在群聊 Run 前注入；私聊/未注入时群管操作类工具一律拒绝。
type GroupActor struct {
	UserID     string // 发起人 openid
	GroupID    string // 群 openid
	MemberRole string // owner/admin/member（事件下发；未知为空）
	IsOwner    bool   // 是否 bot 主人（BOT_OWNERS）
}

// elevated 发起人是否群主/管理员或 bot 主人（可直接执行操作）。
func (a GroupActor) elevated() bool {
	return a.IsOwner || a.MemberRole == bot.MemberRoleOwner || a.MemberRole == bot.MemberRoleAdmin
}

type groupActorKey struct{}

// WithGroupActor 把发起人身份塞进 ctx（仿 memory.WithScope / scheduler.WithChatInfo）。
func WithGroupActor(ctx context.Context, a GroupActor) context.Context {
	return context.WithValue(ctx, groupActorKey{}, a)
}

// GroupActorFrom 取出发起人身份（未注入返回 ok=false）。
func GroupActorFrom(ctx context.Context) (GroupActor, bool) {
	a, ok := ctx.Value(groupActorKey{}).(GroupActor)
	return a, ok
}

// GroupAdminAPI 是群管工具需要的最小能力（*bot.Bot 天然满足；测试注入假实现）。
type GroupAdminAPI interface {
	ListGroupMembers(ctx context.Context, groupOpenID string) ([]bot.GroupMember, error)
	GetGroupMember(ctx context.Context, groupOpenID, memberOpenID string) (*bot.GroupMember, error)
	RemoveGroupMembers(ctx context.Context, groupOpenID string, memberOpenIDs []string, addToBlacklist bool) (*bot.RemoveMembersResult, error)
	ListGroupBlacklist(ctx context.Context, groupOpenID string) ([]bot.GroupBlacklistUser, error)
	SetGroupBlacklist(ctx context.Context, groupOpenID, op string, memberOpenIDs []string) ([]string, error)
	AvatarURLTemplate() string // 成员头像 URL 模板（{openid} 占位）；空 = 不提示
}

// EvidenceSearcher 是证据核实所需的记忆检索能力（*memory.Store 满足）。
type EvidenceSearcher interface {
	Search(sc memory.Scope, q memory.SearchQuery) ([]memory.Message, error)
}

// evidenceMinLen 证据片段最小长度（rune 计）：太短的片段（如「你好」）无辨识度，
// 既容易误匹配也无法判断恶意性。
const evidenceMinLen = 6

// verifyEvidence 核实 target 近 24h 在本群确实发过 evidence 这段原文
// （子串匹配：evidence 须是对方某条消息的连续片段）。
func verifyEvidence(s EvidenceSearcher, groupID, target, evidence string) bool {
	if s == nil {
		return false
	}
	msgs, err := s.Search(memory.Scope{Source: "group", GroupID: groupID}, memory.SearchQuery{
		User:  target, // openid 精确匹配（SearchQuery.User 同时匹配昵称/openid）
		Query: evidence,
		Since: 24 * time.Hour,
		Limit: 3,
	})
	return err == nil && len(msgs) > 0
}

// --- group_members：成员查询（所有人）---

// GroupMembers 让 AI 查询群成员列表/按昵称搜成员（把「发广告那个人」解析成 openid）。
type GroupMembers struct{ API GroupAdminAPI }

func (t *GroupMembers) Name() string { return "group_members" }

func (t *GroupMembers) Description() string {
	return "查询本群的成员列表（昵称/角色/openid/入群时间），可按昵称关键词过滤。" +
		"把用户说的某个人定位到 openid 时用（后续踢人/拉黑等操作都认 openid）。仅群聊可用。"
}

func (t *GroupMembers) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "昵称关键词（子串，大小写不敏感）；空=返回全部"},
		},
	}
}

func (t *GroupMembers) Guidelines() []string {
	return nil
}

func (t *GroupMembers) Execute(ctx context.Context, arguments string) (string, error) {
	actor, ok := GroupActorFrom(ctx)
	if !ok {
		return "当前会话不支持群成员查询（仅群聊可用）", nil
	}
	members, err := t.API.ListGroupMembers(ctx, actor.GroupID)
	if err != nil {
		return "", fmt.Errorf("查询群成员失败: %w", err)
	}
	kw := strings.ToLower(strings.TrimSpace(gjson.Get(arguments, "query").String()))
	var lines []string
	for _, m := range members {
		if kw != "" && !strings.Contains(strings.ToLower(m.Username), kw) {
			continue
		}
		role := m.MemberRole
		if role == "" {
			role = "member"
		}
		if m.Bot {
			role += "(bot)"
		}
		lines = append(lines, fmt.Sprintf("%s | %s | %s | 入群 %s",
			m.Username, role, m.MemberOpenID, m.JoinedAt))
	}
	if len(lines) == 0 {
		if kw != "" {
			return fmt.Sprintf("没有昵称含「%s」的成员", kw), nil
		}
		return "群成员列表为空", nil
	}
	if len(lines) > 50 {
		lines = append(lines[:50], fmt.Sprintf("…（其余 %d 人从略）", len(lines)-50))
	}
	out := fmt.Sprintf("共 %d 人：\n%s", len(members), strings.Join(lines, "\n"))
	if tpl := t.API.AvatarURLTemplate(); tpl != "" {
		out += "\n成员头像：" + tpl + "（{openid} 换成某人的 openid 即其头像直链）"
	}
	return out, nil
}

// --- kick_group_member：踢人（管理员直接；普通成员需证据核实）---

// KickGroupMember 让 AI 把发恶意内容（色情/诈骗/刷屏广告等）的成员移出群。
// 普通成员发起时必须带 evidence（对方消息原文片段）并经记忆库核实。
type KickGroupMember struct {
	API GroupAdminAPI
	Mem EvidenceSearcher // 证据核实用聊天记录库；nil = 普通成员路径不可用
}

func (t *KickGroupMember) Name() string { return "kick_group_member" }

func (t *KickGroupMember) Description() string {
	return "把一名群成员移出本群（不拉黑）。适用于对方发送色情/诈骗/恶意刷屏广告等违规内容。" +
		"参数 member_openid 用 group_members 查。" +
		"群主/管理员/主人发起可直接执行；普通成员举报必须填 evidence=对方消息的原文片段" +
		"（从引用消息或 search_history 结果里取有辨识度的连续片段，如链接或关键句），" +
		"工具会核实对方近 24 小时确实发过这段内容后才执行，核实不过会拒绝。"
}

func (t *KickGroupMember) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"member_openid": map[string]any{"type": "string", "description": "要移出的成员 openid（group_members 查）"},
			"reason":        map[string]any{"type": "string", "description": "移出原因（如：发送诈骗链接），用于日志审计"},
			"evidence":      map[string]any{"type": "string", "description": "普通成员发起时必填：对方消息的原文连续片段（管理员可省略）"},
		},
		"required": []string{"member_openid", "reason"},
	}
}

func (t *KickGroupMember) Guidelines() []string {
	return []string{
		"用户要求踢人时：先用 group_members（或 search_history）把目标定位到 openid 并拿到消息原文，再调 kick_group_member",
		"普通成员举报恶意消息：evidence 必须填对方消息原文片段（链接/关键句），不能凭举报人的一面之词踢人；核实不过就建议联系群管理员处理",
		"踢人不可逆，拿不准（如目标身份不明、证据存疑）时不要执行，转告管理员人工处理",
	}
}

func (t *KickGroupMember) Execute(ctx context.Context, arguments string) (string, error) {
	actor, ok := GroupActorFrom(ctx)
	if !ok {
		return "踢人仅群聊可用", nil
	}
	target := gjson.Get(arguments, "member_openid").String()
	if target == "" {
		return "缺少 member_openid（先用 group_members 查）", nil
	}
	reason := strings.TrimSpace(gjson.Get(arguments, "reason").String())

	// 目标校验：实时拉取角色，群主/管理员/机器人一律拒绝（本地拦下，不浪费注定失败的调用）
	info, err := t.API.GetGroupMember(ctx, actor.GroupID, target)
	if err != nil {
		return "", fmt.Errorf("查询目标成员失败: %w", err)
	}
	if info.MemberRole == bot.MemberRoleOwner || info.MemberRole == bot.MemberRoleAdmin {
		return fmt.Sprintf("不能移除%s「%s」（群主/管理员不能被移出群）", roleZh(info.MemberRole), info.Username), nil
	}
	if info.Bot {
		return "不能移除机器人账号", nil
	}

	// 普通成员：证据核实（管理员/主人免）
	evidence := strings.TrimSpace(gjson.Get(arguments, "evidence").String())
	if !actor.elevated() {
		if len([]rune(evidence)) < evidenceMinLen {
			return fmt.Sprintf("你是普通群成员，踢人需提供证据：evidence 填对方消息的原文片段（至少 %d 字，如链接或关键句），否则请 @群管理员 人工处理", evidenceMinLen), nil
		}
		if t.Mem == nil {
			return "当前未启用聊天记忆库，无法核实证据，普通成员不能直接踢人（请联系群管理员）", nil
		}
		if !verifyEvidence(t.Mem, actor.GroupID, target, evidence) {
			return "证据核实未通过：近 24 小时的群聊记录里没有查到该成员发过这段内容。" +
				"可换一段对方消息的原文连续片段重试；仍不行的说明证据不足，请转群管理员人工处理。", nil
		}
	}

	res, err := t.API.RemoveGroupMembers(ctx, actor.GroupID, []string{target}, false)
	if err != nil {
		return "", fmt.Errorf("移除失败: %w", err)
	}
	who := actor.MemberRole
	if who == "" {
		who = "member"
	}
	if actor.IsOwner {
		who += "/owner"
	}
	log.Printf("[agent-group] 群 %s：%s(%s) 经 AI 踢出 %s(%s) 原因=%q 证据=%q 结果=%s",
		actor.GroupID, actor.UserID, who, info.Username, target, reason, evidence, res.RemoveMembersResult)
	return fmt.Sprintf("已将「%s」移出群（原因：%s）", info.Username, reason), nil
}

// roleZh 角色转中文（回执用）。
func roleZh(role string) string {
	switch role {
	case bot.MemberRoleOwner:
		return "群主"
	case bot.MemberRoleAdmin:
		return "管理员"
	default:
		return "成员"
	}
}

// --- group_blacklist：黑名单 list（所有人）/ add、del（管理员）---

// GroupBlacklist 让 AI 查看与维护群黑名单（add/del 仅群主/管理员/主人）。
type GroupBlacklist struct{ API GroupAdminAPI }

func (t *GroupBlacklist) Name() string { return "group_blacklist" }

func (t *GroupBlacklist) Description() string {
	return "查看或维护本群黑名单。op=list 查看列表（所有人可用）；op=add 把 openid 加入黑名单、" +
		"op=del 移出（仅群主/管理员/主人可用；add 只收已不在群中的人，在群里的先用 kick_group_member）。"
}

func (t *GroupBlacklist) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"op":             map[string]any{"type": "string", "enum": []string{"list", "add", "del"}, "description": "list=查看 / add=加入 / del=移出"},
			"member_openids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "add/del 时的 openid 列表（上限 20）"},
		},
		"required": []string{"op"},
	}
}

func (t *GroupBlacklist) Guidelines() []string { return nil }

func (t *GroupBlacklist) Execute(ctx context.Context, arguments string) (string, error) {
	actor, ok := GroupActorFrom(ctx)
	if !ok {
		return "群黑名单仅群聊可用", nil
	}
	op := gjson.Get(arguments, "op").String()
	switch op {
	case "list", "":
		users, err := t.API.ListGroupBlacklist(ctx, actor.GroupID)
		if err != nil {
			return "", fmt.Errorf("查询群黑名单失败: %w", err)
		}
		if len(users) == 0 {
			return "群黑名单为空", nil
		}
		lines := make([]string, 0, len(users))
		for _, u := range users {
			lines = append(lines, fmt.Sprintf("%s | %s | 拉黑于 %s", u.Username, u.MemberOpenID, u.BannedAt))
		}
		return fmt.Sprintf("群黑名单共 %d 人：\n%s", len(users), strings.Join(lines, "\n")), nil
	case "add", "del":
		if !actor.elevated() {
			return "黑名单增删仅群主/管理员/主人可用", nil
		}
		var ids []string
		for _, id := range gjson.Get(arguments, "member_openids").Array() {
			if s := strings.TrimSpace(id.String()); s != "" {
				ids = append(ids, s)
			}
		}
		if len(ids) == 0 {
			return "add/del 需要 member_openids 列表", nil
		}
		apiOp := bot.BlacklistOpAdd
		verb := "加入"
		if op == "del" {
			apiOp, verb = bot.BlacklistOpDel, "移出"
		}
		fail, err := t.API.SetGroupBlacklist(ctx, actor.GroupID, apiOp, ids)
		if err != nil {
			return "", fmt.Errorf("黑名单操作失败: %w", err)
		}
		log.Printf("[agent-group] 群 %s：%s 经 AI 黑名单 %s：%v（失败 %d）", actor.GroupID, actor.UserID, op, ids, len(fail))
		out := fmt.Sprintf("已%s黑名单 %d 人", verb, len(ids))
		if len(fail) > 0 {
			out += fmt.Sprintf("（失败 %d 人：%s）", len(fail), strings.Join(fail, "、"))
		}
		return out, nil
	default:
		return fmt.Sprintf("未知 op：%s（list/add/del）", op), nil
	}
}
