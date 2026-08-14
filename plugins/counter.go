package plugins

import (
	"fmt"

	"qqbot/internal/bot"
)

// Counter 用会话状态记录每个人的计数，演示多轮状态：
//
//	/count  → 个人计数 +1 并回复
//	/reset  → 清零个人计数
type Counter struct{}

// NewCounter 创建 Counter 插件。
func NewCounter() *Counter { return &Counter{} }

// Install 注册命令。
func (p *Counter) Install(b *bot.Bot) {
	b.Command("count", p.count).Desc("累加你的个人计数")
	b.Command("reset", p.reset).Desc("清零你的计数")
}

func (p *Counter) count(c *bot.Ctx) {
	st := c.Bot.UserState(c.UserID())
	n := st.GetInt("count") + 1
	st.Set("count", n)
	_, _ = c.ReplyText(fmt.Sprintf("你已计数 %d 次", n))
}

func (p *Counter) reset(c *bot.Ctx) {
	c.Bot.UserState(c.UserID()).Delete("count")
	_, _ = c.ReplyText("已清零")
}
