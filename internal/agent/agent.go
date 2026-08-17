package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	neturl "net/url"
	"path"
	"strings"
	"sync"
	"time"
)

// Config 是 Agent 的配置。
type Config struct {
	LLM                 Completer     // 必填：LLM 客户端
	SystemPrompt        string        // base 人设，可选
	Tools               *Registry     // nil = 不启用工具（退化为单轮对话）
	Sessions            *SessionStore // nil = 无状态单轮（向后兼容）；非空则每用户多轮历史
	MaxIterations       int           // 工具循环上限，默认 8
	LoopTimeout         time.Duration // 单次 Run 的总时长上限，默认 240s（守住 5min 被动回复窗口）
	CompactionThreshold int           // token 估算阈值，>0 才启用压缩，默认 0(关)
	KeepRecent          int           // 压缩时保留的近期消息条数，默认 10
	Skills              []Skill       // 注入 system 的技能（SKILL.md）
	RuntimeMCPCfg       string        // 运行时添加的 MCP server 持久化文件（空 = 不持久化）
}

// Agent 是带工具循环的对话 agent。
type Agent struct {
	cfg Config

	skills     *skillStore  // 动态技能容器（运行时可加）
	mcpMu      sync.Mutex   // 保护 mcpClients
	mcpClients []*MCPClient // 运行时动态连接的 MCP server

	// 运行注册表：会话 key → 当前 Run（Steer 补充 / Interrupt 打断的载体）。
	runsMu sync.Mutex
	runs   map[string]*runHandle
}

// New 创建 agent，补齐默认值。
func New(cfg Config) *Agent {
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = 8
	}
	if cfg.LoopTimeout <= 0 {
		cfg.LoopTimeout = 240 * time.Second
	}
	if cfg.KeepRecent <= 0 {
		cfg.KeepRecent = 10
	}
	return &Agent{cfg: cfg, skills: newSkillStore(cfg.Skills), runs: map[string]*runHandle{}}
}

