// Package bot 是基于腾讯官方 botgo SDK 的 QQ 机器人插件框架。
//
// 它在官方机器人 API 之上提供命令分发、中间件、监听器、插件与会话状态，让你写功能时
// 只关心业务本身。botgo 负责连接与收发，bot 负责应用结构。
//
// 官方机器人约束：只能被动回复（收到消息后 5 分钟内回复）、群消息需 @、ID 是 openid。
//
// 快速开始：
//
//	b := bot.New(bot.Config{AppID: appID, AppSecret: secret}, bot.WithPrefixes("/"))
//
//	// 1) 函数式：一行注册命令
//	b.Command("ping", ping).Desc("测试在线").Alias("p")
//	b.OnKeyword("早安", morning)
//
//	// 2) 插件式：复杂功能封装成结构体
//	b.UsePlugin(&Echo{})
//
//	if err := b.Run(ctx); err != nil && err != context.Canceled {
//	    log.Fatal(err)
//	}
//
// 命令触发：群消息需 @ 机器人（官方强制），然后可选前缀（默认 "/"）；私聊直接匹配命令。
//
// 写一个插件：
//
//	type Echo struct{}
//	func (p *Echo) Install(b *bot.Bot) {
//	    b.Command("echo", p.run).Desc("复读").Usage("/echo <文本>")
//	}
//	func (p *Echo) run(c *bot.Ctx) {
//	    c.ReplyText(c.Args.Join())
//	}
package bot
