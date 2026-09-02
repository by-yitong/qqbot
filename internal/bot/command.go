package bot

import (
	"strings"
)

// Args 是命令解析后的参数。
type Args struct {
	Name string   // 实际触发的命令名（可能是别名）
	Raw  string   // 命令名之后的整段文本（未拆分）
	Args []string // 按空白拆分的参数列表
}

// Get 返回第 i 个参数，越界返回 ""。
func (a Args) Get(i int) string {
	if i < 0 || i >= len(a.Args) {
		return ""
	}
	return a.Args[i]
}

// Join 返回命令名之后的整段文本，等价于 Raw。
func (a Args) Join() string { return a.Raw }

// scope 记录命令的作用域限制。
type scope struct {
	groupOnly   bool
	privateOnly bool
	channelOnly bool
}

// Command 是一个已注册的命令。
type Command struct {
	name        string
	aliases     []string
	desc        string
	usage       string
	hidden      bool
	hideBotMenu bool
	requireAt   bool     // 必须命中前缀或 @机器人 才触发（默认 false：裸文本也命中）
	ownerOnly   bool     // 仅主人（WithOwners 配置的 openid）可用；未配置主人 = 无人可用
	handler     CommandHandler
	mw          []Middleware
	sc          scope
	bot         *Bot
}

// Desc 设置命令描述（用于 /help）。
func (c *Command) Desc(s string) *Command { c.desc = s; return c }

// Usage 设置用法示例（用于 /help）。
func (c *Command) Usage(s string) *Command { c.usage = s; return c }

// Alias 添加别名。
func (c *Command) Alias(names ...string) *Command {
	c.aliases = append(c.aliases, names...)
	c.bot.registerAliases(c, names)
	return c
}

// Hidden 让命令不出现在 /help 中。
func (c *Command) Hidden() *Command { c.hidden = true; return c }

// HideBotMenu 让命令不自动注册到 QQ bot 自定义菜单/指令面板（不影响 /help）。
func (c *Command) HideBotMenu() *Command { c.hideBotMenu = true; return c }

// RequireAtOrPrefix 要求该命令必须带命令前缀或 @机器人 才触发（默认关闭）。
//
// 默认情况下命令「裸文本也可命中」：发送 xxx 等价 .xxx，群里不用 @ 不用前缀，
// 指令面板点击、免前缀直达都靠它。名字是常用词、容易被群聊误触的命令可显式
// 收紧成必须前缀/@（AI 兜底等无命令功能不走命令表，本来就要求 @ 或前缀）。
func (c *Command) RequireAtOrPrefix() *Command { c.requireAt = true; return c }

// OwnerOnly 限制命令仅主人可用（WithOwners 配置的 openid，环境变量 BOT_OWNERS）。
// fail-closed：未配置主人时对所有人拒绝。此类命令同时不进 /help（非主人视角）
// 与自动菜单/指令面板（面板所有用户可见，主人命令不该出现在那里）。
func (c *Command) OwnerOnly() *Command { c.ownerOnly = true; return c }

// Use 为该命令追加专属中间件（在作用域检查之后执行）。
func (c *Command) Use(mw ...Middleware) *Command { c.mw = append(c.mw, mw...); return c }

// GroupOnly 限制命令仅在群聊可用。
func (c *Command) GroupOnly() *Command { c.sc.groupOnly = true; return c }

// PrivateOnly 限制命令仅在私聊可用。
func (c *Command) PrivateOnly() *Command { c.sc.privateOnly = true; return c }

// ChannelOnly 限制命令仅在频道可用。
func (c *Command) ChannelOnly() *Command { c.sc.channelOnly = true; return c }

// Command 注册一个命令，返回 *Command 供链式配置。
func (b *Bot) Command(name string, h CommandHandler) *Command {
	c := &Command{name: name, handler: h, bot: b}
	b.mu.Lock()
	b.cmds = append(b.cmds, c)
	b.cmdIndex[name] = c
	b.mu.Unlock()
	return c
}

func (b *Bot) registerAliases(c *Command, names []string) {
	b.mu.Lock()
	for _, n := range names {
		b.cmdIndex[n] = c
	}
	b.mu.Unlock()
}

