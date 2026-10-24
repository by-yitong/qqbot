package plugins

import (
	"qqbot/internal/bot"
)

// Echo 复读命令：/echo <文本>。
type Echo struct{}

// NewEcho 创建 Echo 插件。
func NewEcho() *Echo { return &Echo{} }

// Install 注册命令。
func (p *Echo) Install(b *bot.Bot) {
	b.Command("echo", p.run).
		Desc("复读一句话").
		Usage("/echo <文本>").
		Category(bot.CatGeneral)
}

func (p *Echo) run(c *bot.Ctx) {
	text := c.Args.Join()
	if text == "" {
		_, _ = c.ReplyText("你要我复读什么？用法：/echo <文本>")
		return
	}
	_, _ = c.ReplyText(text)
}
