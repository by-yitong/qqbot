package bot

// 处理器与中间件类型。
type (
	// MessageHandler 处理一条消息（监听器、关键词、命令内部都用它）。
	MessageHandler func(c *Ctx)

	// CommandHandler 处理一条匹配到的命令；参数从 c.Args 取。
	CommandHandler func(c *Ctx)

	// Middleware 包裹一个 MessageHandler；不调用 next 即短路（终止后续处理）。
	Middleware func(next MessageHandler) MessageHandler
)

// msgListener 是带过滤条件的消息监听器。
type msgListener struct {
	filter string // "", "group", "channel", "private"
	h      MessageHandler
}

// kwListener 是关键词触发器。
type kwListener struct {
	kw string
	h  MessageHandler
}

// Plugin 是可注册到 Bot 的一组功能。Install 内部用 b.Command / b.OnKeyword 等
// 注册本插件的能力。
type Plugin interface {
	Install(b *Bot)
}

func (b *Bot) addListener(filter string, h MessageHandler) {
	b.mu.Lock()
	b.listeners = append(b.listeners, msgListener{filter: filter, h: h})
	b.mu.Unlock()
}

// OnMessage 注册一个处理所有消息的监听器。
func (b *Bot) OnMessage(h MessageHandler) { b.addListener("", h) }

// OnGroupMessage 注册一个仅处理群消息的监听器。
func (b *Bot) OnGroupMessage(h MessageHandler) { b.addListener("group", h) }

// OnPrivateMessage 注册一个仅处理私聊消息的监听器。
func (b *Bot) OnPrivateMessage(h MessageHandler) { b.addListener("private", h) }

// OnChannelMessage 注册一个仅处理频道消息的监听器。
func (b *Bot) OnChannelMessage(h MessageHandler) { b.addListener("channel", h) }

// OnKeyword 注册一个关键词触发器：当消息文本包含 kw 时触发。
func (b *Bot) OnKeyword(kw string, h MessageHandler) {
	b.mu.Lock()
	b.keywords = append(b.keywords, kwListener{kw: kw, h: h})
	b.mu.Unlock()
}

// Use 注册全局中间件，按注册顺序包裹命令与监听器（先注册的更靠外、更先执行）。
func (b *Bot) Use(mw ...Middleware) *Bot {
	b.mu.Lock()
	b.middlewares = append(b.middlewares, mw...)
	b.mu.Unlock()
	return b
}

// UsePlugin 注册一个或多个插件，依次调用其 Install（nil 插件跳过，便于按条件装配）。
func (b *Bot) UsePlugin(ps ...Plugin) *Bot {
	installed := make([]Plugin, 0, len(ps))
	b.mu.Lock()
	for _, p := range ps {
		if p == nil {
			continue
		}
		b.plugins = append(b.plugins, p)
		installed = append(installed, p)
	}
	b.mu.Unlock()
	for _, p := range installed {
		p.Install(b)
	}
	return b
}
