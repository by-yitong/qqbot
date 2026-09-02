// setup.go —— agent（AI 兜底）的装配：工具注册、MCP 拨号、LLM 主备熔断、
// agent 实例与配套插件（短视频解析、AI 兜底、会话管理命令）。
// main.go 只调用 setupAgent，这里集中「配了 AI_API_KEY 之后发生的一切」。
package main

import (
	"context"
	"log"
	"os"
	"time"

	"qqbot/internal/agent"
	tools "qqbot/internal/agent/tools"
	"qqbot/internal/bot"
	"qqbot/internal/imgstore"
	"qqbot/internal/memory"
	"qqbot/internal/utils"
	"qqbot/pkg/imgbed"
	"qqbot/plugins"
)

// setupAgent 装配 agent 并接线配套插件。配置了 AI_API_KEY 才启用：
// 返回 agent（未配置时为 nil，相关插件一并不装，行为与单模型/无 AI 一致）
// 与清理函数（进程退出时关 agent、备用模型探活、MCP 连接；main defer 调用）。
//
// imgbedCli/imgbedMax 是图床插件（/图床 命令）已建的 client，agent 的
// upload_image 工具与之共用同一 client 与大小上限；nil 表示图床未启用。
func setupAgent(b *bot.Bot, mem *memory.Store, imgbedCli *imgbed.Client, imgbedMax int64) (*agent.Agent, func()) {
	key := os.Getenv("AI_API_KEY")
	if key == "" {
		return nil, func() {}
	}

	reg := agent.NewRegistry()
	// 内网放行名单：http_get 与站长诊断工具（ping/tcp_ping/cert_info）共用。
	netAllow := utils.SplitList(utils.EnvOr("AGENT_HTTP_ALLOW", ""))
	reg.Register(&tools.HTTPGet{Allow: netAllow})
	reg.Register(&tools.Now{})

	// 站长诊断工具（ping/tcp_ping/whois/dns_lookup/cert_info）：连接类工具
	// 复用 http_get 的 SSRF 守卫（内网/回环默认拒绝）。
	reg.Register(&tools.PingTool{Allow: netAllow})
	reg.Register(&tools.TCPPingTool{Allow: netAllow})
	reg.Register(&tools.WhoisTool{})
	reg.Register(&tools.DNSLookupTool{})
	reg.Register(&tools.CertInfoTool{Allow: netAllow})

	// anysearch 联网搜索（web_search + web_extract）；API key 可选（匿名低限额）。
	any := &tools.AnySearch{
		APIKey:  utils.EnvOr("ANYSEARCH_API_KEY", ""),
		BaseURL: utils.EnvOr("ANYSEARCH_BASE_URL", ""),
	}
	reg.Register(&tools.WebSearch{Owner: any})
	reg.Register(&tools.WebExtract{Owner: any})

	// 长期记忆检索（search_history）：有记忆库才注册。
	if mem != nil {
		reg.Register(&tools.HistorySearch{Store: mem})
	}

	// 图床（QQ 富媒体是 URL 模式，本地图片都得经它转公网 URL）：
	// 生图工具、send_image 的本地文件（如浏览器截图）共用。
	// 两种模式 .env 切换：IMGHOST_LOCAL_DIR 非空 → 本地直存（写盘 + BaseURL 拼公网地址，
	// 适合 bot 与 web 服务同机，如公网服务器 nginx 映射该目录）；为空 → 远端图床上传。
	imgHost := &imgstore.Host{
		BaseURL: utils.EnvOr("IMGHOST_BASE_URL", ""),
		Token:   utils.EnvOr("IMGHOST_TOKEN", ""),
		Dir:     utils.EnvOr("IMGHOST_LOCAL_DIR", ""),
	}

	// 生图（OpenAI 协议，文生图/图生图）；有 IMAGE_API_KEY 才注册。
	// 网关若只回 base64，工具解码后上传图床拿公网 URL，再由 send_image 经 URL 模式发出。
	if imgKey := os.Getenv("IMAGE_API_KEY"); imgKey != "" {
		// 生成的图在本地也存一份（归档便于事后查看）；设空则不落盘。须在 Default() 首次调用前设置。
		if dir := utils.EnvOr("IMG_SAVE_DIR", "./data/images"); dir != "" {
			imgstore.SetSaveDir(dir)
		}
		imgen := &tools.ImageGen{
			APIKey:    imgKey,
			BaseURL:   utils.EnvOr("IMAGE_BASE_URL", ""),
			Model:     utils.EnvOr("IMAGE_MODEL", "gpt-image-1"),
			ImageHost: imgHost,
		}
		reg.Register(&tools.GenerateImage{Owner: imgen})
		hostMsg := "无图床（生成的图仅本地，QQ 发不出）"
		if imgen.ImageHost.Configured() {
			if imgen.ImageHost.Dir != "" {
				hostMsg = "本地直存 " + imgen.ImageHost.Dir + " → " + imgen.ImageHost.BaseURL
			} else {
				hostMsg = "远端图床 " + imgen.ImageHost.BaseURL
			}
		}
		log.Println("[agent] 已启用生图工具，模型: " + imgen.Model + "，" + hostMsg + "，归档: " + utils.EnvOr("IMG_SAVE_DIR", "./data/images"))
	}

	// 图床上传工具：对话里用户发图要外链（「把这张图传图床」）时 AI 直接调，
	// 不必非得用 /图床 命令；与插件共用同一 client 与大小上限。
	if imgbedCli != nil {
		reg.Register(&agent.UploadImageTool{Cli: imgbedCli, MaxBytes: imgbedMax})
	}

	// 危险工具（默认关）：bash 与文件读写，需显式 env 开关才注册。
	if utils.EnvOr("AGENT_ENABLE_BASH", "0") == "1" {
		reg.Register(&tools.Bash{
			WorkDir: utils.EnvOr("AGENT_BASH_CWD", ""),
			Allow:   utils.SplitList(utils.EnvOr("AGENT_BASH_ALLOW", "")),
		})
		log.Println("[agent] 已启用 bash 工具（公开 bot 上有 RCE 风险，请确认）")
	}
	if utils.EnvOr("AGENT_ENABLE_FS", "0") == "1" {
		root := utils.EnvOr("AGENT_FS_ROOT", "./data/fs")
		reg.Register(&tools.FileRead{Root: root})
		reg.Register(&tools.FileWrite{Root: root})
		log.Println("[agent] 已启用文件读写工具，根目录: " + root)
	}

	sessions, err := agent.NewSessionStore(utils.EnvOr("AGENT_HISTORY_DIR", "./data/sessions"))
	if err != nil {
		log.Fatalf("创建会话目录失败: %v", err)
	}

	skills, err := agent.LoadSkills(utils.EnvOr("AGENT_SKILLS_DIR", "./skills"))
	if err != nil {
		log.Printf("[agent] 加载技能失败（已忽略）: %v", err)
	}
	if len(skills) > 0 {
		log.Printf("[agent] 已加载 %d 个技能", len(skills))
	}

	// MCP：连接 mcp.json 里的 server，工具并入 registry；
	// 再加载运行时动态添加的（对话里贴配置加的，重启自动重连），与 mcp.json 同名去重。
	mcpCfgs, err := agent.LoadMCPConfig(utils.EnvOr("AGENT_MCP_CONFIG", "./mcp.json"))
	if err != nil {
		log.Printf("[agent] 加载 MCP 配置失败（已忽略）: %v", err)
	}
	runtimeMCPPath := utils.EnvOr("AGENT_RUNTIME_MCP", "./data/mcp_runtime.json")
	runtimeCfgs, err := agent.LoadMCPConfig(runtimeMCPPath)
	if err != nil {
		log.Printf("[agent] 加载运行时 MCP 配置失败（已忽略）: %v", err)
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

	// LLM 客户端：OnUsage 在每次成功调用后记一行日志，有记忆库时同时落库
	// （.usage 命令的数据源）。工具循环内的多次调用、压缩摘要调用都计。
	llm := agent.NewLLMClient(utils.EnvOr("AI_BASE_URL", "https://api.deepseek.com/v1"), key, utils.EnvOr("AI_MODEL", "deepseek-chat"))
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

	// 主备双模型：主模型连续失败达阈值熔断到备用，探活恢复后自动切回。
	// 备用配置（AI_FALLBACK_*）留空则不启用，行为与单模型一致。
	var llmRoot agent.Completer = llm
	var fo *agent.FailoverClient
	if fbModel := os.Getenv("AI_FALLBACK_MODEL"); fbModel != "" {
		fbKey := utils.EnvOr("AI_FALLBACK_API_KEY", key)
		fb := agent.NewLLMClient(utils.EnvOr("AI_FALLBACK_BASE_URL", utils.EnvOr("AI_BASE_URL", "")), fbKey, fbModel)
		fb.OnUsage = llm.OnUsage
		fo = agent.NewFailover(llm, fb,
			agent.WithBreakerThreshold(utils.EnvInt("AI_FAILBACK_BREAKER", 3)),
			agent.WithProbeInterval(time.Duration(utils.EnvInt("AI_FALLBACK_PROBE_SEC", 180))*time.Second))
		llmRoot = fo
		log.Printf("[agent] 已启用备用模型: %s（连续失败 %d 次熔断，每 %ds 探活主模型）",
			fbModel, utils.EnvInt("AI_FAILBACK_BREAKER", 3), utils.EnvInt("AI_FALLBACK_PROBE_SEC", 180))
	}

	ag := agent.New(agent.Config{
		LLM:                 llmRoot,
		SystemPrompt:        utils.EnvOr("AI_SYSTEM_PROMPT", ""),
		Tools:               reg,
		Sessions:            sessions,
		MaxIterations:       utils.EnvInt("AGENT_MAX_ITERATIONS", 0),
		LoopTimeout:         utils.EnvDurationSec("AGENT_LOOP_TIMEOUT", 0),
		CompactionThreshold: utils.EnvInt("AGENT_COMPACTION_THRESHOLD", 6000),
		KeepRecent:          utils.EnvInt("AGENT_KEEP_RECENT", 0),
		Skills:              skills,
		SkillsDir:           utils.EnvOr("AGENT_SKILLS_DIR", "./skills"),
		RuntimeMCPCfg:       runtimeMCPPath,
	})
	// 让 AI 在对话中能自主添加技能/MCP（用户发 URL 或贴配置 JSON 即可）。
	ag.RegisterControlTools(reg)

	// 记忆联动：增量群聊记录并进 session（窗口/条数 env 可调，marker 落盘重启续上）
	// + bot 回复落库。
	agentPlug := plugins.NewAgent(ag, plugins.WithMemory(mem,
		time.Duration(utils.EnvInt("AGENT_RECENT_WINDOW", 60))*time.Minute,
		utils.EnvInt("AGENT_RECENT_MAX", 30)),
		plugins.WithGapFile(utils.EnvOr("AGENT_GAP_MARKERS", "./data/gap_markers.json")),
		plugins.WithImageHost(imgHost),
		plugins.WithSteerAck(utils.EnvInt("AGENT_STEER_ACK", 1) != 0))

	// 短视频解析（注册在 Agent 之前：命中分享链接即 Abort，不触发 AI 兜底）。
	b.UsePlugin(plugins.NewVideo(imgHost))
	b.UsePlugin(agentPlug)
	// 会话管理命令（.stop/.reset/.status/.usage/.mcp/.skill）。
	b.UsePlugin(plugins.NewSession(ag, os.Getenv("AI_MODEL"), agentPlug.ResetMarker, mem))

	// 清理顺序与原 main 内联版本的 defer LIFO 一致：agent → 熔断器 → MCP 连接。
	return ag, func() {
		ag.Close()
		if fo != nil {
			fo.Close()
		}
		agent.CloseAll(mcpClients)
	}
}
