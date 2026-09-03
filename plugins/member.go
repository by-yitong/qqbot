package plugins

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"qqbot/internal/bot"
	"qqbot/internal/utils"
)

// Member 群成员管理与群黑名单：调官方 2026-09 新增的成员列表/详情/批量移除/黑名单接口
// （均为白名单接口，非白名单机器人返回 11253——未获平台内邀接入时命令会报无权限）。
//
// 命令（查询开放给所有群成员；操作类 AdminOnly——群主/群管理员或主人可用，
// 角色来自群消息事件下发的 member_role）：
//
//	成员            群成员概览（人数/群主/管理员）；成员 @某人 看详情；成员 关键词 搜昵称
//	黑名单          查看群黑名单
//	踢 @某人 [拉黑]  移出群（可选同时拉黑），仅管理员/主人；一次 @ 多人批量（上限 20）
//	拉黑 openid…    加入群黑名单（仅不在群中的人可拉黑），仅管理员/主人
//	解除拉黑 openid… 移出群黑名单，仅管理员/主人
//
// 配置（main.go 装配）：
//
//	plugins.NewMember(groups)
//
// groups 为群白名单（空 = 所有群）。
type Member struct {
	Groups  []string // 群白名单（空 = 所有群）
	Timeout time.Duration
}

// NewMember 构造群成员管理插件。
func NewMember(groups []string) *Member {
	return &Member{Groups: groups, Timeout: 30 * time.Second}
}

// NewMemberFromEnv 读 MEMBER_* 变量构造群成员管理插件；MEMBER_ENABLE=1 才启用，否则返回 nil。
// 踢人/拉黑命令仅群主/群管理员或主人可用（AdminOnly：事件下发的 member_role + BOT_OWNERS）；
// MEMBER_GROUPS 限定群（空 = 所有群）。
func NewMemberFromEnv() bot.Plugin {
	if utils.EnvInt("MEMBER_ENABLE", 0) != 1 {
		return nil
	}
	log.Println("[member] 已启用群成员管理与黑名单（/成员 /黑名单 /踢 /拉黑 /解除拉黑；踢人/拉黑仅群主/群管理员或主人；官方白名单接口，未获内邀接入的机器人会报 11253 无权限）")
	return NewMember(utils.SplitList(os.Getenv("MEMBER_GROUPS")))
}

// memberAPI 插件需要的最小能力（*bot.Bot 天然满足；测试用假实现注入）。
type memberAPI interface {
	ListGroupMembers(ctx context.Context, groupOpenID string) ([]bot.GroupMember, error)
	GetGroupMember(ctx context.Context, groupOpenID, memberOpenID string) (*bot.GroupMember, error)
	RemoveGroupMembers(ctx context.Context, groupOpenID string, memberOpenIDs []string, addToBlacklist bool) (*bot.RemoveMembersResult, error)
	ListGroupBlacklist(ctx context.Context, groupOpenID string) ([]bot.GroupBlacklistUser, error)
	SetGroupBlacklist(ctx context.Context, groupOpenID, op string, memberOpenIDs []string) ([]string, error)
}

// Install 注册命令。查询类（成员/黑名单）开放给所有群成员；操作类（踢/拉黑/解除拉黑）
// AdminOnly——群主/群管理员（事件下发的 member_role）或主人（BOT_OWNERS）可用。
func (p *Member) Install(b *bot.Bot) {
	b.Command("成员", p.cmdMembers).Alias("member").
		Desc("群成员概览；@某人 看详情；带关键词搜昵称").
		Usage("/成员 [@某人|关键词]").
		GroupOnly().RequireAtOrPrefix()
	b.Command("黑名单", p.cmdBlacklist).Alias("blacklist").
		Desc("查看群黑名单列表").
		GroupOnly().RequireAtOrPrefix()
	b.Command("踢", p.cmdKick).Alias("kick").
		Desc("移 @某人 出群（可加「拉黑」同时拉黑）").
		Usage("/踢 @某人 [拉黑]").
		GroupOnly().AdminOnly().RequireAtOrPrefix()
	b.Command("拉黑", p.cmdBlacklistAdd).
		Desc("把 openid 加入群黑名单（仅限已不在群中的人；在群里的用 /踢 @某人 拉黑）").
		Usage("/拉黑 openid [openid…]").
		GroupOnly().AdminOnly().RequireAtOrPrefix()
	b.Command("解除拉黑", p.cmdBlacklistDel).Alias("unblacklist").
		Desc("把 openid 移出群黑名单").
		Usage("/解除拉黑 openid [openid…]").
		GroupOnly().AdminOnly().RequireAtOrPrefix()
}

