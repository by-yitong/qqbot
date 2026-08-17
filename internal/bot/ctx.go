package bot

import (
	"context"
	"errors"

	"github.com/tencent-connect/botgo/dto"
)

// ErrNotConnected 在尚未连接（api 未就绪）时由 ReplyText 返回。
var ErrNotConnected = errors.New("bot: 尚未连接，api 未就绪")

// Ctx 是一次消息的处理上下文：把 Bot、context、消息内容、来源、命令参数绑在一起，
// 并提供 ReplyText 等便捷方法。
//
//	b.Command("echo", func(c *bot.Ctx) {
//	    c.ReplyText(c.Args.Join())   // 被动回复（锚定到当前消息）
//	})
type Ctx struct {
	Bot     *Bot
	Context context.Context
	Source  string // "group" | "channel" | "c2c"
	Content string // 已去掉 @ 前缀的纯文本
	Args    Args   // 命令参数（命令触发时填充）

	userID    string // 用户 openid
	username  string // 发送者群昵称（可能为空）
	groupID   string // 群 openid
	channelID string // 子频道 id
	guildID   string // 频道 id
	messageID string // 消息 id（回复锚点）
	ts        string // 消息时间（原始字符串，如 2026-08-14T12:00:02+08:00）

	aborted bool    // 是否已 Abort：阻止后续关键词/监听器执行
	atBot   bool    // 是否 @ 了机器人（群/频道 @消息为 true；全量群消息/私聊为 false）
	quote   string  // 被引用的消息文本（引用回复时，来自 msg_elements）
	images  []Image // 用户发送的图片附件（来自 attachments，按顺序）
}

// Image 是用户在消息里发送的一张图片附件。
type Image struct {
	URL         string // 图片下载地址（QQ 多媒体 CDN，可能带 rkey 时效）
	ContentType string // image/png 等
	Filename    string // 原始文件名
	Width       int    // 像素宽
	Height      int    // 像素高
}

// NewEventCtx 按事件字段构造一条消息上下文（程序化/测试用；正常消息由框架在事件解析时构造）。
func NewEventCtx(b *Bot, source, content, userID, username, groupID, messageID, ts string, atBot bool) *Ctx {
	return &Ctx{
		Bot:       b,
		Context:   context.Background(),
		Source:    source,
		Content:   content,
		userID:    userID,
		username:  username,
		groupID:   groupID,
		messageID: messageID,
		ts:        ts,
		atBot:     atBot,
	}
}

// Abort 标记终止后续处理：调用后，当前消息后续的关键词与监听器不再执行。
// 命令中间件里调用同样有效（不执行后续中间件与命令 handler）。
// 通常用于「我已处理这条消息，不要再往下走了」（如 AI 兜底后阻止其他监听器）。
func (c *Ctx) Abort() { c.aborted = true }

// Aborted 报告是否已 Abort。
func (c *Ctx) Aborted() bool { return c.aborted }

// AtBot 报告本条消息是否 @ 了机器人（群 GROUP_AT_MESSAGE / 频道 @消息 为 true；
// 全量群消息 GROUP_MESSAGE_CREATE / 私聊为 false）。
func (c *Ctx) AtBot() bool { return c.atBot }

// StripPrefix 若文本以某命令前缀开头，返回去掉前缀后的文本与 true；否则返回原文与 false。
func (c *Ctx) StripPrefix() (string, bool) {
	p := c.Bot.matchPrefix(c.Content)
	if p == "" {
		return c.Content, false
	}
	return c.Content[len(p):], true
}

// Quote 返回被引用的消息文本（引用回复时）；非引用消息返回空。
func (c *Ctx) Quote() string { return c.quote }

// Images 返回用户在当前消息里发送的图片（按附件顺序）；无图片返回 nil。
func (c *Ctx) Images() []Image { return c.images }

// Text 返回消息纯文本。
func (c *Ctx) Text() string { return c.Content }

// IsGroup 是否群消息。
func (c *Ctx) IsGroup() bool { return c.Source == "group" }

