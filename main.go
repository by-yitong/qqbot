// 基于腾讯官方 botgo SDK 的 QQ 机器人（插件框架 qqbot/internal/bot + agent harness qqbot/internal/agent）。
//
// godotenv 在启动时加载 ".env"。配置（复制 .env.example 为 .env）：
//
//	QQBOT_APP_ID=你的机器人 AppID        # q.qq.com 后台获取
//	QQBOT_APP_SECRET=你的机器人 AppSecret # q.qq.com 后台获取
//	BOT_PREFIXES=/                       # 命令前缀（逗号分隔多个，可选）
//	AI_API_KEY=                          # 配置则启用 agent 兜底（工具循环/多轮记忆/压缩/技能/MCP）
//
// 运行：go run .
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"qqbot/internal/agent"
	tools "qqbot/internal/agent/tools"
	"qqbot/internal/bot"
	"qqbot/internal/imgstore"
	"qqbot/internal/memory"
	"qqbot/internal/scheduler"
	"qqbot/plugins"
)

func main() {
	_ = godotenv.Load()

	// 单实例守护：已有活的 bot 进程在跑时拒绝启动。两个实例 = 两条 WSS 连接，
	// 同一条消息被处理两次（指令+AI 各回一次的元凶）。air 的杀旧起新是先杀后启，
	// 不会误伤；重复的 air / 手动再起一个会被这里拦下。
	guardSingleInstance(envOr("BOT_PID_FILE", "./data/bot.pid"))

	appID := os.Getenv("QQBOT_APP_ID")
	appSecret := os.Getenv("QQBOT_APP_SECRET")
	if appID == "" || appSecret == "" {
		log.Fatal("请在 .env 中配置 QQBOT_APP_ID 和 QQBOT_APP_SECRET（去 q.qq.com 后台获取）")
	}

	b := bot.New(bot.Config{AppID: appID, AppSecret: appSecret},
		// 兼容旧变量名 BOT_PREFIX（单数）；BOT_PREFIXES 优先。
		bot.WithPrefixes(parsePrefixes(envOr("BOT_PREFIXES", envOr("BOT_PREFIX", "/")))...),
	)

	// 日志中间件：每条消息打印一行（openid 不是 QQ 号）。
	b.Use(func(next bot.MessageHandler) bot.MessageHandler {
		return func(c *bot.Ctx) {
			where := "私聊"
			switch {
			case c.IsGroup():
				where = "群 " + c.GroupID()
			case c.IsChannel():
				where = "频道 " + c.ChannelID()
			}
			log.Printf("[%s] %s: %s", where, c.UserID(), c.Text())
			next(c)
		}
	})

	// 定时任务调度器（tasks.json 落盘；TASK_TZ 缺省用本地时区；上限 <=0 用内置默认）。
	tz := time.Local
	if s := envOr("TASK_TZ", ""); s != "" {
		if l, err := time.LoadLocation(s); err == nil {
			tz = l
		} else {
			log.Printf("[main] TASK_TZ 不合法（用本地时区）: %v", err)
		}
	}
	sched := scheduler.New(envOr("TASK_FILE", "./data/tasks.json"), tz,
		envInt("TASK_MAX_PER_CHAT", 0), envInt("TASK_MAX_TOTAL", 0))

	// agent 兜底未启用时为 nil：TasksPlugin 对 nil ag 降级（agent 任务不可用，插件任务照常）。
	var ag *agent.Agent

	// 注册插件。
	b.UsePlugin(
		plugins.NewPing(),
		plugins.NewEcho(),
		plugins.NewCounter(),
		plugins.NewHappy(sched, envOr("HAPPY_DAILY_TARGET", "")),
		plugins.NewSystem(),
		plugins.NewZhiPu(),
		plugins.NewOnline("AD649FF82C450D9E5BF229974329E36C", "🤖 机器人已上线"),
	)

	// 自定义菜单 + 指令面板（官方 2026 新增接口，botgo 未覆盖，自行 HTTP 调用）：
	// 默认按已注册命令自动生成（AutoMenuConfig，发布时取，命令已全部注册完），
	// AGENT_MENU_CONFIG 指向的 menu.json 可整体覆盖；
	// AGENT_MENU=1（默认）时启动自动发布，.menu 命令手动 apply/clear/查看。
	menuCfg, menuFromFile := bot.MenuConfig{}, false
	if cfg, err := bot.LoadMenuConfig(envOr("AGENT_MENU_CONFIG", "./menu.json")); err != nil {
		log.Printf("[main] 菜单配置加载失败（用自动生成）: %v", err)
	} else if cfg != nil {
		menuCfg = *cfg
		menuFromFile = true
		log.Println("[main] 已加载菜单配置 menu.json")
	}
	getMenuCfg := func() bot.MenuConfig {
		if menuFromFile {
			return menuCfg
		}
		return b.AutoMenuConfig()
	}
	if envInt("AGENT_MENU", 1) != 0 {
		b.OnStart(func(ctx context.Context) {
			sum, err := b.ApplyMenus(ctx, getMenuCfg())
			if err != nil {
				log.Printf("[menu] 启动发布失败: %v", err)
				return
			}
			log.Printf("[menu] %s", sum)
		})
	}
	b.Command("menu", func(c *bot.Ctx) {
		switch c.Args.Get(0) {
		case "apply":
			sum, err := b.ApplyMenus(c.Context, getMenuCfg())
			if err != nil {
				c.ReplyText("发布失败：" + err.Error())
				return
			}
			c.ReplyText(sum)
		case "clear":
			if err := b.ClearMenus(c.Context); err != nil {
				c.ReplyText("清除失败：" + err.Error())
				return
			}
			c.ReplyText("菜单与面板已清除")
		default:
			m, err := b.GetMenu(c.Context)
			if err != nil {
				c.ReplyText("查询失败：" + err.Error())
				return
			}
			if len(m.Items) == 0 {
				c.ReplyText("当前未设置菜单。.menu apply 发布 / .menu clear 清除")
				return
			}
			names := make([]string, 0, len(m.Items))
			for _, it := range m.Items {
				names = append(names, it.Name)
			}
			c.ReplyText("当前菜单：" + strings.Join(names, "、") + "。.menu apply 重新发布 / .menu clear 清除")
		}
	}).Desc("管理自定义菜单与指令面板（apply 发布 / clear 清除 / 无参查看）").Alias("菜单")

	// 中文帮助命令（内置 help 之外）：菜单/面板按钮填入的中文词要能直接命中命令。
	b.Command("帮助", func(c *bot.Ctx) {
		c.ReplyText(c.Bot.Help())
	}).Desc("显示命令列表")

	// 长期记忆（SQLite）：群/私聊消息全量落库（独立于 agent，没配 AI 也记录）。
	var mem *memory.Store
	if envOr("AGENT_MEMORY", "1") == "1" {
		dbPath := envOr("AGENT_MEMORY_DB", "./data/memory.db")
		var err error
		mem, err = memory.Open(dbPath)
		if err != nil {
			log.Fatalf("打开聊天记忆库失败: %v", err)
		}
		defer mem.Close()
		log.Println("[main] 聊天记忆已启用: " + dbPath)
		b.UsePlugin(plugins.NewRecorder(mem))
	}

	// Agent 兜底：未命中命令的消息交给 agent（可调工具；后续还会多轮记忆/压缩/技能/MCP）。
	// 配置了 AI_API_KEY 才启用。
	if key := os.Getenv("AI_API_KEY"); key != "" {
		reg := agent.NewRegistry()
		reg.Register(&tools.HTTPGet{Allow: splitList(envOr("AGENT_HTTP_ALLOW", ""))})
		reg.Register(&tools.Now{})

		// anysearch 联网搜索（web_search + web_extract）；API key 可选（匿名低限额）。
		any := &tools.AnySearch{
			APIKey:  envOr("ANYSEARCH_API_KEY", ""),
			BaseURL: envOr("ANYSEARCH_BASE_URL", ""),
		}
		reg.Register(&tools.WebSearch{Owner: any})
		reg.Register(&tools.WebExtract{Owner: any})

		// 长期记忆检索（search_history）：有记忆库才注册。
		if mem != nil {
			reg.Register(&tools.HistorySearch{Store: mem})
		}

		// 图床（QQ 富媒体是 URL 模式，本地图片都得经它转公网 URL）：
		// 生图工具、send_image 的本地文件（如浏览器截图）共用。
		imgHost := &imgstore.Host{
			BaseURL: envOr("IMGHOST_BASE_URL", ""),
			Token:   envOr("IMGHOST_TOKEN", ""),
		}

		// 生图（OpenAI 协议，文生图/图生图）；有 IMAGE_API_KEY 才注册。
		// 网关若只回 base64，工具解码后上传图床拿公网 URL，再由 send_image 经 URL 模式发出。
		if imgKey := os.Getenv("IMAGE_API_KEY"); imgKey != "" {
			// 生成的图在本地也存一份（归档便于事后查看）；设空则不落盘。须在 Default() 首次调用前设置。
			if dir := envOr("IMG_SAVE_DIR", "./data/images"); dir != "" {
				imgstore.SetSaveDir(dir)
			}
			imgen := &tools.ImageGen{
				APIKey:    imgKey,
				BaseURL:   envOr("IMAGE_BASE_URL", ""),
				Model:     envOr("IMAGE_MODEL", "gpt-image-1"),
				ImageHost: imgHost,
			}
			reg.Register(&tools.GenerateImage{Owner: imgen})
			hostMsg := "无图床（生成的图仅本地，QQ 发不出）"
			if imgen.ImageHost.Configured() {
				hostMsg = "图床 " + imgen.ImageHost.BaseURL
			}
			log.Println("[main] 已启用生图工具，模型: " + imgen.Model + "，" + hostMsg + "，归档: " + envOr("IMG_SAVE_DIR", "./data/images"))
		}

		// 危险工具（默认关）：bash 与文件读写，需显式 env 开关才注册。
		if envOr("AGENT_ENABLE_BASH", "0") == "1" {
			reg.Register(&tools.Bash{
				WorkDir: envOr("AGENT_BASH_CWD", ""),
				Allow:   splitList(envOr("AGENT_BASH_ALLOW", "")),
			})
			log.Println("[main] 已启用 bash 工具（公开 bot 上有 RCE 风险，请确认）")
		}
		if envOr("AGENT_ENABLE_FS", "0") == "1" {
			root := envOr("AGENT_FS_ROOT", "./data/fs")
			reg.Register(&tools.FileRead{Root: root})
			reg.Register(&tools.FileWrite{Root: root})
			log.Println("[main] 已启用文件读写工具，根目录: " + root)
		}

		sessions, err := agent.NewSessionStore(envOr("AGENT_HISTORY_DIR", "./data/sessions"))
		if err != nil {
			log.Fatalf("创建会话目录失败: %v", err)
		}

		skills, err := agent.LoadSkills(envOr("AGENT_SKILLS_DIR", "./skills"))
		if err != nil {
			log.Printf("[main] 加载技能失败（已忽略）: %v", err)
		}
		if len(skills) > 0 {
			log.Printf("[main] 已加载 %d 个技能", len(skills))
		}

		// MCP：连接 mcp.json 里的 server，工具并入 registry；
		// 再加载运行时动态添加的（对话里贴配置加的，重启自动重连），与 mcp.json 同名去重。
		mcpCfgs, err := agent.LoadMCPConfig(envOr("AGENT_MCP_CONFIG", "./mcp.json"))
		if err != nil {
			log.Printf("[main] 加载 MCP 配置失败（已忽略）: %v", err)
		}
		runtimeMCPPath := envOr("AGENT_RUNTIME_MCP", "./data/mcp_runtime.json")
		runtimeCfgs, err := agent.LoadMCPConfig(runtimeMCPPath)
		if err != nil {
			log.Printf("[main] 加载运行时 MCP 配置失败（已忽略）: %v", err)
		}
		staticNames := map[string]bool{}
		for _, c := range mcpCfgs {
			staticNames[c.Name] = true
		}
		for _, c := range runtimeCfgs {
			if !staticNames[c.Name] {
				mcpCfgs = append(mcpCfgs, c)
			}
		}
		mcpClients, _ := agent.DialAllMCP(context.Background(), mcpCfgs, reg)
		defer agent.CloseAll(mcpClients)

		// LLM 客户端：OnUsage 在每次成功调用后记一行日志（你盯日志的习惯正好用上），
		// 有记忆库时同时落库（.usage 命令的数据源）。工具循环内的多次调用、压缩摘要调用都计。
		llm := agent.NewLLMClient(envOr("AI_BASE_URL", "https://api.deepseek.com/v1"), key, envOr("AI_MODEL", "deepseek-chat"))
		llm.OnUsage = func(u agent.Usage) {
			log.Printf("[agent] tokens: %s", u)
			if mem != nil {
				now := time.Now()
				mem.RecordUsage(memory.LLMUsage{
					Ts:               now,
					Day:              now.Format("2006-01-02"),
					Model:            u.Model,
					PromptTokens:     u.PromptTokens,
					CompletionTokens: u.CompletionTokens,
					TotalTokens:      u.TotalTokens,
					CachedTokens:     u.CachedTokens,
					CacheSupported:   u.CacheSupported,
					DurationMs:       u.DurationMs,
				})
			}
		}

		ag = agent.New(agent.Config{
			LLM:                 llm,
			SystemPrompt:        envOr("AI_SYSTEM_PROMPT", ""),
			Tools:               reg,
			Sessions:            sessions,
			MaxIterations:       envInt("AGENT_MAX_ITERATIONS", 0),
			LoopTimeout:         envDurationSec("AGENT_LOOP_TIMEOUT", 0),
			CompactionThreshold: envInt("AGENT_COMPACTION_THRESHOLD", 6000),
			KeepRecent:          envInt("AGENT_KEEP_RECENT", 0),
			Skills:              skills,
			RuntimeMCPCfg:       runtimeMCPPath,
		})
		// 让 AI 在对话中能自主添加技能/MCP（用户发 URL 或贴配置 JSON 即可）。
		ag.RegisterControlTools(reg)
		defer ag.Close()
		// 记忆联动：增量群聊记录并进 session（窗口/条数 env 可调，marker 落盘重启续上）
		// + bot 回复落库。
		agentPlug := plugins.NewAgent(ag, plugins.WithMemory(mem,
			time.Duration(envInt("AGENT_RECENT_WINDOW", 60))*time.Minute,
			envInt("AGENT_RECENT_MAX", 30)),
			plugins.WithGapFile(envOr("AGENT_GAP_MARKERS", "./data/gap_markers.json")),
			plugins.WithImageHost(imgHost),
			plugins.WithSteerAck(envInt("AGENT_STEER_ACK", 1) != 0))
		// 短视频解析（注册在 Agent 之前：命中分享链接即 Abort，不触发 AI 兜底）。
		b.UsePlugin(plugins.NewVideo(imgHost))

		b.UsePlugin(agentPlug)

		// .stop：停止当前会话正在进行的处理（与补充消息共用同一会话 key）。
		b.Command("stop", func(c *bot.Ctx) {
			if ag.Interrupt(plugins.SessionKey(c)) {
				c.ReplyText("已停止")
			} else {
				c.ReplyText("当前没有进行中的任务")
			}
		}).Desc("停止当前会话正在进行的处理").Alias("停止", "停下")

		// .reset：清空当前会话的短期记忆（session；长期记忆库不受影响）。
		b.Command("reset", func(c *bot.Ctx) {
			go func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[reset] panic: %v", r)
					}
				}()
				key := plugins.SessionKey(c)
				ag.Interrupt(key) // 在跑先停，防它 persist 把清掉的历史写回来
				ag.WaitIdle(key, 3*time.Second)
				agentPlug.ResetMarker(key) // 覆盖点一起清：下次 @bot 重新全量铺底
				if err := ag.ResetSession(key); err != nil {
					c.ReplyText("清空失败：" + err.Error())
					return
				}
				c.ReplyText("当前会话已清空（长期记忆不受影响），开始新对话吧")
			}()
		}).Desc("清空当前会话的对话历史（长期记忆保留）").Alias("清空对话", "清空")

		// .status：查看当前会话的处理状态。
		b.Command("status", func(c *bot.Ctx) {
			info := ag.Status(plugins.SessionKey(c))
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
			sb.WriteString("；模型 " + envOr("AI_MODEL", "未设置"))
			c.ReplyText(sb.String())
		}).Desc("查看当前会话的处理状态").Alias("状态")

		// .usage：token 用量统计（今日/累计；数据在记忆库，需 AGENT_MEMORY 开启）。
		if mem != nil {
			b.Command("usage", func(c *bot.Ctx) {
				today, err := mem.UsageSummary(time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 0, 0, 0, time.Local))
				if err != nil {
					c.ReplyText("统计失败：" + err.Error())
					return
				}
				total, err := mem.UsageSummary(time.Time{})
				if err != nil {
					c.ReplyText("统计失败：" + err.Error())
					return
				}
				if _, err := c.ReplyMarkdown(usageMarkdown(today, total)); err != nil {
					c.ReplyText("今日：" + today.String() + "\n累计：" + total.String())
				}
			}).Desc("查看 token 用量统计（今日/累计）").Alias("用量")
		}

		// 确定性命令入口（直接发 URL 或贴配置 JSON 添加，不依赖 AI 判断）。
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
				n, err := ag.AddMCPFromURL(c.Context, typ, arg)
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
				n, err := ag.AddMCPFromConfig(c.Context, cfg)
				if err != nil {
					lines = append(lines, cfg.Name+": 失败（"+err.Error()+"）")
					continue
				}
				lines = append(lines, cfg.Name+": 已连接，接入 "+strconv.Itoa(n)+" 个工具")
			}
			c.ReplyText(strings.Join(lines, "\n") + "\n已保存，重启后自动重连")
		}).Desc("连接一个 MCP server（传 http/sse URL，或粘贴 MCP 配置 JSON）")
		b.Command("skill", func(c *bot.Ctx) {
			u := strings.TrimSpace(c.Args.Get(0))
			if u == "" {
				c.ReplyText("用法：/skill <SKILL.md URL>")
				return
			}
			sk, err := ag.AddSkillFromURL(c.Context, u)
			if err != nil {
				c.ReplyText("加载失败：" + err.Error())
				return
			}
			msg := "已加载技能 " + sk.Name
			if sk.Description != "" {
				msg += "：" + sk.Description
			}
			c.ReplyText(msg)
		}).Desc("加载一个 SKILL.md（传 URL）")
	}

	// 定时任务接线：调度循环（OnStart 启动）+ agent.run 处理器 + /task 命令。
	// 放在 agent 块之后注册：ag 为 nil（未配 AI_API_KEY）时 agent 任务自动降级。
	b.UsePlugin(plugins.NewTasks(sched, ag))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Println("bot 启动")
	if err := b.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("bot 退出: %v", err)
	}
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

