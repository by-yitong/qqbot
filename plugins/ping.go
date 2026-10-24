// Package plugins 是示例插件集合，演示如何用 qqbot/bot 框架编写功能。
package plugins

import (
	"log"
	"os"
	"runtime/debug"
	"strings"

	"qqbot/internal/agent/tools"
	"qqbot/internal/bot"
	"qqbot/internal/utils"
)

// Ping 命令插件：/ping 回 pong（在线检测）；/ping <域名/IP> 做真实 ping
// （与 agent 的 ping 工具共用实现，命令直达、不烧 AI token）。
type Ping struct{}

// NewPing 创建 Ping 插件。
func NewPing() *Ping { return &Ping{} }

// Install 注册命令。
func (p *Ping) Install(b *bot.Bot) {
	b.Command("ping", p.run).
		Desc("测试机器人是否在线；ping <域名/IP> 做真实 ping").
		Alias("p", "pong").
		Category(bot.CatGeneral)
}

// pingAllow 与 http_get / 站长工具共用的内网放行名单（AGENT_HTTP_ALLOW）。
var pingAllow = utils.SplitList(os.Getenv("AGENT_HTTP_ALLOW"))

func (p *Ping) run(c *bot.Ctx) {
	target := strings.TrimSpace(c.Args.Join())
	if target == "" {
		_, _ = c.ReplyText("pong!")
		return
	}
	// 真实 ping 要几秒，异步跑避免堵住消息分发（同 agent 兜底）。
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[ping] panic: %v\n%s", r, debug.Stack())
			}
		}()
		md, text, err := tools.PingHostRender(c.Context, target, 4, pingAllow)
		if err != nil {
			_, _ = c.ReplyText("ping 失败：" + err.Error())
			return
		}
		// 优先 markdown（渲染更好看）；群/私聊公域语料审核拒发时回退纯文本。
		if _, err := c.ReplyMarkdown(md); err != nil {
			_, _ = c.ReplyText(text)
		}
	}()
}
