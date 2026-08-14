package plugins

import (
	"context"
	"fmt"
	"log"
	"strings"

	"qqbot/internal/agent"
	"qqbot/internal/agent/tools"
	"qqbot/internal/bot"
	"qqbot/internal/scheduler"
)

// TasksPlugin 是定时任务的接线层：启动调度循环（bot.OnStart）、实现 agent.run
// 处理器（到点以任务专属会话跑 agent 并把结果主动发到目标聊天）、提供 /task 命令。
//
// ag 可为 nil（未配 AI_API_KEY）：agent 任务不可用、工具不注册，插件任务照常调度。
type TasksPlugin struct {
	sched *scheduler.Scheduler
	ag    *agent.Agent
	bot   *bot.Bot // Install 时捕获，处理器里主动发消息用
}

// NewTasks 创建接线插件（sched 由 main 装配并传入；ag 可为 nil）。
func NewTasks(sched *scheduler.Scheduler, ag *agent.Agent) *TasksPlugin {
	return &TasksPlugin{sched: sched, ag: ag}
}

// Install 注册启动钩子、处理器与命令。
func (p *TasksPlugin) Install(b *bot.Bot) {
	p.bot = b
	b.OnStart(func(ctx context.Context) { p.sched.Run(ctx) })
	if p.ag != nil {
		p.sched.Handle(scheduler.HandlerAgent, p.runAgentTask)
		// 删除任务时顺带清它的专属会话（t:任务id），重建同名任务不串历史。
		p.sched.OnDelete(func(t scheduler.Task) {
			if t.Handler != scheduler.HandlerAgent {
				return
			}
			if err := p.ag.ResetSession(taskSessionKey(t.ID)); err != nil {
				log.Printf("[task] 清理任务会话 %s 失败: %v", t.ID, err)
			}
		})
	}
	b.Command("task", p.cmd).
		Desc("管理定时任务（list 列表 / del 删除 / on|off 开关 / run 立即试跑；创建直接对话，如「每天8点发新闻摘要」）").
		Alias("任务")
}

// taskSessionKey 是 agent 任务的专属会话 key：跨次执行共享上下文（能记住之前发过
// 什么、用户偏好），又与聊天会话隔离。
func taskSessionKey(taskID string) string { return "t:" + taskID }

// runAgentTask 是 agent.run 处理器：以任务专属会话执行 prompt，结果按 Target 主动
// 发送。群/私聊主动消息需在 q.qq.com 开通且受官方额度限制（频道无限制），失败记
// LastError、不自动重试（下个周期自然会再试）。
func (p *TasksPlugin) runAgentTask(ctx context.Context, t scheduler.Task) error {
	if p.ag == nil {
		return fmt.Errorf("agent 未启用（未配置 AI_API_KEY），无法执行 AI 任务")
	}
	if t.Target == nil {
		return fmt.Errorf("任务缺少发送目标")
	}
	reply, err := p.ag.Run(ctx, taskSessionKey(t.ID), t.Payload)
	if err != nil {
		return fmt.Errorf("agent 执行失败: %w", err)
	}
	reply = strings.TrimSpace(reply)
	if reply == "" {
		log.Printf("[task] %s(%s) 本次没有产出内容，跳过发送", t.ID, t.Name)
		return nil
	}
	return p.send(ctx, *t.Target, truncateForSend(reply, taskMsgMaxRunes))
}

// send 按目标类型主动发送一条文本（空锚点=主动消息；ctx 带调度的执行时限）。
func (p *TasksPlugin) send(ctx context.Context, tg scheduler.Target, text string) error {
	var err error
	switch tg.Source {
	case "group":
		_, err = p.bot.SendGroupText(ctx, tg.ID, "", text)
	case "c2c":
		_, err = p.bot.SendC2CText(ctx, tg.ID, "", text)
	case "channel":
		_, err = p.bot.SendChannelText(ctx, tg.ID, text)
	default:
		return fmt.Errorf("未知发送目标类型 %q", tg.Source)
	}
	return err
}

// taskMsgMaxRunes 是任务结果发送的长度上限（超长截断，避免主动消息被整条拒发）。
const taskMsgMaxRunes = 2000

// truncateForSend 超长截断并加提示。
func truncateForSend(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "\n…（超长已截断）"
}

// cmd 是 /task 命令入口。
func (p *TasksPlugin) cmd(c *bot.Ctx) {
	switch c.Args.Get(0) {
	case "", "list", "列表":
		tasks := p.sched.List()
		if len(tasks) == 0 {
			c.ReplyText("还没有定时任务。创建直接跟我说，比如「每天早上8点给我发一条新闻摘要」")
			return
		}
		lines := make([]string, 0, len(tasks))
		for _, t := range tasks {
			lines = append(lines, tools.DescribeTask(t))
		}
		c.ReplyText(strings.Join(lines, "\n"))

	case "del", "rm", "删除":
		id := strings.TrimSpace(c.Args.Get(1))
		if id == "" {
			c.ReplyText("用法：/task del <ID>（如 /task del t3，ID 见 /task 列表）")
			return
		}
		if t, ok := p.sched.Delete(id); ok {
			c.ReplyText(fmt.Sprintf("已删除 [%s]「%s」", t.ID, t.Name))
		} else {
			c.ReplyText("任务 " + id + " 不存在")
		}

	case "on", "off":
		id := strings.TrimSpace(c.Args.Get(1))
		if id == "" {
			c.ReplyText("用法：/task on|off <ID>")
			return
		}
		if t, ok := p.sched.Toggle(id, c.Args.Get(0) == "on"); ok {
			c.ReplyText(fmt.Sprintf("[%s]「%s」已%s", t.ID, t.Name, map[bool]string{true: "启用", false: "停用"}[t.Enabled]))
		} else {
			c.ReplyText("任务 " + id + " 不存在")
		}

	case "run", "试跑":
		id := strings.TrimSpace(c.Args.Get(1))
		if id == "" {
			c.ReplyText("用法：/task run <ID>（立即执行一次，不影响原计划）")
			return
		}
		if err := p.sched.RunNow(id); err != nil {
			c.ReplyText("无法执行：" + err.Error())
			return
		}
		c.ReplyText("已开始执行，结果会发送到任务配置的目标聊天，稍后可用 /task 查看运行状态")

	default:
		c.ReplyText("用法：/task（列表）｜/task del <ID>｜/task on|off <ID>｜/task run <ID>；创建新任务直接跟我说，比如「每天8点发新闻摘要」")
	}
}

// chatInfoOf 由消息上下文构造注入 agent 工具的当前聊天信息（定时任务默认发送目标等）。
func chatInfoOf(c *bot.Ctx) scheduler.ChatInfo {
	tg := scheduler.Target{Source: "c2c", ID: c.UserID()}
	switch {
	case c.IsGroup():
		tg = scheduler.Target{Source: "group", ID: c.GroupID()}
	case c.IsChannel():
		tg = scheduler.Target{Source: "channel", ID: c.ChannelID()}
	}
	return scheduler.ChatInfo{Target: tg, UserID: c.UserID(), Username: c.Username()}
}
