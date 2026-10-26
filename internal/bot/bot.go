package bot

import (
	"context"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"
	"qqbot/pkg/botgo"
	"qqbot/pkg/botgo/dto"
	"qqbot/pkg/botgo/dto/message"
	"qqbot/pkg/botgo/event"
	"qqbot/pkg/botgo/openapi"
	"qqbot/pkg/botgo/token"

	"qqbot/internal/imgstore"
)

// Config 是连接官方机器人所需的配置。
type Config struct {
	AppID     string
	AppSecret string
}

// Options 是行为配置。
type Options struct {
	Prefixes   []string       // 命令前缀（可选；命中前缀会去掉再匹配）
	EnableHelp bool           // 自动注册 /help，默认 true
	HelpName   string         // help 命令名，默认 "help"
	Owners     []string       // 主人 openid 列表（OwnerOnly 命令仅他们可用；空 = 无人可用）
	MediaHost  *imgstore.Host // 媒体托管：语音自动转码后的 silk 经它发布公网 URL（可选）
}

// Option 用于 New 的可选参数。
type Option func(*Options)

// WithPrefixes 设置命令前缀。
func WithPrefixes(p ...string) Option { return func(o *Options) { o.Prefixes = p } }

// WithOwners 设置主人 openid 列表（OwnerOnly 命令仅主人可用）。
// 注意是 fail-closed：不配置则 OwnerOnly 命令对所有人拒绝。
func WithOwners(ids ...string) Option { return func(o *Options) { o.Owners = ids } }

// WithHelp 开关自动 /help。
func WithHelp(enable bool) Option { return func(o *Options) { o.EnableHelp = enable } }

// WithMediaHost 配置媒体托管（图床）：非 silk 语音自动转码后的产物经它发布公网 URL。
// 未配置时仅能直接发送已是 silk 且自带公网 URL 的语音。
func WithMediaHost(h *imgstore.Host) Option { return func(o *Options) { o.MediaHost = h } }

// Bot 是基于官方 botgo 的插件框架。
type Bot struct {
	cfg      Config
	opts     Options
	prefixes []string
	helpName string

	api openapi.OpenAPI // 在 Run 中就绪

	appID     string         // 在 Run 中就绪（上传富媒体 X-Union-Appid 头）
	mediaHost *imgstore.Host // 媒体托管（WithMediaHost；语音转码产物发布公网 URL 用）

	mu          sync.RWMutex
	cmds        []*Command
	cmdIndex    map[string]*Command
	listeners   []msgListener
	keywords    []kwListener
	middlewares []Middleware
	plugins     []Plugin

	memberAddHandlers []GroupMemberAddHandler   // 群成员加入事件（groupevent.go）
	joinReqHandlers   []GroupJoinRequestHandler // 入群申请事件（groupevent.go）

	startHooks []StartHook // api 就绪后在 Run 中触发（启动通知等）

	state *stateStore

	owners map[string]struct{} // 主人 openid 集合（New 时定死，只读无需锁）

	seq       atomic.Uint32
	startOnce sync.Once
	selfMu    sync.Mutex // 保护 selfID（Me() 不在锁内做，失败不缓存、下次重试）
	selfID    string     // 机器人自身 openid（Mentions() 排除自己用；空 = 未知，不排除）
}

// New 创建一个 Bot。
func New(cfg Config, opts ...Option) *Bot {
	o := Options{Prefixes: []string{"/"}, EnableHelp: true, HelpName: "help"}
	for _, opt := range opts {
		opt(&o)
	}
	prefixes := make([]string, 0, len(o.Prefixes))
	for _, p := range o.Prefixes {
		if p != "" {
			prefixes = append(prefixes, p)
		}
	}
	if len(prefixes) == 0 {
		prefixes = []string{"/"}
	}
	// 按长度降序，避免短前缀吞掉长前缀。
	sort.Slice(prefixes, func(i, j int) bool { return len(prefixes[i]) > len(prefixes[j]) })

	owners := make(map[string]struct{}, len(o.Owners))
	for _, id := range o.Owners {
		if id != "" {
			owners[id] = struct{}{}
		}
	}

	return &Bot{
		cfg:       cfg,
		opts:      o,
		prefixes:  prefixes,
		helpName:  o.HelpName,
		owners:    owners,
		mediaHost: o.MediaHost,
		cmdIndex:  map[string]*Command{},
		state:     newStateStore(),
	}
}