func fmtTokMD(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
	}
	if n >= 10_000 {
		return fmt.Sprintf("%.1f万", float64(n)/10_000)
	}
	return strconv.FormatInt(n, 10)
}

// parsePrefixes 把 ".|/" 解析成 []string。用 | 分隔：逗号在中英文/全角半角间太容易
// 出错（".，/" 会被当成一个三字符前缀），| 没有歧义。
func parsePrefixes(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "|") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// envOr 读环境变量，为空则返回默认值。
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envInt 读环境变量为整数，为空或非法则返回默认值。
func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// envDurationSec 读环境变量为「秒」并转 time.Duration；<=0 返回 0（让 agent 用默认值）。
func envDurationSec(key string, def int) time.Duration {
	n := envInt(key, def)
	if n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// splitList 把逗号分隔的字符串解析成列表（用于白名单等，空项跳过）。
func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// guardSingleInstance 单实例守护：pid 文件指向一个活着的本 bot 进程则拒绝启动，
// 否则写入自己的 pid。判断「是本 bot」看 /proc/<pid>/cmdline 是否含 main/qqbot
// （pid 被系统复用到无关进程时不误拦，直接接管）。
func guardSingleInstance(path string) {
	if data, err := os.ReadFile(path); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid != os.Getpid() {
			if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
				cmd := strings.ReplaceAll(string(b), "\x00", " ")
				if strings.Contains(cmd, "main") || strings.Contains(cmd, "qqbot") {
					log.Fatalf("[main] 已有实例在运行 (pid %d)，拒绝重复启动；确认只跑一个 air，或先 kill 旧进程（rm %s 可跳过检查）", pid, path)
				}
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("[main] 创建 pid 目录失败（忽略）: %v", err)
		return
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		log.Printf("[main] 写 pid 文件失败（忽略）: %v", err)
	}
}
