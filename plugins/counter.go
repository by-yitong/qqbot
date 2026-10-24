package plugins

import (
	"fmt"

	"qqbot/internal/bot"
)

// Counter 用会话状态记录每个人的计数，演示多轮状态：
//
//	/count  → 个人计数 +1 并回复
//	/清零   → 清零个人计数
type Counter struct{}

// NewCounter 创建 Counter 插件。
func NewCounter() *Counter { return &Counter{} }

// Install 注册命令。清零命令叫「清零」不叫 reset：session 插件后注册同名 reset
// （清空对话）会把裸「reset」从命令表里覆盖掉，counter 的就永远不可达了。
func (p *Counter) Install(b *bot.Bot) {
	b.Command("count", p.count).Desc("累加你的个人计数").Category(bot.CatFun)
	b.Command("清零", p.reset).Desc("清零你的计数").Category(bot.CatFun)
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
