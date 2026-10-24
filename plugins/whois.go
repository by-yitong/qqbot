package plugins

import (
	"log"
	"runtime/debug"
	"strings"

	"qqbot/internal/agent/tools"
	"qqbot/internal/bot"
)

// Whois 命令插件：/whois <域名/IP> 直查注册信息（与 agent 的 whois 工具共用实现，
// 命令直达、不烧 AI token）。
type Whois struct{}

// NewWhois 创建 Whois 插件。
func NewWhois() *Whois { return &Whois{} }

// Install 注册命令。
func (p *Whois) Install(b *bot.Bot) {
	b.Command("whois", p.run).
		Desc("查询域名/IP 的 whois 注册信息（注册商、注册与到期时间、NS）").
		Category(bot.CatGeneral)
}

func (p *Whois) run(c *bot.Ctx) {
	domain := strings.TrimSpace(c.Args.Join())
	if domain == "" {
		_, _ = c.ReplyText("用法：whois <域名或 IP>，如 whois baidu.com")
		return
	}
	// whois 要连服务器查询（最长 ~20s），异步跑避免堵住消息分发（同 agent 兜底）。
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[whois] panic: %v\n%s", r, debug.Stack())
			}
		}()
		md, text, err := tools.WhoisRender(c.Context, domain, 0)
		if err != nil {
			_, _ = c.ReplyText("whois 失败：" + err.Error())
			return
		}
		// 优先 markdown（记录放代码块里保持逐行排版）；审核拒发时回退纯文本。
		if _, err := c.ReplyMarkdown(md); err != nil {
			_, _ = c.ReplyText(text)
		}
	}()
}