// IsOwner 报告 userID 是否主人（WithOwners 配置的 openid 集合）。
func (b *Bot) IsOwner(userID string) bool {
	_, ok := b.owners[userID]
	return ok
}

// Owners 返回主人 openid 列表（排序保稳定；未配置返回 nil）。
// owners 在 New 时定死、此后只读，无需加锁。
func (b *Bot) Owners() []string {
	if len(b.owners) == 0 {
		return nil
	}
	out := make([]string, 0, len(b.owners))
	for id := range b.owners {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// NC 返回底层 openapi.OpenAPI（需要直接调官方接口时用）。连接前为 nil。
func (b *Bot) NC() openapi.OpenAPI { return b.api }

// openapi 返回已就绪的 OpenAPI 实例；未连接（Run 之前/连接失败）报 ErrNotConnected。
// 本包各协议委托方法（群管理/菜单/上传/撤回）统一经此做连接保护。
func (b *Bot) openapi() (openapi.OpenAPI, error) {
	if b.api == nil {
		return nil, ErrNotConnected
	}
	return b.api, nil
}

// selfOpenID 返回机器人自身的 openid（Me() 拉取后缓存；失败不缓存，下次重试）。
// api 未就绪或拉取失败返回空（此时 Mentions() 不排除自身）。
func (b *Bot) selfOpenID() string {
	b.selfMu.Lock()
	id := b.selfID
	b.selfMu.Unlock()
	if id != "" || b.api == nil {
		return id
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	u, err := b.api.Me(ctx)
	if err != nil {
		log.Printf("bot: 获取自身信息失败（Mentions 将包含机器人自身，稍后重试）: %v", err)
		return ""
	}
	if u == nil || u.ID == "" {
		return ""
	}
	b.selfMu.Lock()
	b.selfID = u.ID
	b.selfMu.Unlock()
	return u.ID
}

// StartHook 是启动钩子：在 Run 中 api 就绪后触发（ctx 是 Run 的 ctx，随停止而取消）。
// 适合做「机器人已上线」通知等一次性初始化。
type StartHook func(ctx context.Context)

// OnStart 注册一个启动钩子（api 就绪后在 Run 中异步触发）。
func (b *Bot) OnStart(h StartHook) {
	b.mu.Lock()
	b.startHooks = append(b.startHooks, h)
	b.mu.Unlock()
}

// fireStart 异步触发所有启动钩子；单个钩子 panic/阻塞 不影响连接与其他钩子。
func (b *Bot) fireStart(ctx context.Context) {
	b.mu.RLock()
	hooks := append([]StartHook(nil), b.startHooks...)
	b.mu.RUnlock()
	for _, h := range hooks {
		go func(hook StartHook) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("bot: OnStart 钩子 panic: %v", r)
				}
			}()
			hook(ctx)
		}(h)
	}
}

// SendChannelText 主动往子频道发一条文本（仅频道支持主动发送；
// 群/C2C 官方机器人只支持被动回复，无主动推送接口）。
// 返回新消息 id 与错误。
func (b *Bot) SendChannelText(ctx context.Context, channelID, text string) (string, error) {
	if b.api == nil {
		return "", ErrNotConnected
	}
	msg := &dto.MessageToCreate{Content: text, MsgType: dto.TextMsg}
	m, err := b.api.PostMessage(ctx, channelID, msg)
	return msgID(m), err
}

// SendGroupText 发送群消息：msgAnchor 非空=被动回复（5 分钟内某条群消息 id）；
// msgAnchor 为空=主动消息（需在 q.qq.com 开通主动消息权限，受官方频控/额度限制）。
// 返回新消息 id 与错误。
func (b *Bot) SendGroupText(ctx context.Context, groupID, msgAnchor, text string) (string, error) {
	if b.api == nil {
		return "", ErrNotConnected
	}
	msg := &dto.MessageToCreate{
		Content: text,
		MsgType: dto.TextMsg,
		MsgID:   msgAnchor,
		MsgSeq:  b.nextSeq(),
	}
	m, err := b.api.PostGroupMessage(ctx, groupID, msg)
	return msgID(m), err
}