// timeout 返回 API 超时（<=0 用默认 30s；成员列表要翻页，比单发命令宽一些）。
func (p *Member) timeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return 30 * time.Second
}

// cmdMembers /成员：无参概览；@某人 详情；关键词搜昵称。
func (p *Member) cmdMembers(c *bot.Ctx) {
	if !groupAllowed(p.Groups, c.GroupID()) {
		return
	}
	if ms := c.Mentions(); len(ms) > 0 {
		p.memberDetail(c.Bot, c, ms)
		return
	}
	keyword := strings.TrimSpace(c.Args.Join())
	p.memberList(c.Bot, c, keyword)
}

// memberList 拉全量成员后按 keyword 三种形态回复（测试可注入假 memberAPI）。
func (p *Member) memberList(ap memberAPI, c *bot.Ctx, keyword string) {
	ctx, cancel := context.WithTimeout(c.Context, p.timeout())
	defer cancel()
	members, err := ap.ListGroupMembers(ctx, c.GroupID())
	if err != nil {
		c.ReplyText("获取群成员失败：" + err.Error())
		return
	}
	if keyword != "" {
		c.ReplyText(formatMemberSearch(members, keyword))
		return
	}
	c.ReplyText(formatMemberOverview(members))
}

// memberDetail @ 的成员逐个拉详情（测试可注入假 memberAPI）。
func (p *Member) memberDetail(ap memberAPI, c *bot.Ctx, ms []bot.Mention) {
	ctx, cancel := context.WithTimeout(c.Context, p.timeout())
	defer cancel()
	var sb strings.Builder
	for i, m := range ms {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		info, err := ap.GetGroupMember(ctx, c.GroupID(), m.ID)
		if err != nil {
			fmt.Fprintf(&sb, "【%s】详情获取失败：%s", memberDisplayName(m.Username, m.ID), err)
			continue
		}
		sb.WriteString(formatMemberDetail(info))
	}
	c.ReplyText(sb.String())
}

// cmdKick /踢 @某人 [拉黑]：批量移除，可选同时拉黑。
func (p *Member) cmdKick(c *bot.Ctx) {
	if !groupAllowed(p.Groups, c.GroupID()) {
		return
	}
	ms := c.Mentions()
	if len(ms) == 0 {
		c.ReplyText("请 @ 要移出群的人，如：/踢 @某人（加「拉黑」同时拉黑，如 /踢 @某人 拉黑）")
		return
	}
	p.kick(c.Bot, c, ms, kickBlacklistFlag(c.Args.Args))
}

// kickBlacklistFlag /踢 的参数里是否带了「拉黑」（拉黑/黑名单/ban 任一）。
func kickBlacklistFlag(args []string) bool {
	for _, a := range args {
		if a == "拉黑" || a == "黑名单" || a == "ban" {
			return true
		}
	}
	return false
}

// kick 执行移除并回执（测试可注入假 memberAPI）。
// 前置校验：@ 的是群主或机器人账号的直接本地拦下（事件 mentions 自带角色，
// 不浪费一次注定失败的 API 调用）；管理员目标官方是否放行未明确，交给服务端判定。
func (p *Member) kick(ap memberAPI, c *bot.Ctx, ms []bot.Mention, blacklist bool) {
	var blocked, targets []bot.Mention
	for _, m := range ms {
		if m.Role == bot.MemberRoleOwner || m.Bot {
			blocked = append(blocked, m)
			continue
		}
		targets = append(targets, m)
	}
	if len(blocked) > 0 {
		c.ReplyText("无法移除 " + mentionNames(blocked) + "（群主/机器人不能被移出群）")
	}
	if len(targets) == 0 {
		return
	}
	ms = targets
	if len(ms) > 20 {
		ms = ms[:20]
	}
	ids := make([]string, 0, len(ms))
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	ctx, cancel := context.WithTimeout(c.Context, p.timeout())
	defer cancel()
	res, err := ap.RemoveGroupMembers(ctx, c.GroupID(), ids, blacklist)
	if err != nil {
		c.ReplyText("移除失败：" + err.Error())
		return
	}
	log.Printf("[member] 群 %s：%s(%s) 移除 %s（拉黑=%v）", c.GroupID(), c.Username(), c.UserID(), mentionNames(ms), blacklist)
	out := "已移除 " + mentionNames(ms)
	if blacklist {
		out += " 并加入黑名单"
		if len(res.AddToMemberBlacklistFailOpenIDs) > 0 {
			out += fmt.Sprintf("（其中 %d 人拉黑失败，可稍后用 /拉黑 openid 补拉）", len(res.AddToMemberBlacklistFailOpenIDs))
		}
	}
	c.ReplyText(out)
}

