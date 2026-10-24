package plugins

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"qqbot/internal/agent"
	"qqbot/internal/bot"
	"qqbot/internal/memory"
)

// Session 是会话管理命令插件：.stop / .reset / .status / .usage / .mcp / .skill。
// 都围绕 agent 会话（打断、清空短期记忆、状态、token 用量、动态加 MCP 与技能）；
// 仅在配置了 AI（ag 非 nil）时由 main 装配，没配 AI 这些命令不存在。
type Session struct {
	ag          *agent.Agent
	model       string        // .status 展示用（空显示「未设置」）
	mem         *memory.Store // .usage 数据源；nil 时不注册 .usage
	resetMarker func(string)  // .reset 时同步清 agent 的注入覆盖点；可 nil
}

// NewSession 创建会话管理命令插件。
func NewSession(ag *agent.Agent, model string, resetMarker func(string), mem *memory.Store) *Session {
	return &Session{ag: ag, model: model, mem: mem, resetMarker: resetMarker}
}

// Install 注册命令。
func (p *Session) Install(b *bot.Bot) {
	p.installStop(b)
	p.installReset(b)
	p.installStatus(b)
	p.installUsage(b)
	p.installMCP(b)
	p.installSkill(b)
}

// installStop .stop：停止当前会话正在进行的处理（与补充消息共用同一会话 key）。
func (p *Session) installStop(b *bot.Bot) {
	b.Command("stop", func(c *bot.Ctx) {
		if p.ag.Interrupt(SessionKey(c)) {
			c.ReplyText("已停止")
		} else {
			c.ReplyText("当前没有进行中的任务")
		}
	}).Desc("停止当前会话正在进行的处理").Alias("停止", "停下").Category(bot.CatAI)
}

// installReset .reset：清空当前会话的短期记忆（session；长期记忆库不受影响）。
func (p *Session) installReset(b *bot.Bot) {
	b.Command("reset", func(c *bot.Ctx) {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[reset] panic: %v", r)
				}
			}()
			key := SessionKey(c)
			p.ag.Interrupt(key) // 在跑先停，防它 persist 把清掉的历史写回来
			p.ag.WaitIdle(key, 3*time.Second)
			if p.resetMarker != nil {
				p.resetMarker(key) // 覆盖点一起清：下次 @bot 重新全量铺底
			}
			if err := p.ag.ResetSession(key); err != nil {
				c.ReplyText("清空失败：" + err.Error())
				return
			}
			c.ReplyText("当前会话已清空（长期记忆不受影响），开始新对话吧")
		}()
	}).Desc("清空当前会话的对话历史（长期记忆保留）").Alias("清空对话", "清空").Category(bot.CatAI)
}

// installStatus .status：查看当前会话的处理状态。
func (p *Session) installStatus(b *bot.Bot) {
	b.Command("status", func(c *bot.Ctx) {
		info := p.ag.Status(SessionKey(c))
		var sb strings.Builder
		if info.Running {
			elapsed := time.Since(info.Since).Round(time.Second)
			sb.WriteString("处理中，已运行 " + elapsed.String())
		} else {
			sb.WriteString("空闲")
		}
		if info.Pending > 0 {
			sb.WriteString("；待并入补充 " + strconv.Itoa(info.Pending) + " 条")
		}
		model := p.model
		if model == "" {
			model = "未设置"
		}
		sb.WriteString("；模型 " + model)
		c.ReplyText(sb.String())
	}).Desc("查看当前会话的处理状态").Alias("状态").Category(bot.CatAI)
}

// installUsage .usage：token 用量统计（今日/累计；数据在记忆库，需 AGENT_MEMORY 开启）。
func (p *Session) installUsage(b *bot.Bot) {
	if p.mem == nil {
		return
	}
	b.Command("usage", func(c *bot.Ctx) {
		now := time.Now()
		today, err := p.mem.UsageSummary(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local))
		if err != nil {
			c.ReplyText("统计失败：" + err.Error())
			return
		}
		total, err := p.mem.UsageSummary(time.Time{})
		if err != nil {
			c.ReplyText("统计失败：" + err.Error())
			return
		}
		if _, err := c.ReplyMarkdown(usageMarkdown(today, total)); err != nil {
			c.ReplyText("今日：" + today.String() + "\n累计：" + total.String())
		}
	}).Desc("查看 token 用量统计（今日/累计）").Alias("用量").Category(bot.CatAI)
}