// SendC2CText 发送私聊消息：msgAnchor 非空=被动回复（5 分钟内某条私聊消息 id）；
// msgAnchor 为空=主动消息（需在 q.qq.com 开通主动消息权限，受官方频控/额度限制）。
// 返回新消息 id 与错误。
func (b *Bot) SendC2CText(ctx context.Context, userID, msgAnchor, text string) (string, error) {
	if b.api == nil {
		return "", ErrNotConnected
	}
	msg := &dto.MessageToCreate{
		Content: text,
		MsgType: dto.TextMsg,
		MsgID:   msgAnchor,
		MsgSeq:  b.nextSeq(),
	}
	m, err := b.api.PostC2CMessage(ctx, userID, msg)
	return msgID(m), err
}

// SendChannelMarkdown 主动往子频道发一条原生 markdown（MsgType=2）。频道相对宽松。
func (b *Bot) SendChannelMarkdown(ctx context.Context, channelID, md string) (string, error) {
	if b.api == nil {
		return "", ErrNotConnected
	}
	msg := &dto.MessageToCreate{MsgType: dto.MarkdownMsg, Markdown: &dto.Markdown{Content: md}}
	m, err := b.api.PostMessage(ctx, channelID, msg)
	return msgID(m), err
}

// SendGroupMarkdown 发群原生 markdown：msgAnchor 空=主动（需权限，可能被审核）；
// 非空=被动回复（校验语料）。
func (b *Bot) SendGroupMarkdown(ctx context.Context, groupID, msgAnchor, md string) (string, error) {
	if b.api == nil {
		return "", ErrNotConnected
	}
	msg := &dto.MessageToCreate{
		MsgType:  dto.MarkdownMsg,
		Markdown: &dto.Markdown{Content: md},
		MsgID:    msgAnchor,
		MsgSeq:   b.nextSeq(),
	}
	m, err := b.api.PostGroupMessage(ctx, groupID, msg)
	return msgID(m), err
}

// SendC2CMarkdown 发私聊原生 markdown：msgAnchor 空=主动（需权限，可能被审核）；
// 非空=被动回复（校验语料）。
func (b *Bot) SendC2CMarkdown(ctx context.Context, userID, msgAnchor, md string) (string, error) {
	if b.api == nil {
		return "", ErrNotConnected
	}
	msg := &dto.MessageToCreate{
		MsgType:  dto.MarkdownMsg,
		Markdown: &dto.Markdown{Content: md},
		MsgID:    msgAnchor,
		MsgSeq:   b.nextSeq(),
	}
	m, err := b.api.PostC2CMessage(ctx, userID, msg)
	return msgID(m), err
}

// nextSeq 返回递增的消息序号（被动回复去重用）。
func (b *Bot) nextSeq() uint32 { return b.seq.Add(1) }

