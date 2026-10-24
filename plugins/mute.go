package plugins

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"qqbot/internal/bot"
	"qqbot/internal/utils"
)

// Mute 群成员禁言：/禁言 @某人 10分钟 调官方 restrict_chat_setting 接口，
// /解禁 @某人 解除。目标从消息的 mentions 取（@ 的人；机器人自身已在框架层
// Mentions() 里排除）。官方限制：机器人需是群管理员；只能禁言普通成员
// （群主/管理员/机器人本身不行）；单次最多 10 人；最长 30 天。
//
// 命令为 OwnerOnly（BOT_OWNERS 配置的主人 openid；fail-closed，未配置主人时
// 对所有人拒绝）——禁言是惩戒性操作，不放开给普通群成员。
//
// 配置（main.go 装配）：
//
//	plugins.NewMute(groups)
//
// groups 为群白名单（空 = 所有群）。
type Mute struct {
	Groups  []string // 群白名单（空 = 所有群）
	Timeout time.Duration
}

// NewMute 构造群禁言插件。
func NewMute(groups []string) *Mute {
	return &Mute{Groups: groups, Timeout: 10 * time.Second}
}

// NewMuteFromEnv 读 MUTE_* 变量构造群禁言插件；默认启用（谁能用已有 OwnerOnly 把关），
// MUTE_ENABLE=0 显式关闭。命令仅主人可用（BOT_OWNERS，用 /id 查自己的 openid）；
// MUTE_GROUPS 限定群（空 = 所有群）。
func NewMuteFromEnv() bot.Plugin {
	if utils.EnvInt("MUTE_ENABLE", 1) == 0 {
		return nil
	}
	log.Println("[mute] 已启用群成员禁言（/禁言 /解禁；仅主人可用；机器人需是群管理员，只能禁言普通成员）")
	return NewMute(utils.SplitList(os.Getenv("MUTE_GROUPS")))
}

// muteSetter 是禁言需要的最小能力（*bot.Bot 天然满足；测试用假实现注入）。
type muteSetter interface {
	SetGroupMemberMutes(ctx context.Context, groupOpenID string, states []bot.MemberMuteState) error
}

// Install 注册 /禁言 /解禁 命令（均仅主人可用，OwnerOnly 已含不进 /help 与菜单面板）。
func (p *Mute) Install(b *bot.Bot) {
	b.Command("禁言", p.cmdMute).Alias("mute").
		Desc("禁言 @某人 指定时长（如 10分钟 / 2小时 / 1天，最长 30 天）").
		Usage("/禁言 @某人 10分钟").
		GroupOnly().OwnerOnly().RequireAtOrPrefix().Category(bot.CatGroup)
	b.Command("解禁", p.cmdUnmute).Alias("unmute").
		Desc("解除 @某人 的禁言").Usage("/解禁 @某人").
		GroupOnly().OwnerOnly().RequireAtOrPrefix().Category(bot.CatGroup)
}

// cmdMute /禁言：目标 = 消息里 @ 的人（可多个，一次最多 10 人），时长 = 第一个参数。
func (p *Mute) cmdMute(c *bot.Ctx) {
	if !groupAllowed(p.Groups, c.GroupID()) {
		return
	}
	ms := c.Mentions()
	if len(ms) == 0 {
		c.ReplyText("请 @ 要禁言的人，如：/禁言 @某人 10分钟（支持 30秒/10分钟/2小时/1天/1周，最长 30 天）")
		return
	}
	d, err := parseMuteDuration(c.Args.Get(0))
	if err != nil {
		c.ReplyText(err.Error() + "。示例：/禁言 @某人 10分钟 / 2小时 / 1天 / 7d")
		return
	}
	p.mute(c.Bot, c, ms, d)
}

// mute 执行禁言并回执（测试可注入假 muteSetter）。
func (p *Mute) mute(ap muteSetter, c *bot.Ctx, ms []bot.Mention, d time.Duration) {
	if len(ms) > 10 {
		ms = ms[:10]
	}
	expire := time.Now().Add(d)
	states := make([]bot.MemberMuteState, 0, len(ms))
	for _, m := range ms {
		states = append(states, bot.MemberMuteState{
			Op:           bot.MuteOpAdd,
			MemberOpenID: m.ID,
			MuteExpireAt: expire.Format(time.RFC3339),
		})
	}
	ctx, cancel := context.WithTimeout(c.Context, p.timeout())
	defer cancel()
	if err := ap.SetGroupMemberMutes(ctx, c.GroupID(), states); err != nil {
		c.ReplyText("禁言失败：" + err.Error())
		return
	}
	log.Printf("[mute] 群 %s：%s(%s) 禁言 %s %s", c.GroupID(), c.Username(), c.UserID(), mentionNames(ms), humanDuration(d))
	c.ReplyText("已禁言 " + mentionNames(ms) + " " + humanDuration(d))
}

