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
//
// 装配分层：main.go 只做「读配置 → bot 构造 → 插件注册 → agent 装配 → Run」；
// agent 的工具/MCP/熔断与配套插件在 setup.go；会话管理命令在 plugins/session.go。
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
	"qqbot/internal/bot"
	"qqbot/internal/memory"
	"qqbot/internal/scheduler"
	"qqbot/internal/utils"
	"qqbot/pkg/imgbed"
	"qqbot/plugins"
)

func main() {
	_ = godotenv.Load()

	// 单实例守护：已有活的 bot 进程在跑时拒绝启动。两个实例 = 两条 WSS 连接，
	// 同一条消息被处理两次（指令+AI 各回一次的元凶）。air 的杀旧起新是先杀后启，
	// 不会误伤；重复的 air / 手动再起一个会被这里拦下。
	guardSingleInstance(utils.EnvOr("BOT_PID_FILE", "./data/bot.pid"))

	appID := os.Getenv("QQBOT_APP_ID")
	appSecret := os.Getenv("QQBOT_APP_SECRET")
	if appID == "" || appSecret == "" {
		log.Fatal("请在 .env 中配置 QQBOT_APP_ID 和 QQBOT_APP_SECRET（去 q.qq.com 后台获取）")
	}

	b := bot.New(bot.Config{AppID: appID, AppSecret: appSecret},
		// 兼容旧变量名 BOT_PREFIX（单数）；BOT_PREFIXES 优先。
		bot.WithPrefixes(parsePrefixes(utils.EnvOr("BOT_PREFIXES", utils.EnvOr("BOT_PREFIX", "/")))...),
		// 主人 openid（BOT_OWNERS，逗号分隔）：OwnerOnly 命令（如 /webhook）仅主人可用。
		// 自己的 openid 用 /id 命令查。fail-closed：不配置则这些命令无人可用。
		bot.WithOwners(utils.SplitList(os.Getenv("BOT_OWNERS"))...),
	)
	if len(utils.SplitList(os.Getenv("BOT_OWNERS"))) > 0 {
		log.Println("[main] 已配置主人，OwnerOnly 命令（如 /webhook）仅主人可用")
	}

	// /id 查发送者自己的 openid / 群 openid：配置 BOT_OWNERS、JOIN_ADMINS 等白名单用。
	b.Command("id", func(c *bot.Ctx) {
		out := "你的 openid：" + c.UserID()
		if c.IsGroup() {
			out += "\n本群 openid：" + c.GroupID()
		}
		_, _ = c.ReplyText(out)
	}).Desc("查看自己的 openid（配置主人/白名单用）").HideBotMenu()

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
	if s := utils.EnvOr("TASK_TZ", ""); s != "" {
		if l, err := time.LoadLocation(s); err == nil {
			tz = l
		} else {
			log.Printf("[main] TASK_TZ 不合法（用本地时区）: %v", err)
		}
	}
	sched := scheduler.New(utils.EnvOr("TASK_FILE", "./data/tasks.json"), tz,
		utils.EnvInt("TASK_MAX_PER_CHAT", 0), utils.EnvInt("TASK_MAX_TOTAL", 0))

	// 注册插件。
	var plugs = []bot.Plugin{
		plugins.NewPing(),
		plugins.NewWhois(),
		plugins.NewEcho(),
		plugins.NewCounter(),
		plugins.NewHappy(sched, utils.EnvOr("HAPPY_DAILY_TARGET", "")),
		plugins.NewSystem(),
		plugins.NewZhiPu(),
		plugins.NewOnline("AD649FF82C450D9E5BF229974329E36C", "🤖 机器人已上线"),
	}
	// 宝塔面板状态插件：配置了 BT_PANEL_URL/BT_PANEL_KEY 才启用（密钥不入库）。
	if u, k := os.Getenv("BT_PANEL_URL"), os.Getenv("BT_PANEL_KEY"); u != "" && k != "" {
		plugs = append(plugs, plugins.NewBtPlugin(u, k))
	}
	// 图床上传：配置了 IMGBED_TOKEN 才启用。「/图床」命令插件 + agent 的
	// upload_image 工具（自然语言「把这张图传图床」）共用同一个 client。
	var imgbedCli *imgbed.Client
	var imgbedMax int64
	if tok := os.Getenv("IMGBED_TOKEN"); tok != "" {
		base := utils.EnvOr("IMGBED_BASE_URL", "https://imgbed.jmwl.dpdns.org")
		imgbedCli = &imgbed.Client{BaseURL: base, Token: tok}
		imgbedMax = int64(utils.EnvInt("IMGBED_MAX_MB", 10)) << 20
		plugs = append(plugs, plugins.NewImgbed(imgbedCli, imgbedMax))
		log.Println("[main] 已启用图床上传 /图床 → " + base)
	}
	// Webhook 推送插件：群内 /webhook 生成推送地址，外部 POST 消息进群；
	// WEBHOOK_LISTEN 配置才启 HTTP 服务（地址前缀用 WEBHOOK_BASE_URL，如 http://xxxx.cn）。
	// /webhook 仅主人可用（BOT_OWNERS）；没配主人 = 命令谁都用不了，这里提醒一句。
	if addr := utils.EnvOr("WEBHOOK_LISTEN", ""); addr != "" {
		plugs = append(plugs, plugins.NewWebhook(utils.EnvOr("WEBHOOK_FILE", "./data/webhook.json"), addr, utils.EnvOr("WEBHOOK_BASE_URL", "")))
		log.Println("[main] 已启用 webhook 推送，监听 " + addr)
		if len(utils.SplitList(os.Getenv("BOT_OWNERS"))) == 0 {
			log.Println("[main] ⚠️ 未配置 BOT_OWNERS：/webhook 命令将对所有人拒绝（用 /id 查 openid 后配置）")
		}
	}
	// 入群欢迎：WELCOME_TEXT 配置才启用（{name} → 新成员昵称，昵称取不到用「新朋友」）。
	// 欢迎语是主动消息（入群事件无回复锚点），需在 q.qq.com 开通主动消息权限。
	if wtxt := utils.EnvOr("WELCOME_TEXT", ""); wtxt != "" {
		plugs = append(plugs, plugins.NewWelcome(wtxt, utils.SplitList(os.Getenv("WELCOME_GROUPS"))))
		log.Println("[main] 已启用入群欢迎")
	}
	// 入群申请自动审批：配了 JOIN_REVIEW_ANSWERS（| 分隔）或 JOIN_REVIEW_MATCH=ai 即启用。
	// 匹配模式：exact 精确 / contains 关键词包含 / ai 把问题与回答交给 LLM 判对错
	//（问答模式的事件自带群设置的问题原文，多群零配置；纯验证消息模式用
	// JOIN_REVIEW_AI_RULE 给全局判定标准；AI 失败回退关键词再留人工）。
	// 判定不通过默认留人工（JOIN_REVIEW_DECLINE=1 改为自动拒绝）。
	// 机器人需是群管理员才能收到申请事件；/审批 命令可人工查看与处理。
	if match := utils.EnvOr("JOIN_REVIEW_MATCH", "exact"); match == plugins.JoinMatchAI || os.Getenv("JOIN_REVIEW_ANSWERS") != "" {
		jr := plugins.NewJoinReview(parseAnswers(os.Getenv("JOIN_REVIEW_ANSWERS")), match,
			utils.EnvInt("JOIN_REVIEW_DECLINE", 0) == 1,
			utils.SplitList(os.Getenv("JOIN_REVIEW_GROUPS")),
			utils.SplitList(os.Getenv("JOIN_ADMINS")))
		if match == plugins.JoinMatchAI {
			if key := os.Getenv("AI_API_KEY"); key != "" {
				jr.WithAI(agent.NewLLMClient(
					utils.EnvOr("AI_BASE_URL", "https://api.deepseek.com/v1"), key,
					utils.EnvOr("AI_MODEL", "deepseek-chat")),
					utils.EnvOr("JOIN_REVIEW_AI_RULE", ""))
				log.Println("[main] 已启用入群申请自动审批（AI 判定）")
			} else {
				log.Println("[main] JOIN_REVIEW_MATCH=ai 但未配置 AI_API_KEY，AI 判定不生效（关键词兜底/留人工）")
			}
		} else {
			log.Printf("[main] 已启用入群申请自动审批（匹配模式 %s）", match)
		}
		plugs = append(plugs, jr)
	}
	b.UsePlugin(plugs...)

	installMenu(b)

	// 长期记忆（SQLite）：群/私聊消息全量落库（独立于 agent，没配 AI 也记录）。
	var mem *memory.Store
	if utils.EnvOr("AGENT_MEMORY", "1") == "1" {
		dbPath := utils.EnvOr("AGENT_MEMORY_DB", "./data/memory.db")
		var err error
		mem, err = memory.Open(dbPath)
		if err != nil {
			log.Fatalf("打开聊天记忆库失败: %v", err)
		}
		defer mem.Close()
		log.Println("[main] 聊天记忆已启用: " + dbPath)
		b.UsePlugin(plugins.NewRecorder(mem))
	}

	// Agent 兜底：未命中命令的消息交给 agent（工具循环/多轮记忆/压缩/技能/MCP，
	// 装配细节见 setup.go）；配置了 AI_API_KEY 才启用。
	ag, closeAgent := setupAgent(b, mem, imgbedCli, imgbedMax)
	defer closeAgent()

	// 定时任务接线：调度循环（OnStart 启动）+ agent.run 处理器 + /task 命令。
	// 放在 agent 装配之后注册：ag 为 nil（未配 AI_API_KEY）时 agent 任务自动降级。
	b.UsePlugin(plugins.NewTasks(sched, ag))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Println("bot 启动")
	if err := b.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("bot 退出: %v", err)
	}
}

// installMenu 注册自定义菜单/指令面板（官方 2026 新增接口，botgo 未覆盖）与
// .menu / 帮助 命令：默认按已注册命令自动生成（AutoMenuConfig，发布时取，
// 命令已全部注册完），AGENT_MENU_CONFIG 指向的 menu.json 可整体覆盖；
// AGENT_MENU=1（默认）时启动自动发布。
func installMenu(b *bot.Bot) {
	menuCfg, menuFromFile := bot.MenuConfig{}, false
	if cfg, err := bot.LoadMenuConfig(utils.EnvOr("AGENT_MENU_CONFIG", "./menu.json")); err != nil {
		log.Printf("[menu] 菜单配置加载失败（用自动生成）: %v", err)
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
	if utils.EnvInt("AGENT_MENU", 1) != 0 {
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
		c.ReplyText(c.Bot.HelpFor(c))
	}).Desc("显示命令列表")
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

// parseAnswers 把 "答案1|答案2" 解析成 []string（入群审批的正确答案集合）。
// 同 parsePrefixes 用 | 分隔，避免中文答案里的逗号歧义。
func parseAnswers(s string) []string {
	var out []string
	for _, a := range strings.Split(s, "|") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
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