// Run 启动 access token 刷新、建立 WSS 连接并阻塞，直到 ctx 被取消。
func (b *Bot) Run(ctx context.Context) error {
	creds := &token.QQBotCredentials{AppID: b.cfg.AppID, AppSecret: b.cfg.AppSecret}
	ts := token.NewQQBotTokenSource(creds)
	if err := token.StartRefreshAccessToken(ctx, ts); err != nil {
		return err
	}
	// botgo 默认 5s，但 /files 富媒体接口是腾讯服务端先拉取 URL 再回包，
	// 大视频常超 5s（2026-10 生产实测），放宽到 60s；普通消息调用 1-3s 返回不受影响。
	b.api = botgo.NewOpenAPI(creds.AppID, ts).WithTimeout(60 * time.Second)
	b.appID = creds.AppID

	b.startOnce.Do(func() {
		b.mu.RLock()
		_, exists := b.cmdIndex[b.helpName]
		b.mu.RUnlock()
		if b.opts.EnableHelp && !exists {
			b.Command(b.helpName, func(c *Ctx) {
				c.ReplyTaggedOrPlain(c.Bot.HelpMarkdownFor(c), c.Bot.HelpFor(c))
			}).Desc("显示命令列表").Category(CatGeneral)
		}
	})

	// 注册 botgo 事件处理器：把官方消息转成 *Ctx 走框架分发。
	intent := event.RegisterHandlers(
		event.GroupATMessageEventHandler(b.onGroupAtMessage), // 群 @ 消息
		event.C2CMessageEventHandler(b.onC2CMessage),         // 私聊
		event.ATMessageEventHandler(b.onChannelAtMessage),    // 频道 @ 消息
	)
	// 群全量消息（GROUP_MESSAGE_CREATE，无需 @）：botgo v0.2.1 无内置类型，
	// 用通用 dispatch 注册；intent 已含（IntentGroupMessages 覆盖，需 q.qq.com 开通全量权限）。
	botgo.RegisterDispatchEventHandler("GROUP_MESSAGE_CREATE", b.onGroupMessage)
	// 群成员加入 / 入群申请事件（官方 2026-08 新增，同样不在 botgo v0.2.1 内置类型里）。
	// 入群申请与群消息同 intent（1<<25，已含）；群成员事件需补 1<<24，
	// 只在注册了监听器时申请（未开通该权限的机器人带上会连不上 WSS）。
	botgo.RegisterDispatchEventHandler("GROUP_MEMBER_ADD", b.onGroupMemberAdd)
	botgo.RegisterDispatchEventHandler("GROUP_JOIN_REQUEST", b.onGroupJoinRequest)
	if b.wantGroupMemberEvent() {
		intent |= IntentGroupMemberEvent
	}

	ap, err := b.api.WS(ctx, map[string]string{}, "")
	if err != nil {
		return err
	}
	manager := botgo.NewSessionManager()
	log.Printf("bot: 正在连接官方机器人 WSS (appid=%s) ...", b.cfg.AppID)
	// api 已就绪：触发启动钩子（如「机器人已上线」通知）。频道主动消息走 HTTP，无需等 WSS。
	go b.fireStart(ctx)
	// botgo v0.2.1 的 SessionManager.Start 内部是无限 chan receive，永不返回
	// （SIGQUIT 栈证实卡在 local.go:52），同步调用会挡住 <-ctx.Done()，导致
	// SIGINT 后进程永远不退（此前 air 靠超时强杀）。放进 goroutine，主流程等信号。
	startErr := make(chan error, 1)
	go func() { startErr <- manager.Start(ap, ts, &intent) }()
	select {
	case err := <-startErr:
		return err
	case <-ctx.Done():
		log.Println("bot 已停止")
		return ctx.Err()
	}
}

// --- botgo 事件入口 -> *Ctx ---

func (b *Bot) onGroupAtMessage(payload *dto.WSPayload, data *dto.WSGroupATMessageData) error {
	c := b.fromMessage(context.Background(), "group", (*dto.Message)(data), true)
	b.EnrichGroupRoles(c, payload.RawMessage)
	b.dispatch(c)
	return nil
}

func (b *Bot) onC2CMessage(_ *dto.WSPayload, data *dto.WSC2CMessageData) error {
	b.dispatch(b.fromMessage(context.Background(), "c2c", (*dto.Message)(data), false))
	return nil
}

func (b *Bot) onChannelAtMessage(_ *dto.WSPayload, data *dto.WSATMessageData) error {
	b.dispatch(b.fromMessage(context.Background(), "channel", (*dto.Message)(data), true))
	return nil
}

