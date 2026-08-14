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
	name    string
	aliases []string
	desc    string
	usage   string
	hidden  bool
	handler CommandHandler
	mw      []Middleware
	sc      scope
	bot     *Bot
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
// 群聊规则（与 agent 兜底一致）：非 @消息 必须带前缀才匹配命令，
// 避免普通群消息碰巧等于命令名而触发；@消息/私聊/频道 前缀可选。
// 例外：群普通消息**整条恰好等于**命令名/别名时放行——指令面板点击填入的就是
// 命令名本身（如「状态」），不放行的话面板在群里点了没反应。
func (b *Bot) matchCommand(c *Ctx) (*Command, Args, bool) {
	text := c.Content
	prefixHit := false
	if p := b.matchPrefix(text); p != "" {
		text = strings.TrimSpace(text[len(p):])
		prefixHit = true
	}
	if c.IsGroup() && !c.AtBot() && !prefixHit {
		if len(strings.Fields(text)) != 1 {
			return nil, Args{}, false
		}
		return b.parseCommand(text)
	}
	return b.parseCommand(text)
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

// Help 返回所有非隐藏命令的帮助文本。
func (b *Bot) Help() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	pref := ""
	if len(b.prefixes) > 0 {
		pref = b.prefixes[0]
	}
	var sb strings.Builder
	sb.WriteString("命令列表：\n")
	for _, c := range b.cmds {
		if c.hidden {
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
			line += " — " + c.desc
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	return sb.String()
}
