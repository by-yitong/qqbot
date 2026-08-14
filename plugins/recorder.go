package plugins

import (
	"time"

	"qqbot/internal/bot"
	"qqbot/internal/memory"
)

// RecorderPlugin 把收到的消息全量异步落库（长期记忆），供 search_history 与近期注入消费。
// 群消息（含未 @bot 的闲聊，需全量消息权限）与私聊都记；频道不记（本 bot 主战场是群/私聊）。
// 独立于 agent：没配 AI 也可以只记录。
type RecorderPlugin struct {
	mem *memory.Store
}

// NewRecorder 构造记录插件。mem 为 nil 时不注册（直接返回 nil，UsePlugin 容忍 nil）。
func NewRecorder(mem *memory.Store) *RecorderPlugin {
	if mem == nil {
		return nil
	}
	return &RecorderPlugin{mem: mem}
}

// Install 注册监听器。
func (p *RecorderPlugin) Install(b *bot.Bot) {
	b.OnGroupMessage(p.record)
	b.OnPrivateMessage(p.record)
}

// record 把一条消息转成 memory.Message 异步落库（MsgID 唯一约束去重）。
func (p *RecorderPlugin) record(c *bot.Ctx) {
	ts, err := time.Parse(time.RFC3339, c.Timestamp())
	if err != nil {
		ts = time.Now() // 事件缺时间戳时用当前时间兜底
	}
	p.mem.Record(memory.Message{
		MsgID:    c.MessageID(),
		Ts:       ts,
		Source:   c.Source,
		GroupID:  c.GroupID(),
		UserID:   c.UserID(),
		Username: c.Username(),
		Content:  c.Text(),
		AtBot:    c.AtBot(),
	})
}