// onGroupMessage 处理群全量消息（GROUP_MESSAGE_CREATE，无需 @）。
// botgo v0.2.1 没有内置类型，通过 RegisterDispatchEventHandler 注册，这里自行解析。
// 注意：botgo 传进来的是整个 WSPayload（含 op/s/t/d），必须先取 "d" 再解析，
// 否则顶层无 content/author 等字段，解析出来全空（=event.ParseData 做的事）。
func (b *Bot) onGroupMessage(_ *dto.WSPayload, msg []byte) error {
	var m dto.Message
	if err := event.ParseData(msg, &m); err != nil {
		return err
	}
	// 全量群消息里也可能 @ 了 bot（mentions.is_you=true）；botgo 的 dto.User 未带 is_you 字段，用 gjson 补判。
	atBot := gjson.Get(string(msg), "d.mentions.#(is_you==true)").Exists()
	// 没有 @ 本 bot、却 @ 了其他机器人（如「@其他机器人 /help」）：消息是对别的 bot 说的。
	// cleanContent 会把 @ 结构剥掉，剩下的命令文本会按裸命中规则匹配到本 bot，须整条忽略。
	if !atBot && mentionsOtherBot(msg) {
		return nil
	}
	c := b.fromMessage(context.Background(), "group", &m, atBot)
	c.quote = extractQuote(msg) // 引用回复时，被引用的原文在 msg_elements 里
	b.EnrichGroupRoles(c, msg)
	b.dispatch(c)
	return nil
}

// EnrichGroupRoles 从群消息事件的原始 payload 补齐 botgo v0.2.1 dto 丢弃的群角色字段：
//
//   - 发送者角色 d.author.member_role → Ctx.MemberRole() / IsGroupAdmin()（AdminOnly 权限判断用）；
//   - 被 @ 者的角色与机器人标记 d.mentions[].member_role / bot → Mention.Role / Mention.Bot
//     （如 /踢 前置校验 @ 的是不是群主/管理员）。
//
// 官方 2026-08 起随群消息事件下发；两种群消息入口（GROUP_MESSAGE_CREATE /
// GROUP_AT_MESSAGE_CREATE）均自动调用。raw 为空或字段缺失时静默跳过（保持零值，
// AdminOnly 回落为仅主人）。导出以便测试构造带角色的场景。
func (b *Bot) EnrichGroupRoles(c *Ctx, raw []byte) {
	if len(raw) == 0 || c == nil {
		return
	}
	if r := gjson.GetBytes(raw, "d.author.member_role"); r.Exists() {
		c.memberRole = r.String()
	}
	gjson.GetBytes(raw, "d.mentions").ForEach(func(_, m gjson.Result) bool {
		id := m.Get("member_openid").String()
		if id == "" {
			id = m.Get("id").String()
		}
		if id == "" {
			return true
		}
		for i := range c.mentions {
			if c.mentions[i].ID == id {
				c.mentions[i].Role = m.Get("member_role").String()
				c.mentions[i].Bot = m.Get("bot").Bool()
			}
		}
		return true
	})
}

// mentionsOtherBot 报告群消息事件的 mentions 里是否 @ 了其他机器人
// （d.mentions[].bot=true 且非 is_you）。官方 2026-08 起下发 bot 标记；
// 旧事件缺该字段时返回 false（保持原有行为）。人类 @ 不算（裸文本命令
// 如「/禁言 @某人」依赖 mentions 定位目标，不能被误伤）。
func mentionsOtherBot(raw []byte) bool {
	found := false
	gjson.GetBytes(raw, "d.mentions").ForEach(func(_, m gjson.Result) bool {
		if m.Get("bot").Bool() && !m.Get("is_you").Bool() {
			found = true
			return false // 找到即停
		}
		return true
	})
	return found
}

// extractQuote 从群消息的 msg_elements 提取被引用消息的文本（引用回复场景）。
// 普通消息没有 msg_elements，返回空。
// 附件（文件/图片等）没有 content，只有 attachments：把文件名 + 下载 URL 也拼进去，
// 否则「看下这个」+ 引用一个文件时 agent 看不到任何内容（URL 可经 httpget 工具下载）。
func extractQuote(msg []byte) string {
	var sb strings.Builder
	gjson.GetBytes(msg, "d.msg_elements").ForEach(func(_, el gjson.Result) bool {
		if c := el.Get("content").String(); c != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(c)
		}
		el.Get("attachments").ForEach(func(_, att gjson.Result) bool {
			fn, url := att.Get("filename").String(), att.Get("url").String()
			if fn == "" && url == "" {
				return true
			}
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString("[被引用消息的附件] " + fn + "\n" + url)
			return true
		})
		return true
	})
	return strings.TrimSpace(sb.String())
}