// cmdUnmute /解禁：目标 = 消息里 @ 的人（可多个，一次最多 10 人）。
func (p *Mute) cmdUnmute(c *bot.Ctx) {
	if !groupAllowed(p.Groups, c.GroupID()) {
		return
	}
	ms := c.Mentions()
	if len(ms) == 0 {
		c.ReplyText("请 @ 要解禁的人，如：/解禁 @某人")
		return
	}
	p.unmute(c.Bot, c, ms)
}

// unmute 执行解禁并回执（测试可注入假 muteSetter）。
func (p *Mute) unmute(ap muteSetter, c *bot.Ctx, ms []bot.Mention) {
	if len(ms) > 10 {
		ms = ms[:10]
	}
	states := make([]bot.MemberMuteState, 0, len(ms))
	for _, m := range ms {
		states = append(states, bot.MemberMuteState{Op: bot.MuteOpDel, MemberOpenID: m.ID})
	}
	ctx, cancel := context.WithTimeout(c.Context, p.timeout())
	defer cancel()
	if err := ap.SetGroupMemberMutes(ctx, c.GroupID(), states); err != nil {
		c.ReplyText("解禁失败：" + err.Error())
		return
	}
	log.Printf("[mute] 群 %s：%s(%s) 解禁 %s", c.GroupID(), c.Username(), c.UserID(), mentionNames(ms))
	c.ReplyText("已解除 " + mentionNames(ms) + " 的禁言")
}

// timeout 返回 API 超时（<=0 用默认 10s）。
func (p *Mute) timeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return 10 * time.Second
}

// mentionNames 拼 @ 的人名（昵称缺失用 openid 前 6 位），顿号分隔。
func mentionNames(ms []bot.Mention) string {
	names := make([]string, 0, len(ms))
	for _, m := range ms {
		n := m.Username
		if n == "" {
			n = m.ID
			if len(n) > 6 {
				n = n[:6] + "…"
			}
		}
		names = append(names, n)
	}
	return strings.Join(names, "、")
}

// humanDuration 时长转中文（90 分钟 → 「1小时30分钟」；日志与回执用）。
func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d秒", int(d/time.Second))
	}
	var parts []string
	for _, u := range []struct {
		name string
		d    time.Duration
	}{
		{"天", 24 * time.Hour},
		{"小时", time.Hour},
		{"分钟", time.Minute},
	} {
		if n := int(d / u.d); n > 0 {
			parts = append(parts, fmt.Sprintf("%d%s", n, u.name))
			d %= u.d
		}
	}
	return strings.Join(parts, "")
}

// muteUnits 时长单位后缀（长后缀在前，避免「1个小时」按「小时」截断后残留「1个」
// 解析失败就跳过、继续试其他单位）。
var muteUnits = []struct {
	suffix string
	d      time.Duration
}{
	{"个星期", 7 * 24 * time.Hour},
	{"星期", 7 * 24 * time.Hour},
	{"个周", 7 * 24 * time.Hour},
	{"周", 7 * 24 * time.Hour},
	{"weeks", 7 * 24 * time.Hour},
	{"week", 7 * 24 * time.Hour},
	{"w", 7 * 24 * time.Hour},
	{"天", 24 * time.Hour},
	{"日", 24 * time.Hour},
	{"days", 24 * time.Hour},
	{"day", 24 * time.Hour},
	{"d", 24 * time.Hour},
	{"个小时", time.Hour},
	{"小时", time.Hour},
	{"个时", time.Hour},
	{"时", time.Hour},
	{"hours", time.Hour},
	{"hour", time.Hour},
	{"h", time.Hour},
	{"分钟", time.Minute},
	{"个分", time.Minute},
	{"分", time.Minute},
	{"minutes", time.Minute},
	{"minute", time.Minute},
	{"min", time.Minute},
	{"m", time.Minute},
	{"秒钟", time.Second},
	{"秒", time.Second},
	{"seconds", time.Second},
	{"second", time.Second},
	{"sec", time.Second},
	{"s", time.Second},
}

// parseMuteDuration 解析禁言时长：支持中文（30秒/10分钟/2小时/1天/1周/1星期）
// 与字母（30s/10m/2h/1d/1w）单位，裸数字按分钟；上限 30 天。
func parseMuteDuration(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, fmt.Errorf("缺少时长")
	}
	for _, u := range muteUnits {
		if !strings.HasSuffix(s, u.suffix) {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(s, u.suffix)))
		if err != nil {
			continue // 后缀撞上了但前缀不是纯数字（如 1小时30分钟），继续试别的单位
		}
		return checkMuteDuration(time.Duration(n) * u.d)
	}
	if n, err := strconv.Atoi(s); err == nil { // 裸数字 = 分钟
		return checkMuteDuration(time.Duration(n) * time.Minute)
	}
	return 0, fmt.Errorf("无法识别时长「%s」", s)
}

// checkMuteDuration 校验时长为正且不超过 30 天。
func checkMuteDuration(d time.Duration) (time.Duration, error) {
	switch {
	case d <= 0:
		return 0, fmt.Errorf("时长必须是正数")
	case d > bot.MuteMaxDuration:
		return 0, fmt.Errorf("禁言最长 30 天")
	}
	return d, nil
}