// IsChannel 是否频道消息。
func (c *Ctx) IsChannel() bool { return c.Source == "channel" }

// IsPrivate 是否私聊（C2C）消息。
func (c *Ctx) IsPrivate() bool { return c.Source == "c2c" }

// UserID 返回用户 openid。
func (c *Ctx) UserID() string { return c.userID }

// Username 返回发送者群昵称（可能为空；私聊/频道事件常缺）。
func (c *Ctx) Username() string { return c.username }

// MessageID 返回消息 id（回复锚点；也用作聊天记录去重键）。
func (c *Ctx) MessageID() string { return c.messageID }

// Timestamp 返回消息时间原始串（如 2026-08-14T12:00:02+08:00；可能为空）。
func (c *Ctx) Timestamp() string { return c.ts }

// GroupID 返回群 openid。
func (c *Ctx) GroupID() string { return c.groupID }

// ChannelID 返回子频道 id。
func (c *Ctx) ChannelID() string { return c.channelID }

// ReplyText 以纯文本回复当前消息（被动回复：锚定到当前消息 id，需在收到消息后 5 分钟内）。
// 返回新消息 id 与错误。
func (c *Ctx) ReplyText(s string) (string, error) {
	return c.send(&dto.MessageToCreate{Content: s, MsgType: dto.TextMsg})
}

// ReplyMarkdown 以原生 markdown 回复当前消息（被动回复：锚定到当前消息 id）。
// ⚠️ 群/私聊的原生 markdown 公域机器人会被校验语料/异步审核，可能发不出或延迟；
// 频道相对宽松。
func (c *Ctx) ReplyMarkdown(md string) (string, error) {
	return c.send(&dto.MessageToCreate{
		MsgType:  dto.MarkdownMsg,
		Markdown: &dto.Markdown{Content: md},
	})
}

// ReplyImage 以图片回复当前消息：下载 imageURL → 上传富媒体 → 发 MsgType=7。
// 仅群/私聊支持；频道 v1 不支持（返回 ErrChannelImageUnsupported）。
func (c *Ctx) ReplyImage(imageURL string) (string, error) {
	switch c.Source {
	case "group":
		return c.Bot.SendGroupImage(c.Context, c.groupID, c.messageID, imageURL)
	case "c2c":
		return c.Bot.SendC2CImage(c.Context, c.userID, c.messageID, imageURL)
	default:
		return "", ErrChannelImageUnsupported
	}
}

// ReplyVideo 以视频回复当前消息（file_type=2，URL 模式）。仅群/私聊支持。
func (c *Ctx) ReplyVideo(videoURL string) (string, error) {
	switch c.Source {
	case "group":
		return c.Bot.SendGroupVideo(c.Context, c.groupID, c.messageID, videoURL)
	case "c2c":
		return c.Bot.SendC2CVideo(c.Context, c.userID, c.messageID, videoURL)
	default:
		return "", ErrChannelImageUnsupported
	}
}

// send 按来源分支发送一条消息，自动带上被动回复锚点（MsgID/MsgSeq/引用）。
func (c *Ctx) send(msg *dto.MessageToCreate) (string, error) {
	api := c.Bot.api
	if api == nil {
		return "", ErrNotConnected
	}
	msg.MsgID = c.messageID
	msg.MsgSeq = c.Bot.nextSeq()
	msg.MessageReference = &dto.MessageReference{
		MessageID:             c.messageID,
		IgnoreGetMessageError: true,
	}
	switch c.Source {
	case "group":
		m, err := api.PostGroupMessage(c.Context, c.groupID, msg)
		return msgID(m), err
	case "channel":
		m, err := api.PostMessage(c.Context, c.channelID, msg)
		return msgID(m), err
	default: // c2c
		m, err := api.PostC2CMessage(c.Context, c.userID, msg)
		return msgID(m), err
	}
}

func msgID(m *dto.Message) string {
	if m == nil {
		return ""
	}
	return m.ID
}