// atAnyRE 匹配 @ 结构：频道 <@!数字> 与群 <@openid>。botgo 的 ETLInput 只处理频道格式，这里兜底群格式。
var atAnyRE = regexp.MustCompile(`<@!?[^>]+>`)

// cleanContent 去掉所有 @ 结构并 trim，得到纯文本。
func cleanContent(s string) string {
	return strings.TrimSpace(atAnyRE.ReplaceAllString(message.ETLInput(s), ""))
}

// fromMessage 把官方消息转成 *Ctx（去掉 @ 前缀文本）。
func (b *Bot) fromMessage(ctx context.Context, source string, m *dto.Message, atBot bool) *Ctx {
	userID, username := "", ""
	if m.Author != nil {
		userID = m.Author.ID
		username = m.Author.Username
	}
	return &Ctx{
		Bot:       b,
		Context:   ctx,
		Source:    source,
		Content:   cleanContent(m.Content),
		userID:    userID,
		username:  username,
		groupID:   m.GroupID,
		channelID: m.ChannelID,
		guildID:   m.GuildID,
		messageID: m.ID,
		ts:        string(m.Timestamp),
		atBot:     atBot,
		images:    extractImages(m.Attachments),
		mentions:  extractMentions(m.Mentions),
	}
}

// extractMentions 把事件的 mentions（被 @ 用户）转成 []Mention（空 id 跳过）。
// 自身是否在列表里由 Mentions() 取值时按 selfOpenID 排除（此处无法判断哪个是自己）。
func extractMentions(us []*dto.User) []Mention {
	if len(us) == 0 {
		return nil
	}
	out := make([]Mention, 0, len(us))
	for _, u := range us {
		if u == nil || u.ID == "" {
			continue
		}
		out = append(out, Mention{ID: u.ID, Username: u.Username})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// extractImages 从附件中筛出图片（content_type 以 image/ 开头，或文件名是图片扩展名）。
// 非图片附件（语音/视频等）跳过。
func extractImages(atts []*dto.MessageAttachment) []Image {
	var imgs []Image
	for _, a := range atts {
		if a == nil || a.URL == "" {
			continue
		}
		if !isImageAttachment(a.ContentType, a.FileName) {
			continue
		}
		imgs = append(imgs, Image{
			URL:         a.URL,
			ContentType: a.ContentType,
			Filename:    a.FileName,
			Width:       a.Width,
			Height:      a.Height,
		})
	}
	return imgs
}

// isImageAttachment 判断附件是否图片：content_type 以 image/ 开头，或文件名带图片扩展名。
func isImageAttachment(contentType, filename string) bool {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if strings.HasPrefix(ct, "image/") {
		return true
	}
	// 兜底：content_type 空时按文件名扩展名判
	name := strings.ToLower(filename)
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// --- 分发管线 ---

// dispatch：全局中间件链包裹 core。
func (b *Bot) dispatch(c *Ctx) {
	h := MessageHandler(b.core)
	b.mu.RLock()
	mws := b.middlewares
	b.mu.RUnlock()
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	h(c)
}

// core：命令优先；未命中命令才走关键词 + 监听器。
func (b *Bot) core(c *Ctx) {
	if cmd, args, ok := b.matchCommand(c); ok {
		c.Args = args
		b.runCommand(c, cmd)
		return
	}
	text := c.Content
	b.mu.RLock()
	kws := b.keywords
	ls := b.listeners
	b.mu.RUnlock()

	for _, kw := range kws {
		if c.aborted {
			return
		}
		if strings.Contains(text, kw.kw) {
			kw.h(c)
		}
	}
	for _, l := range ls {
		if c.aborted {
			return
		}
		switch {
		case l.filter == "":
			l.h(c)
		case l.filter == "group" && c.IsGroup():
			l.h(c)
		case l.filter == "channel" && c.IsChannel():
			l.h(c)
		case l.filter == "private" && c.IsPrivate():
			l.h(c)
		}
	}
}