// Run 处理一次用户输入：拉历史（若有）、跑工具循环、返回最终回复文本。
//
// userID 用于定位会话历史（Sessions 为 nil 时忽略）。
//
// 流程：
//  1. 若启用 Sessions，取该用户会话、加锁、载入历史。
//  2. 追加本次用户输入。
//  3. 循环：调 LLM；若返回工具调用则执行并把结果喂回，再继续；否则结束。
//     每轮 LLM 前注入运行中补充的消息（Steer，参考 pi 的 steering）；
//     纯文本回复后若又有补充，则继续循环作答。
//  4. 循环上限 MaxIterations / 总超时 LoopTimeout 二选一先到即停；
//     补充注入会重置迭代计数（总时长仍由 LoopTimeout 硬保护）。
//     Interrupt 的取消与超时走同一条 ctx 取消路径。
//  5. 若启用 Sessions，把新历史落盘（被取消的 Run 也保留部分历史）。
func (a *Agent) Run(ctx context.Context, userID, userText string, opts ...RunOption) (string, error) {
	o := &runOptions{}
	for _, opt := range opts {
		opt(o)
	}
	// ctx 链：parent → 手动取消（Interrupt）→ 总超时（LoopTimeout）。
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	loopCtx, cancelLoop := context.WithTimeout(runCtx, a.cfg.LoopTimeout)
	defer cancelLoop()
	if o.responder != nil {
		// 让工具（如 send_image）能经 ResponderFrom 取到当前会话的 responder。
		loopCtx = context.WithValue(loopCtx, responderKey{}, o.responder)
	}

	// 注册运行 handle（Steer/Interrupt 的入口）；defer 注销。
	h := a.startRun(userID, cancelRun)
	defer a.endRun(userID, h)

	// 多轮记忆：拉历史，并在 loop 期间锁住该用户会话（串行化同一用户的并发 Run）。
	var sess *Session
	var msgs []Message
	if a.cfg.Sessions != nil {
		sess = a.cfg.Sessions.Get(userID)
		sess.Lock()
		defer sess.Unlock()
		msgs = append(msgs, sess.history...)
	}
	// 补充块插在问题前、位于请求尾部，不破坏 system+历史 的前缀（上下文缓存）。
	// 临时块（ctxBlock）发完即弃；持久块（persistBlk）是普通消息，随 session 落盘。
	if o.ctxBlock != "" {
		msgs = append(msgs, newEphemeralUser(o.ctxBlock))
	}
	if o.persistBlk != "" {
		msgs = append(msgs, NewUser(o.persistBlk))
	}
	msgs = append(msgs, NewUser(userText))

	var lastReply string
	var overflowCompacted bool // 每次 Run 至多做一次溢出压缩+重试，防死循环
	iter := 0
	for {
		// turn 边界：注入运行中补充的消息（steering）。用户主动补充重置迭代预算。
		if steered := h.drainSteer(); len(steered) > 0 {
			for _, s := range steered {
				msgs = append(msgs, NewUser(s))
			}
			iter = 0
		}
		if err := loopCtx.Err(); err != nil {
			break // 被取消（Interrupt/父ctx）或超时：直接退出；取消且无回复 → ErrInterrupted
		}
		if iter >= a.cfg.MaxIterations {
			// 预算尽：无补充则收工；恰好又来了补充则继续（closeIfIdle 与 Steer 同锁互斥）。
			if h.closeIfIdle() {
				break
			}
			continue
		}
		iter++

		// 上下文压缩：token 估算超阈值时，把旧前缀摘要、保留近期窗口。
		if a.cfg.CompactionThreshold > 0 && ShouldCompact(msgs, a.cfg.CompactionThreshold) {
			if compacted, err := Compact(loopCtx, a.cfg.LLM, msgs, a.cfg.KeepRecent); err != nil {
				log.Printf("[agent] 压缩失败，沿用原历史: %v", err)
			} else {
				msgs = compacted
			}
		}

		var tools []map[string]any
		if a.cfg.Tools != nil {
			tools = a.cfg.Tools.OpenAISchemas()
		}
		sys := a.systemPromptFor(o)
		result, err := a.cfg.LLM.Complete(loopCtx, sys, msgs, tools)
		// 上下文超长：强制压缩（用更紧凑的历史）后重试一次。每次 Run 至多一次，避免死循环。
		// 急救路径用比配置更紧的保留窗口（配置可能设得很大——曾有人设 120，压缩完仍超限）。
		if err != nil && errors.Is(err, ErrContextOverflow) && !overflowCompacted {
			overflowCompacted = true
			keep := a.cfg.KeepRecent
			if keep <= 0 || keep > 20 {
				keep = 20
			}
			if compacted, cerr := Compact(loopCtx, a.cfg.LLM, msgs, keep); cerr == nil {
				msgs = compacted
				log.Printf("[agent] 上下文超长，已压缩急救（保留 %d 条近期消息）", keep)
				result, err = a.cfg.LLM.Complete(loopCtx, sys, msgs, tools)
			} else {
				log.Printf("[agent] 溢出急救压缩失败: %v", cerr)
			}
		}
		if err != nil {
			if errors.Is(loopCtx.Err(), context.Canceled) {
				// 被主动停止：有中间回复就返回它，否则交给 ErrInterrupted 分支。
				if lastReply != "" {
					a.persist(sess, msgs)
					return lastReply, nil
				}
				a.persist(sess, msgs)
				return "", ErrInterrupted
			}
			// 已有中间回复就先返回它，避免工具跑完却因最后一次调用失败而空手而归。
			if lastReply != "" {
				a.persist(sess, msgs)
				return lastReply, nil
			}
			return "", fmt.Errorf("agent: LLM 调用失败: %w", err)
		}

		// 记录 assistant 消息（含工具调用），再决定是否继续。
		msgs = append(msgs, Message{
			Role:      RoleAssistant,
			Content:   result.Content,
			ToolCalls: result.ToolCalls,
		})
		if result.Content != "" {
			lastReply = result.Content
		}

		if len(result.ToolCalls) == 0 {
			// 纯文本回复：无补充则正常结束；期间又来了补充则继续作答。
			if h.closeIfIdle() {
				break
			}
			continue
		}

		// 执行工具，结果逐条作为 tool 消息喂回。
		// 每个工具前查一次取消：被停止的调用以占位结果收尾，保证 assistant 的
		// 每个 tool_call 都有对应 result（OpenAI 协议硬性要求，不留孤儿）。
		for _, tc := range result.ToolCalls {
			out := "[已打断，未执行]"
			if loopCtx.Err() == nil {
				out = a.executeTool(loopCtx, tc)
			}
			msgs = append(msgs, Message{
				Role:       RoleTool,
				Content:    out,
				ToolCallID: tc.ID,
				Name:       tc.Name,
			})
		}
		if loopCtx.Err() != nil {
			break // 工具批被打断：历史收尾后退出
		}
	}

	a.persist(sess, msgs)

	if lastReply == "" {
		if errors.Is(loopCtx.Err(), context.Canceled) {
			return "", ErrInterrupted
		}
		return "", errors.New("agent: 未产生回复（达到迭代上限或超时）")
	}
	return lastReply, nil
}

