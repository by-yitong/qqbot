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

	"qqbot/internal/bot"
	"qqbot/internal/imgstore"
	"qqbot/internal/memory"
	"qqbot/internal/scheduler"
	"qqbot/internal/utils"
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

	// 图床（QQ 富媒体是 URL 模式，本地媒体都得经它转公网 URL）：
	// 生图、send_image 本地文件、语音自动转码产物共用。
	// 两种模式 .env 切换：IMGHOST_LOCAL_DIR 非空 → 本地直存（写盘 + BaseURL 拼公网地址，
	// 适合 bot 与 web 服务同机，如公网服务器 nginx 映射该目录）；为空 → 远端图床上传。
	imgHost := &imgstore.Host{
		BaseURL: utils.EnvOr("IMGHOST_BASE_URL", ""),
		Token:   utils.EnvOr("IMGHOST_TOKEN", ""),
		Dir:     utils.EnvOr("IMGHOST_LOCAL_DIR", ""),
	}

	b := bot.New(bot.Config{AppID: appID, AppSecret: appSecret},
		// 兼容旧变量名 BOT_PREFIX（单数）；BOT_PREFIXES 优先。
		bot.WithPrefixes(parsePrefixes(utils.EnvOr("BOT_PREFIXES", utils.EnvOr("BOT_PREFIX", "/")))...),
		// 主人 openid（BOT_OWNERS，逗号分隔）：OwnerOnly 命令（如 /禁言）与
		// /webhook 群地址等 handler 内校验主人的操作仅主人可用。
		// 自己的 openid 用 /id 命令查。fail-closed：不配置则这些命令无人可用。
		bot.WithOwners(utils.SplitList(os.Getenv("BOT_OWNERS"))...),
		// 语音自动转码产物经图床发布公网 URL（未配图床则只能发已是 silk 的 URL）。
		bot.WithMediaHost(imgHost),
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
	if s := utils.EnvOr("TASK_TZ", ""); s != "" {
		if l, err := time.LoadLocation(s); err == nil {
			tz = l
		} else {
			log.Printf("[main] TASK_TZ 不合法（用本地时区）: %v", err)
		}
	}
	sched := scheduler.New(utils.EnvOr("TASK_FILE", "./data/tasks.json"), tz,
		utils.EnvInt("TASK_MAX_PER_CHAT", 0), utils.EnvInt("TASK_MAX_TOTAL", 0))

	// 注册插件。带 FromEnv 后缀的构造器各自读环境变量决定是否启用（未配置返回
	// nil，UsePlugin 跳过），启停条件与日志收在插件文件里，main 只保留这张清单。
	//
	// 图床上传：「/图床」命令插件 + agent 的 upload_image 工具（自然语言
	//「把这张图传图床」）共用同一个 client，所以 FromEnv 把 client 也返回出来。
	imgbedPlug, imgbedCli, imgbedMax := plugins.NewImgbedFromEnv()
	b.UsePlugin(
		plugins.NewPing(),
		plugins.NewWhois(),
		plugins.NewEcho(),
		plugins.NewCounter(),
		plugins.NewHappy(sched, utils.EnvOr("HAPPY_DAILY_TARGET", "")),
		plugins.NewSystem(),
		plugins.NewID(),
		plugins.NewZhiPu(),
		plugins.NewZhihuFromEnv(),
		plugins.NewOnline("AD649FF82C450D9E5BF229974329E36C", "🤖 机器人已上线"),
		plugins.NewBtFromEnv(),
		imgbedPlug,
		plugins.NewWebhookFromEnv(imgHost),
		plugins.NewWelcomeFromEnv(),
		plugins.NewJoinReviewFromEnv(),
		plugins.NewMuteFromEnv(),
		plugins.NewMemberFromEnv(),
		plugins.NewMusic(),
	)

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

	// 短视频解析：解析不依赖 AI，无条件启用（没配 AI_API_KEY 也能用）。
	// 截流语义靠注册顺序：须在 Recorder（先落库再被消费）之后、setupAgent
	// 装配的 AI 兜底之前——命中分享链接即 Abort，勿挪。
	b.UsePlugin(plugins.NewVideo(imgHost))

	// Agent 兜底：未命中命令的消息交给 agent（工具循环/多轮记忆/压缩/技能/MCP，
	// 装配细节见 setup.go）；配置了 AI_API_KEY 才启用。
	ag, closeAgent := setupAgent(b, mem, imgbedCli, imgbedMax, imgHost)
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
				c.ReplyTaggedOrPlain(
					"当前未设置菜单。"+bot.CmdInputTag("menu apply", "立即发布")+bot.CmdInputTag("menu clear", "清除"),
					"当前未设置菜单。menu apply 发布 / menu clear 清除")
				return
			}
			names := make([]string, 0, len(m.Items))
			for _, it := range m.Items {
				names = append(names, it.Name)
			}
			c.ReplyTaggedOrPlain(
				"当前菜单："+strings.Join(names, "、")+bot.CmdInputTag("menu apply", "重新发布")+bot.CmdInputTag("menu clear", "清除"),
				"当前菜单："+strings.Join(names, "、")+".menu apply 重新发布 / .menu clear 清除")
		}
	}).Desc("管理自定义菜单与指令面板（apply 发布 / clear 清除 / 无参查看）").Alias("菜单").Category(bot.CatGeneral)

	// 中文帮助命令（内置 help 之外）：菜单/面板按钮填入的中文词要能直接命中命令。
	// 回复是标签版命令列表——每条命令的名字渲染成官方内嵌指令标签，点击填入输入框
	// （带参数的命令补好空格，接着敲参数）；频道/markdown 被拒发时降级纯文本。
	b.Command("帮助", func(c *bot.Ctx) {
		c.ReplyTaggedOrPlain(c.Bot.HelpMarkdownFor(c), c.Bot.HelpFor(c))
	}).Desc("显示命令列表").Category(bot.CatGeneral)
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