// cmdBlacklistAdd /拉黑 openid…：目标须已不在群中。
func (p *Member) cmdBlacklistAdd(c *bot.Ctx) {
	if !groupAllowed(p.Groups, c.GroupID()) {
		return
	}
	ids := blacklistTargets(c)
	if len(ids) == 0 {
		c.ReplyText("请给要拉黑的 openid（在群里的成员先 /踢 @某人 拉黑，黑名单只收已不在群中的人）")
		return
	}
	p.blacklistOp(c.Bot, c, bot.BlacklistOpAdd, ids)
}

// cmdBlacklistDel /解除拉黑 openid…
func (p *Member) cmdBlacklistDel(c *bot.Ctx) {
	if !groupAllowed(p.Groups, c.GroupID()) {
		return
	}
	ids := blacklistTargets(c)
	if len(ids) == 0 {
		c.ReplyText("请给要移出黑名单的 openid")
		return
	}
	p.blacklistOp(c.Bot, c, bot.BlacklistOpDel, ids)
}

// blacklistTargets 收集命令目标 openid：@ 的人 + 参数里的 openid 文本（已退群的人
// 无法 @，只能贴 openid）。上限 20（官方单次限制）。
func blacklistTargets(c *bot.Ctx) []string {
	var ids []string
	for _, m := range c.Mentions() {
		ids = append(ids, m.ID)
	}
	ids = append(ids, c.Args.Args...)
	if len(ids) > 20 {
		ids = ids[:20]
	}
	return ids
}

// blacklistOp 执行黑名单操作并回执（测试可注入假 memberAPI）。
func (p *Member) blacklistOp(ap memberAPI, c *bot.Ctx, op string, ids []string) {
	ctx, cancel := context.WithTimeout(c.Context, p.timeout())
	defer cancel()
	fail, err := ap.SetGroupBlacklist(ctx, c.GroupID(), op, ids)
	if err != nil {
		c.ReplyText("黑名单操作失败：" + err.Error())
		return
	}
	log.Printf("[member] 群 %s：%s(%s) 黑名单 %s：%v", c.GroupID(), c.Username(), c.UserID(), op, ids)
	verb := "已加入黑名单"
	if op == bot.BlacklistOpDel {
		verb = "已移出黑名单"
	}
	out := fmt.Sprintf("%s %d 人", verb, len(ids))
	if len(fail) > 0 {
		out += fmt.Sprintf("（失败 %d 人：%s）", len(fail), strings.Join(fail, "、"))
	}
	c.ReplyText(out)
}

// cmdBlacklist /黑名单：查看群黑名单。
func (p *Member) cmdBlacklist(c *bot.Ctx) {
	if !groupAllowed(p.Groups, c.GroupID()) {
		return
	}
	p.blacklistList(c.Bot, c)
}

// blacklistList 拉黑名单并回执（测试可注入假 memberAPI）。
func (p *Member) blacklistList(ap memberAPI, c *bot.Ctx) {
	ctx, cancel := context.WithTimeout(c.Context, p.timeout())
	defer cancel()
	users, err := ap.ListGroupBlacklist(ctx, c.GroupID())
	if err != nil {
		c.ReplyText("获取群黑名单失败：" + err.Error())
		return
	}
	c.ReplyText(formatBlacklist(users))
}

// --- 展示格式化 ---

// memberDisplayName 昵称缺失时用 openid 前 6 位兜底。
func memberDisplayName(username, openid string) string {
	if username != "" {
		return username
	}
	if len(openid) > 6 {
		return openid[:6] + "…"
	}
	return openid
}

// memberRoleZh 角色转中文。
func memberRoleZh(role string) string {
	switch role {
	case bot.MemberRoleOwner:
		return "群主"
	case bot.MemberRoleAdmin:
		return "管理员"
	case bot.MemberRoleMember:
		return "成员"
	case "":
		return "未知"
	default:
		return role
	}
}

// fmtMemberTime RFC3339 时间转「2006-01-02 15:04」；解析失败原样返回。
func fmtMemberTime(s string) string {
	if s == "" {
		return "未知"
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Format("2006-01-02 15:04")
	}
	return s
}

