package plugins

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"qqbot/internal/bot"
	"qqbot/internal/utils"
)

// Welcome 入群欢迎：收到 GROUP_MEMBER_ADD（新成员入群）后往群里发一条欢迎消息。
//
// 约束（官方接口决定）：
//   - 入群事件没有消息锚点，event_id 被动回复也不支持该事件 → 欢迎语只能走
//     **主动消息**（SendGroupText 空 msgAnchor），需在 q.qq.com 开通主动消息权限；
//     主动消息有频控（单群 1000 条/天），入群频率低足够用。
//   - 事件体只有 openid 没有昵称 → 从 GROUP_JOIN_REQUEST 事件（审批前推送，带 username）
//     缓存「openid → 昵称」，欢迎语里的 {name} 占位用它；取不到时用「新朋友」。
//
// 配置（main.go 装配）：
//
//	plugins.NewWelcome("欢迎 {name} 加入本群！🎉", nil)
//
// groups 为启用的群 openid 白名单（空 = 所有群）。
type Welcome struct {
	Text   string   // 欢迎文案模板，{name} 占位
	Groups []string // 群白名单（空 = 所有群）

	Timeout time.Duration // 发送超时，默认 10s

	mu    sync.Mutex
	names map[string]string // member_openid → 昵称（来自入群申请事件）
	order []string          // 插入顺序，超容量淘汰最旧的
}

// nameCacheCap 昵称缓存上限（入群申请不多，2000 足够覆盖重启周期内的量）。
const nameCacheCap = 2000

// NewWelcome 构造入群欢迎插件。text 里的 {name} 会替换成新成员昵称。
func NewWelcome(text string, groups []string) *Welcome {
	return &Welcome{Text: text, Groups: groups, names: map[string]string{}}
}

// NewWelcomeFromEnv 读 WELCOME_TEXT/WELCOME_GROUPS 构造；未配欢迎语返回 nil（不注册）。
// 欢迎语是主动消息（入群事件无回复锚点），需在 q.qq.com 开通主动消息权限。
func NewWelcomeFromEnv() bot.Plugin {
	wtxt := utils.EnvOr("WELCOME_TEXT", "")
	if wtxt == "" {
		return nil
	}
	log.Println("[welcome] 已启用入群欢迎")
	return NewWelcome(wtxt, utils.SplitList(os.Getenv("WELCOME_GROUPS")))
}

// rememberName 记录入群申请里的昵称（入群事件无昵称，审批通过后成员入群时取用）。
func (p *Welcome) rememberName(e *bot.GroupJoinRequestEvent) {
	if e.Username == "" || e.MemberOpenID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.names[e.MemberOpenID]; !ok {
		p.order = append(p.order, e.MemberOpenID)
		if len(p.order) > nameCacheCap {
			delete(p.names, p.order[0])
			p.order = p.order[1:]
		}
	}
	p.names[e.MemberOpenID] = e.Username
}

// nameOf 取昵称，取不到返回 fallback。
func (p *Welcome) nameOf(openid, fallback string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n := p.names[openid]; n != "" {
		return n
	}
	return fallback
}

// Render 渲染欢迎文案（{name} → name）。
func (p *Welcome) Render(name string) string {
	return strings.ReplaceAll(p.Text, "{name}", name)
}

// greet 发送欢迎消息（主动消息：事件无锚点，event_id 不支持本事件）。
func (p *Welcome) greet(b *bot.Bot, e *bot.GroupMemberAddEvent) {
	if !groupAllowed(p.Groups, e.GroupOpenID) {
		return
	}
	name := p.nameOf(e.MemberOpenID, "新朋友")
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	text := p.Render(name)
	if _, err := b.SendGroupText(ctx, e.GroupOpenID, "", text); err != nil {
		log.Printf("[welcome] 群 %s 欢迎 %s 发送失败（主动消息需 q.qq.com 开通权限）: %v",
			e.GroupOpenID, name, err)
		return
	}
	log.Printf("[welcome] 已欢迎 %s (%s) 入群 %s", name, e.MemberOpenID, e.GroupOpenID)
}

// Install 注册事件监听。
func (p *Welcome) Install(b *bot.Bot) {
	if p.Text == "" {
		return
	}
	b.OnGroupJoinRequest(p.rememberName)
	b.OnGroupMemberAdd(func(e *bot.GroupMemberAddEvent) { p.greet(b, e) })
}

// groupAllowed 判断群是否在白名单内（空白名单 = 全部允许）。
func groupAllowed(allow []string, groupOpenID string) bool {
	if len(allow) == 0 {
		return true
	}
	for _, g := range allow {
		if g == groupOpenID {
			return true
		}
	}
	return false
}