// DefaultSystemPrompt 是未配置 cfg.SystemPrompt 时的人设兜底（QQ bot 取向）。
const DefaultSystemPrompt = `你是一个接入 QQ 的智能助手，运行在一个带工具循环的 agent harness 里。
你能调用工具来查时间、抓网页、联网搜索、读写文件、发图片等；按需组合工具完成用户的请求。
用与用户一致的语言回答，简洁、直接、不啰嗦；不确定就说不确定，不要编造。`

// systemPrompt 拼装每轮的 system 文本：人设（cfg.SystemPrompt 优先，否则 DefaultSystemPrompt）
// + 工具区（Available tools + Guidelines，按当前注册的工具动态生成）+ 技能索引（若有）。
// 每轮重建，从而随 active tools 与运行时新加的技能/工具变化（参考 pi 的 prepareNextTurn）。
func (a *Agent) systemPrompt() string {
	var b strings.Builder
	base := a.cfg.SystemPrompt
	if strings.TrimSpace(base) == "" {
		base = DefaultSystemPrompt
	}
	b.WriteString(base)

	if a.cfg.Tools != nil {
		if block := toolsPromptBlock(a.cfg.Tools); block != "" {
			b.WriteString("\n\n")
			b.WriteString(block)
		}
	}

	if sp := SkillsPrompt(a.skills.List()); sp != "" {
		b.WriteString("\n\n")
		b.WriteString(sp)
	}
	return b.String()
}

// systemPromptFor 目前等价 systemPrompt（动态上下文已改为临时消息注入，不再拼 system，
// 保证 system 前缀稳定以命中 LLM 上下文缓存）。保留此函数作为拼装点。
func (a *Agent) systemPromptFor(o *runOptions) string {
	return a.systemPrompt()
}

// persist 把历史写回会话（sessions 为 nil 时无操作）。临时消息（ephemeral）不落盘。
func (a *Agent) persist(sess *Session, msgs []Message) {
	if sess == nil {
		return
	}
	sess.history = stripEphemeral(msgs)
	if err := sess.save(); err != nil {
		log.Printf("[agent] 会话保存失败: %v", err)
	}
}

// executeTool 执行单个工具调用；出错时返回错误文本（不中断 loop），让模型自行处理。
func (a *Agent) executeTool(ctx context.Context, tc ToolCall) string {
	if a.cfg.Tools == nil {
		return "错误: 工具未启用"
	}
	tool, ok := a.cfg.Tools.Get(tc.Name)
	if !ok {
		return fmt.Sprintf("错误: 未知工具 %q", tc.Name)
	}
	out, err := tool.Execute(ctx, tc.Arguments)
	if err != nil {
		return fmt.Sprintf("错误: %v", err)
	}
	return out
}