// installMCP .mcp：连接一个 MCP server 的确定性入口（URL 或贴配置 JSON，不依赖 AI 判断）。
func (p *Session) installMCP(b *bot.Bot) {
	b.Command("mcp", func(c *bot.Ctx) {
		rest, _ := c.StripPrefix()
		arg := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), "mcp"))
		if arg == "" {
			c.ReplyText("用法：/mcp <http/sse URL> 或直接粘贴 MCP 配置 JSON（stdio/http/sse 均可）")
			return
		}
		if strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://") {
			typ := "http"
			if strings.Contains(arg, "/sse") {
				typ = "sse"
			}
			n, err := p.ag.AddMCPFromURL(c.Context, typ, arg)
			if err != nil {
				c.ReplyText("连接失败：" + err.Error())
				return
			}
			c.ReplyText("已连接 MCP server，接入 " + strconv.Itoa(n) + " 个工具")
			return
		}
		cfgs, err := agent.ParseMCPSnippet(arg)
		if err != nil {
			c.ReplyText("配置解析失败：" + err.Error())
			return
		}
		var lines []string
		for _, cfg := range cfgs {
			n, err := p.ag.AddMCPFromConfig(c.Context, cfg)
			if err != nil {
				lines = append(lines, cfg.Name+": 失败（"+err.Error()+"）")
				continue
			}
			lines = append(lines, cfg.Name+": 已连接，接入 "+strconv.Itoa(n)+" 个工具")
		}
		c.ReplyText(strings.Join(lines, "\n") + "\n已保存，重启后自动重连")
	}).Desc("连接一个 MCP server（传 http/sse URL，或粘贴 MCP 配置 JSON）").Category(bot.CatAI)
}

// installSkill .skill：加载一个 SKILL.md（传 URL）。
func (p *Session) installSkill(b *bot.Bot) {
	b.Command("skill", func(c *bot.Ctx) {
		u := strings.TrimSpace(c.Args.Get(0))
		if u == "" {
			c.ReplyText("用法：/skill <SKILL.md URL>")
			return
		}
		sk, err := p.ag.AddSkillFromURL(c.Context, u)
		if err != nil {
			c.ReplyText("加载失败：" + err.Error())
			return
		}
		msg := "已加载技能 " + sk.Name
		if sk.Description != "" {
			msg += "：" + sk.Description
		}
		c.ReplyText(msg)
	}).Desc("加载一个 SKILL.md（传 URL）").Category(bot.CatAI)
}

// usageMarkdown 把今日/累计用量渲染成 markdown（.usage 命令的展示层）。
// 缓存命中率和输出速率只在有数据时展示，避免一堆 0。
func usageMarkdown(today, total memory.UsageSummary) string {
	var b strings.Builder
	b.WriteString("**📊 Token 用量统计**\n\n")
	b.WriteString("**今日**\n")
	b.WriteString(usageLines(today))
	b.WriteString("\n**累计**\n")
	b.WriteString(usageLines(total))
	return b.String()
}

func usageLines(u memory.UsageSummary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- 调用：%d 次\n", u.Calls)
	fmt.Fprintf(&b, "- 输入：%s token\n", fmtTokMD(u.PromptTokens))
	fmt.Fprintf(&b, "- 输出：%s token\n", fmtTokMD(u.CompletionTokens))
	fmt.Fprintf(&b, "- 合计：%s token\n", fmtTokMD(u.TotalTokens))
	if u.CacheSupported && u.PromptTokens > 0 {
		fmt.Fprintf(&b, "- 缓存命中：%d%%\n", u.CachedTokens*100/u.PromptTokens)
	}
	if u.DurationMs > 0 && u.CompletionTokens > 0 {
		fmt.Fprintf(&b, "- 平均输出：%d tok/s\n", u.CompletionTokens*1000/u.DurationMs)
	}
	return b.String()
}

// fmtTokMD 大数转人话（1.23M / 4.5万 / 9999）。
func fmtTokMD(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
	}
	if n >= 10_000 {
		return fmt.Sprintf("%.1f万", float64(n)/10_000)
	}
	return strconv.FormatInt(n, 10)
}