// formatMemberOverview 成员概览：人数构成 + 群主/管理员/机器人 + 成员昵称（最多 30 个）。
// 成员计数不含机器人（机器人单列一行，与人数构成对齐）。
func formatMemberOverview(members []bot.GroupMember) string {
	if len(members) == 0 {
		return "群成员列表为空（或机器人无该接口权限）"
	}
	var owners, admins, normals, bots []string
	for _, m := range members {
		name := memberDisplayName(m.Username, m.MemberOpenID)
		if m.Bot {
			bots = append(bots, name)
			continue
		}
		switch m.MemberRole {
		case bot.MemberRoleOwner:
			owners = append(owners, name)
		case bot.MemberRoleAdmin:
			admins = append(admins, name)
		default:
			normals = append(normals, name)
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "本群共 %d 人：群主 %d、管理员 %d、成员 %d、机器人 %d",
		len(members), len(owners), len(admins), len(normals), len(bots))
	if len(owners) > 0 {
		sb.WriteString("\n群主：" + strings.Join(owners, "、"))
	}
	if len(admins) > 0 {
		sb.WriteString("\n管理员：" + strings.Join(admins, "、"))
	}
	if len(normals) > 0 {
		shown := normals
		if len(shown) > 30 {
			sb.WriteString("\n成员：" + strings.Join(shown[:30], "、") + fmt.Sprintf("…（其余 %d 人从略）", len(normals)-30))
		} else {
			sb.WriteString("\n成员：" + strings.Join(shown, "、"))
		}
	}
	if len(bots) > 0 {
		sb.WriteString("\n机器人：" + strings.Join(bots, "、"))
	}
	return sb.String()
}

// formatMemberSearch 按昵称子串搜索（大小写不敏感），最多展示 10 条；openid 显示前 8 位
// （完整 openid 用 /成员 @某人 看）。
func formatMemberSearch(members []bot.GroupMember, keyword string) string {
	kw := strings.ToLower(keyword)
	var hits []bot.GroupMember
	for _, m := range members {
		if strings.Contains(strings.ToLower(m.Username), kw) || strings.Contains(strings.ToLower(m.MemberOpenID), kw) {
			hits = append(hits, m)
		}
	}
	switch len(hits) {
	case 0:
		return "没有找到昵称含「" + keyword + "」的成员"
	case 1:
		return "找到 1 人：\n" + formatMemberDetail(&hits[0])
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "找到 %d 人：", len(hits))
	limit := 10
	if len(hits) < limit {
		limit = len(hits)
	}
	for _, m := range hits[:limit] {
		oid := m.MemberOpenID
		if len(oid) > 8 {
			oid = oid[:8] + "…"
		}
		fmt.Fprintf(&sb, "\n%s（%s，入群 %s）%s", memberDisplayName(m.Username, m.MemberOpenID), memberRoleZh(m.MemberRole), fmtMemberTime(m.JoinedAt), oid)
	}
	if len(hits) > limit {
		fmt.Fprintf(&sb, "\n…（其余 %d 人从略，换更精确的关键词）", len(hits)-limit)
	}
	return sb.String()
}

// formatMemberDetail 单个成员详情（openid 完整给出，便于复制到配置）。
func formatMemberDetail(m *bot.GroupMember) string {
	name := memberDisplayName(m.Username, m.MemberOpenID)
	role := memberRoleZh(m.MemberRole)
	if m.Bot {
		role += "（机器人）"
	}
	return fmt.Sprintf("【%s】%s\n入群：%s\nopenid：%s", name, role, fmtMemberTime(m.JoinedAt), m.MemberOpenID)
}

// formatBlacklist 黑名单列表，最多展示 30 条。
func formatBlacklist(users []bot.GroupBlacklistUser) string {
	if len(users) == 0 {
		return "群黑名单为空"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "群黑名单共 %d 人：", len(users))
	limit := 30
	if len(users) < limit {
		limit = len(users)
	}
	for _, u := range users[:limit] {
		fmt.Fprintf(&sb, "\n%s 拉黑于 %s  %s",
			memberDisplayName(u.Username, u.MemberOpenID), fmtMemberTime(u.BannedAt), u.MemberOpenID)
	}
	if len(users) > limit {
		fmt.Fprintf(&sb, "\n…（其余 %d 人从略）", len(users)-limit)
	}
	return sb.String()
}