// matchCommand 在 c.Content 上匹配命令：可选去掉一个前缀，再按首 token 查命令表。
//
// 命令默认「裸文本也可命中」：不带前缀、不 @ 也能触发（发送 xxx 等价 .xxx，
// 指令面板点击填入的就是命令名本身）。显式 RequireAtOrPrefix 的命令是例外——
// 必须命中前缀或 @机器人 才匹配（私聊无法 @，即必须带前缀）。
// AI 兜底不走命令表，群消息本来就要求 @ 或前缀，不受此放宽影响。
func (b *Bot) matchCommand(c *Ctx) (*Command, Args, bool) {
	text := c.Content
	triggered := c.AtBot() // 频道/群 @消息恒 true
	if p := b.matchPrefix(text); p != "" {
		text = strings.TrimSpace(text[len(p):])
		triggered = true
	}
	cmd, args, ok := b.parseCommand(text)
	if !ok {
		return nil, Args{}, false
	}
	if cmd.requireAt && !triggered {
		return nil, Args{}, false
	}
	return cmd, args, true
}

// matchPrefix 返回命中的前缀（已按长度降序）；未命中返回 ""。
func (b *Bot) matchPrefix(text string) string {
	for _, p := range b.prefixes {
		if strings.HasPrefix(text, p) {
			return p
		}
	}
	return ""
}

// parseCommand 把命令文本拆成「命令名 + 参数」并查命令表。
func (b *Bot) parseCommand(cmdText string) (*Command, Args, bool) {
	cmdText = strings.TrimSpace(cmdText)
	if cmdText == "" {
		return nil, Args{}, false
	}
	fields := strings.Fields(cmdText)
	name := fields[0]
	b.mu.RLock()
	c, ok := b.cmdIndex[name]
	b.mu.RUnlock()
	if !ok {
		return nil, Args{}, false
	}
	rest := fields[1:]
	return c, Args{Name: name, Raw: strings.Join(rest, " "), Args: rest}, true
}

// runCommand 执行一条命中的命令：作用域中间件 + 命令专属中间件 + handler。
// 任意中间件调用 c.Abort() 且不调用 next，链路即短路（不执行 handler）。
func (b *Bot) runCommand(c *Ctx, cmd *Command) {
	h := MessageHandler(func(cc *Ctx) {
		if cc.aborted {
			return
		}
		cmd.handler(cc)
	})
	mws := make([]Middleware, 0, len(cmd.mw)+1)
	mws = append(mws, b.scopeMiddleware(cmd))
	mws = append(mws, cmd.mw...)
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	h(c)
}

// scopeMiddleware 把命令的作用域限制实现为中间件：不满足时回复原因并终止。
func (b *Bot) scopeMiddleware(cmd *Command) Middleware {
	return func(next MessageHandler) MessageHandler {
		return func(c *Ctx) {
			if cmd.ownerOnly && !c.IsOwner() {
				if len(b.owners) == 0 {
					_, _ = c.ReplyText("该命令仅主人可用，但尚未配置主人（.env 的 BOT_OWNERS，多个用逗号分隔）")
				} else {
					_, _ = c.ReplyText("该命令仅主人可用")
				}
				return
			}
			switch {
			case cmd.sc.groupOnly && !c.IsGroup():
				_, _ = c.ReplyText("该命令仅在群聊可用")
				return
			case cmd.sc.privateOnly && !c.IsPrivate():
				_, _ = c.ReplyText("该命令仅在私聊可用")
				return
			case cmd.sc.channelOnly && !c.IsChannel():
				_, _ = c.ReplyText("该命令仅在频道可用")
				return
			}
			next(c)
		}
	}
}

// Help 返回所有非隐藏命令的帮助文本（不过滤主人命令；按身份过滤用 HelpFor）。
func (b *Bot) Help() string {
	return b.help(false)
}

// HelpFor 返回面向 c 发送者的帮助文本：非主人看不到 OwnerOnly 命令。
func (b *Bot) HelpFor(c *Ctx) string {
	return b.help(!c.IsOwner())
}

// help 生成帮助文本；hideOwner 为 true 时跳过 OwnerOnly 命令。
func (b *Bot) help(hideOwner bool) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	pref := ""
	if len(b.prefixes) > 0 {
		pref = b.prefixes[0]
	}
	var sb strings.Builder
	sb.WriteString("命令列表：\n")
	for _, c := range b.cmds {
		if c.hidden || (hideOwner && c.ownerOnly) {
			continue
		}
		names := pref + c.name
		if len(c.aliases) > 0 {
			names += "（" + strings.Join(c.aliases, ", ") + "）"
		}
		line := "• " + names
		if c.usage != "" {
			line += "  " + c.usage
		}
		if c.desc != "" {
			line += "  — " + c.desc
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	return sb.String()
}
