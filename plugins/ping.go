// Package plugins 是示例插件集合，演示如何用 qqbot/bot 框架编写功能。
package plugins

import (
	"qqbot/internal/bot"
)

// Ping 是最简命令插件：/ping 或 @bot ping → 回 pong。
type Ping struct{}

// NewPing 创建 Ping 插件。
func NewPing() *Ping { return &Ping{} }

// Install 注册命令。
func (p *Ping) Install(b *bot.Bot) {
	b.Command("ping", p.run).
		Desc("测试机器人是否在线").
		Alias("p", "pong")
}

func (p *Ping) run(c *bot.Ctx) {
	_, _ = c.ReplyText("pong!")
}
