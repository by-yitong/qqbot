package plugins

import (
	"context"
	"log"
	"time"

	"qqbot/internal/bot"
)

// Online 在 bot 启动（api 就绪）后，主动向频道/群/私聊发送「已上线」通知。
//
// 频道一直支持主动发送；群/私聊的主动发送需在 q.qq.com 开通「主动消息」权限
// （受官方频控/额度限制）。开通后，群/私聊用空锚点发送即为主动消息。
//
// 用法：
//
//	plugins.NewOnline("频道id", "🤖 已上线").Group("群openid").User("私聊openid")
type Online struct {
	ChannelID string        // 子频道 id（主动）
	GroupID   string        // 群 openid（主动，需权限）
	UserID    string        // 私聊 openid（主动，需权限）
	Text      string        // 通知文本
	Timeout   time.Duration // 单条发送超时，默认 10s
}

// NewOnline 构造上线通知插件（频道场景）。text 为空时用默认文案。
// 群/私聊目标用链式 .Group() / .User() 追加。
func NewOnline(userOpenID, text string) *Online {
	if text == "" {
		text = "🤖 机器人已上线"
	}
	return &Online{UserID: userOpenID, Text: text, Timeout: 10 * time.Second}
}

// Group 追加一个群通知目标（链式）。
func (p *Online) Group(groupOpenID string) *Online { p.GroupID = groupOpenID; return p }

// render 渲染上线通知的 markdown（含当前时间）。
// 用标题/加粗/引用，这些在腾讯原生 markdown 里渲染最稳；时间取发送时刻（本地时区）。
func (p *Online) render() string {
	title := p.Text
	if title == "" {
		title = "🤖 机器人已上线"
	}
	now := time.Now().Format("2006-01-02 15:04:05")
	return "# " + title + "\n\n" +
		"⏰ **时间**：" + now + "\n" +
		"🟢 **状态**：服务已就绪\n\n" +
		"> 随时待命，有问题随时 @ 我！"
}

// Install 注册启动钩子。
func (p *Online) Install(b *bot.Bot) {
	if p.ChannelID == "" && p.GroupID == "" && p.UserID == "" {
		return
	}
	b.OnStart(func(ctx context.Context) {
		timeout := p.Timeout
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		md := p.render()
		// send 发到单个目标，带独立超时；出错只记日志不影响其他目标。
		send := func(what, id string, f func(context.Context) (string, error)) {
			c, cancel := context.WithTimeout(ctx, timeout)
			mid, err := f(c)
			cancel()
			if err != nil {
				log.Printf("[online] 通知%s %s 失败: %v", what, id, err)
			} else {
				log.Printf("[online] 已通知%s %s (msgid=%s)", what, id, mid)
			}
		}
		if p.ChannelID != "" {
			send("频道", p.ChannelID, func(c context.Context) (string, error) { return b.SendChannelMarkdown(c, p.ChannelID, md) })
		}
		if p.GroupID != "" {
			send("群", p.GroupID, func(c context.Context) (string, error) { return b.SendGroupMarkdown(c, p.GroupID, "", md) })
		}
		if p.UserID != "" {
			send("私聊", p.UserID, func(c context.Context) (string, error) { return b.SendC2CMarkdown(c, p.UserID, "", md) })
		}
	})
}
