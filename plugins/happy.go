package plugins

import (
	"context"
	"fmt"
	"log"

	"qqbot/internal/bot"
	"qqbot/internal/scheduler"
	"qqbot/internal/utils"

	"github.com/tidwall/gjson"
)

// Happy 是「一言」插件示例：用 utils.HttpClient + gjson 取一句话回复；
// 网络失败时用内置句子兜底。演示动态 JSON（.Get）访问与 c.ReplyText 便捷方法。
//
// 同时是「插件侧定时任务」的使用示范：注入 scheduler 后声明一个每天 08:30 的每日
// 笑话任务，HAPPY_DAILY_TARGET 指定发送目标（group:群openid / channel:子频道id，
// 留空不启用）。任务落盘、重启自动恢复；Ensure 幂等——同名任务已存在时不覆盖
// （用户对开关/时间的修改保留）。
type Happy struct {
	sched       *scheduler.Scheduler // 可为 nil：未启用定时任务
	dailyTarget string               // 每日笑话发送目标；空=不注册
	bot         *bot.Bot             // Install 时捕获，定时处理器主动发消息用
}

// NewHappy 创建 Happy 插件（sched 可为 nil，dailyTarget 为空则不注册定时任务）。
func NewHappy(sched *scheduler.Scheduler, dailyTarget string) *Happy {
	return &Happy{sched: sched, dailyTarget: dailyTarget}
}

// 内置兜底句子（API 不通时随机取一句，保证命令永远有回复）。
var happyFallback = []string{
	"今天天气晴朗，心情愉快！",
	"愿你历尽千帆，归来仍是少年。",
	"生活明朗，万物可爱。",
	"保持热爱，奔赴山海。",
}

// Install 注册命令；配置了发送目标时再注册每日笑话定时任务。
func (p *Happy) Install(b *bot.Bot) {
	p.bot = b
	b.Command("一言", p.run).
		Desc("随机一句话（来自 hitokoto.cn）").
		Alias("hitokoto", "yy")
	b.Command("test", p.test)
	if p.sched == nil || p.dailyTarget == "" {
		return
	}
	tg, err := scheduler.ParseTarget(p.dailyTarget)
	if err != nil {
		log.Printf("[happy] HAPPY_DAILY_TARGET 不合法（应为 group:id / channel:id / c2c:id）: %v", err)
		return
	}
	p.sched.Handle("happy.daily", p.daily)
	p.sched.Ensure(scheduler.Task{
		Name:     "每日笑话",
		Handler:  "happy.daily",
		Schedule: scheduler.Schedule{Type: scheduler.ScheduleDaily, At: "08:30"},
		Target:   &tg,
	})
}

func (p *Happy) test(c *bot.Ctx) {
	const kb = `{"content":{"rows":[
{"buttons":[
{"id":"b1","render_data":{"label":"📊排行","visited_label":"排行","style":0},"action":{"type":2,"permission":{"type":2},"data":"/排行","reply":true,"enter":true},"style":{"font_size":"small"}},
{"id":"b2","render_data":{"label":"🎵点歌","visited_label":"点歌","style":1},"action":{"type":2,"permission":{"type":2},"data":"/点歌","reply":true,"enter":true},"style":{"font_size":"small"}},
{"id":"b3","render_data":{"label":"🔊语音","visited_label":"语音","style":0},"action":{"type":2,"permission":{"type":2},"data":"/转语音","reply":true,"enter":true},"style":{"font_size":"small"}}]},
{"buttons":[
{"id":"b4","render_data":{"label":"❓帮助","visited_label":"帮助","style":0},"action":{"type":2,"permission":{"type":2},"data":"/帮助","reply":true,"enter":true},"style":{"font_size":"small"}},
{"id":"b5","render_data":{"label":"👤我的","visited_label":"我的","style":0},"action":{"type":2,"permission":{"type":2},"data":"/我的","reply":true,"enter":true},"style":{"font_size":"small"}},
{"id":"b6","render_data":{"label":"⚙️设置","visited_label":"设置","style":0},"action":{"type":2,"permission":{"type":2},"data":"/设置","reply":true,"enter":true},"style":{"font_size":"small"}}]}],
"style":{"font_size":"small"}}}`
	id, err := c.ReplyRowJson(kb)
	if err != nil {
		c.ReplyText("按钮消息发送失败：" + err.Error())
		return
	}
	_ = id
}

func (p *Happy) run(c *bot.Ctx) {
	text, err := fetchHitokoto()
	if err != nil {
		c.ReplyText("请求失败，" + err.Error())
		return
	}
	c.ReplyText(text)
}

// daily 是 happy.daily 定时处理器：取一句一言，主动发到配置的目标聊天。
func (p *Happy) daily(ctx context.Context, t scheduler.Task) error {
	if t.Target == nil {
		return fmt.Errorf("任务缺少发送目标")
	}
	text, err := fetchHitokoto()
	if err != nil {
		return err
	}
	switch t.Target.Source {
	case "group":
		_, err = p.bot.SendGroupText(ctx, t.Target.ID, "", text)
	case "c2c":
		_, err = p.bot.SendC2CText(ctx, t.Target.ID, "", text)
	case "channel":
		_, err = p.bot.SendChannelText(ctx, t.Target.ID, text)
	default:
		return fmt.Errorf("未知发送目标类型 %q", t.Target.Source)
	}
	return err
}

// fetchHitokoto 从 hitokoto.cn 取一句话（含出处署名）。
func fetchHitokoto() (string, error) {
	resp, err := utils.HttpClient.R().Get("https://v1.hitokoto.cn/")
	if err != nil {
		return "", err
	}
	j := gjson.ParseBytes(resp.Bytes())
	if !j.Get("hitokoto").Exists() {
		return "", fmt.Errorf("获取失败，响应结果：%s", resp.String())
	}
	text := j.Get("hitokoto").String()
	if from := j.Get("from").String(); from != "" {
		text = fmt.Sprintf("%s\n—— %s", text, from)
	}
	return text, nil
}