// --- 运行时动态能力（发 URL 即可加技能 / MCP）---

// AddSkill 运行时追加一个技能（立即注入后续对话的 system prompt）。
func (a *Agent) AddSkill(sk Skill) { a.skills.Add(sk) }

// Skills 返回当前已加载的技能（启动时 + 运行时添加的）。
func (a *Agent) Skills() []Skill { return a.skills.List() }

// AddSkillFromURL 下载指定 URL 的 SKILL.md 内容、解析成技能并加入。
// url 应指向一个可 GET 的 markdown 文本（YAML frontmatter + 正文）。
func (a *Agent) AddSkillFromURL(ctx context.Context, url string) (Skill, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Skill{}, fmt.Errorf("非法 URL: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Skill{}, fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return Skill{}, fmt.Errorf("读取失败: %w", err)
	}
	if resp.StatusCode >= 400 {
		return Skill{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	name, desc, body := splitFrontmatter(string(data))
	if name == "" {
		base := path.Base(url)
		name = strings.TrimSuffix(base, path.Ext(base))
	}
	sk := Skill{Name: name, Description: desc, Content: body, FilePath: url}
	a.AddSkill(sk)
	return sk, nil
}

// AddMCPFromConfig 连接一个 MCP server（stdio/http/sse 均可），把它的工具并入
// registry。同名防重（已存在 mcp_<name>_ 前缀工具则拒绝）；连接成功后若配置了
// RuntimeMCPCfg 会落盘，重启自动重连。返回接入的工具数。
func (a *Agent) AddMCPFromConfig(ctx context.Context, cfg MCPServerConfig) (int, error) {
	if a.cfg.Tools == nil {
		return 0, errors.New("工具未启用（Tools 为 nil）")
	}
	if cfg.Name == "" {
		cfg.Name = defaultMCPName(cfg)
	}
	prefix := "mcp_" + sanitizeToolName(cfg.Name) + "_"
	for _, n := range a.cfg.Tools.Names() {
		if strings.HasPrefix(n, prefix) {
			return 0, fmt.Errorf("%s 已连接过（已有 %s 前缀的工具），无需重复添加", cfg.Name, prefix)
		}
	}
	mc, err := DialMCP(ctx, cfg)
	if err != nil {
		return 0, err
	}
	tools, err := mc.Tools(ctx)
	if err != nil {
		_ = mc.Close()
		return 0, err
	}
	for _, t := range tools {
		a.cfg.Tools.Register(t)
	}
	a.mcpMu.Lock()
	a.mcpClients = append(a.mcpClients, mc)
	a.mcpMu.Unlock()
	if err := SaveRuntimeMCP(a.cfg.RuntimeMCPCfg, []MCPServerConfig{cfg}); err != nil {
		log.Printf("[mcp] %s 已连接但持久化失败: %v", cfg.Name, err)
	}
	return len(tools), nil
}

// AddMCPFromURL 连接一个 http/sse 类型的 MCP server（便捷封装，见 AddMCPFromConfig）。
// serverType 为 "http" 或 "sse"（空默认 http）。返回接入的工具数。
func (a *Agent) AddMCPFromURL(ctx context.Context, serverType, url string) (int, error) {
	if serverType == "" {
		serverType = "http"
	}
	return a.AddMCPFromConfig(ctx, MCPServerConfig{Type: serverType, URL: url, Name: mcpNameFromURL(url)})
}

// Close 关闭运行时动态连接的 MCP server（启动时静态连接的由 main.go 自行管理）。
func (a *Agent) Close() {
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()
	for _, mc := range a.mcpClients {
		_ = mc.Close()
	}
	a.mcpClients = nil
}

// mcpNameFromURL 从 URL 取 host 作 MCP server 实例名（规整成安全字符）。
func mcpNameFromURL(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil || u.Host == "" {
		return "server"
	}
	return sanitizeToolName(u.Host)
}
