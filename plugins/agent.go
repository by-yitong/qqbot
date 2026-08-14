package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"qqbot/internal/agent"
	"qqbot/internal/bot"
	"qqbot/internal/imgstore"
	"qqbot/internal/memory"
	"qqbot/internal/scheduler"
)

// AgentPlugin 是「未命中命令时交给 agent 回复」的插件。
//
// 记忆分层：
//   - session（短期）：群消息用群共享 key（g:群id，全群一份历史），私聊用用户 openid；
//     群消息正文带 [昵称] 前缀，模型能分清说话人。
//   - memory（长期）：RecorderPlugin 全量落库；本插件在 Run 前把「上次对话以来的
//     新群消息」作为持久补充块并进 session（随历史落盘，下轮自动加载、不重复注入），
//     并把 bot 的回复也记进库。增量覆盖点（marker）落盘，重启后接着增量、不重复铺底。
type AgentPlugin struct {
	ag           *agent.Agent
	mem          *memory.Store
	recentWindow time.Duration  // 首次接触（新群）时的全量回退窗口
	recentMax    int            // 注入条数上限
	steerAck     bool           // 补充消息入队成功后是否回一句轻确认
	gapFile      string         // marker 持久化文件（空 = 仅内存，重启回退全量窗口）
	imgHost      *imgstore.Host // 发图床（send_image 收到本地文件路径时经此转公网 URL）

	gapMu   sync.Mutex
	lastGap map[string]time.Time // 群id → 上次注入覆盖到的最后一条消息时间
}

// AgentOption 配置 AgentPlugin。
type AgentOption func(*AgentPlugin)

// WithMemory 启用长期记忆联动（近期注入 + bot 回复落库）。
func WithMemory(mem *memory.Store, window time.Duration, max int) AgentOption {
	return func(p *AgentPlugin) {
		p.mem = mem
		if window > 0 {
			p.recentWindow = window
		}
		if max > 0 {
			p.recentMax = max
		}
	}
}

// WithGapFile 持久化各群的注入覆盖点（marker）。配置后重启不回退全量窗口，
// 增量注入无缝续上。
func WithGapFile(path string) AgentOption {
	return func(p *AgentPlugin) { p.gapFile = path }
}

// WithImageHost 配置图床：send_image 收到本地文件路径（如浏览器截图）时，
// 经此上传转公网 URL 再发（QQ 富媒体是 URL 模式，拉不到 localhost）。
func WithImageHost(h *imgstore.Host) AgentOption {
	return func(p *AgentPlugin) { p.imgHost = h }
}

// WithSteerAck 设置补充消息入队成功后是否回一句轻确认（默认开）。
func WithSteerAck(on bool) AgentOption {
	return func(p *AgentPlugin) { p.steerAck = on }
}

// NewAgent 用配置好的 agent 构造插件（默认首次全量窗口 60 分钟 / 30 条，补充确认开）。
func NewAgent(ag *agent.Agent, opts ...AgentOption) *AgentPlugin {
	p := &AgentPlugin{
		ag:           ag,
		recentWindow: 60 * time.Minute,
		recentMax:    30,
		steerAck:     true,
		lastGap:      map[string]time.Time{},
	}
	for _, opt := range opts {
		opt(p)
	}
	if p.gapFile != "" {
		if m, err := loadGapMarkers(p.gapFile); err != nil {
			log.Printf("[agent] 读取注入覆盖点失败（从空开始）: %v", err)
		} else {
			p.lastGap = m
		}
	}
	return p
}

// loadGapMarkers 读 marker 文件（{群id: RFC3339 时间}；不存在视为空）。
func loadGapMarkers(path string) (map[string]time.Time, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]time.Time{}, nil
		}
		return nil, err
	}
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]time.Time, len(raw))
	for k, v := range raw {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			out[k] = t
		}
	}
	return out, nil
}

// saveGapMarkers 写 marker 文件（调用方持 gapMu）。
func saveGapMarkers(path string, m map[string]time.Time) error {
	raw := make(map[string]string, len(m))
	for k, t := range m {
		raw[k] = t.Format(time.RFC3339)
	}
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// ResetMarker 清掉某会话的注入覆盖点（配合 .reset：清空 session 后下次重新全量铺底）。
// 传 session key（g:群id 或用户 openid），非群会话无 marker、直接忽略。
func (p *AgentPlugin) ResetMarker(sessionKey string) {
	gid := strings.TrimPrefix(sessionKey, "g:")
	if gid == sessionKey {
		return
	}
	p.gapMu.Lock()
	defer p.gapMu.Unlock()
	if _, ok := p.lastGap[gid]; !ok {
		return
	}
	delete(p.lastGap, gid)
	p.persistMarkers()
}

// persistMarkers 落盘 marker（调用方持 gapMu；未配置文件则 no-op）。
func (p *AgentPlugin) persistMarkers() {
	if p.gapFile == "" {
		return
	}
	if err := saveGapMarkers(p.gapFile, p.lastGap); err != nil {
		log.Printf("[agent] 注入覆盖点落盘失败: %v", err)
	}
}

// Install 注册监听器（私聊 + 群）。
//
// agent 处理整体异步：botgo 的 WSS 事件是单 goroutine 串行分发的，若同步跑 agent，
// 一次几十秒的 Run 会堵住分发循环，后续消息（包括补充与 .stop）根本到不了 handler。
func (p *AgentPlugin) Install(b *bot.Bot) {
	async := func(c *bot.Ctx) {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[agent] 处理 panic: %v\n%s", r, debug.Stack())
				}
			}()
			p.handle(c)
		}()
	}
	b.OnPrivateMessage(async) // 私聊
	b.OnGroupMessage(async)   // 群（@ 消息）
}

// SessionKey 返回消息所属的 agent 会话 key：群聊全群共享（g:群id），私聊按用户。
// 插件路由与 .stop/.reset/.status 命令都用它定位同一份会话。
func SessionKey(c *bot.Ctx) string {
	if c.IsGroup() {
		return "g:" + c.GroupID()
	}
	return c.UserID()
}

func (p *AgentPlugin) handle(c *bot.Ctx) {
	// 群里：只在 @机器人 或 带命令前缀 时回复，避免对每条群消息都回（刷屏）。
	text := c.Text()
	if c.IsGroup() {
		switch {
		case c.AtBot():
			// @机器人：用原文（@ 前缀已由框架 ETLInput 去掉）
		default:
			stripped, hit := c.StripPrefix()
			if !hit {
				return // 既没 @ 也没前缀，群聊忽略
			}
			text = stripped // 带前缀：用去掉前缀后的文本
		}
	}
	question := strings.TrimSpace(text)
	images := c.Images()
	if question == "" && len(images) == 0 {
		return // 既无文本也无图片，不处理
	}
	// 说话人标注：群聊正文前加 [昵称]（模型分清谁在说；也随 session 落盘，历史可追溯）。
	if c.IsGroup() && c.Username() != "" {
		question = "[" + c.Username() + "] " + question
	}
	// 引用回复：把被引用的原文拼进输入，让 agent 有上下文（如「翻译这句」）。
	if q := c.Quote(); q != "" {
		question = "[引用的消息]\n" + q + "\n\n[我的消息]\n" + question
	}
	// 用户发了图片：把图片 URL 拼进输入。
	if len(images) > 0 {
		question = appendImagesToQuestion(question, images)
	}

	// 会话 key：群共享（g:群id），私聊按用户。
	key := SessionKey(c)

	// 补充优先（steering）：该会话有活跃 Run 时，把消息注入当前处理（turn 边界生效，
	// 不打断在飞的工具），而非排队等上一轮结束。返回 false 说明没有进行中的 Run，
	// 走正常 Run 路径。
	if p.ag.Steer(key, question) {
		if p.steerAck {
			replyTextWithMention(c, "（已收到，会并入当前任务继续处理）")
		}
		return
	}

	// 近期动态注入（长期记忆 → 持久补充块并进 session，下轮自动随历史加载；见 ctxBlockOf）。
	runCtx := c.Context
	var opts []agent.RunOption
	opts = append(opts, agent.WithResponder(&imageResponder{c: c, host: p.imgHost}))
	if p.mem != nil {
		runCtx = memory.WithScope(runCtx, p.scopeOf(c)) // search_history 工具按此隔离查询
		if blk := p.ctxBlockOf(c); blk != "" {
			opts = append(opts, agent.WithPersistBlock(blk))
		}
	}
	// 当前聊天信息注入：create_task 等工具据此拿到定时任务的默认发送目标与创建者。
	runCtx = scheduler.WithChatInfo(runCtx, chatInfoOf(c))

	reply, err := p.ag.Run(runCtx, key, question, opts...)
	if err != nil {
		if errors.Is(err, agent.ErrInterrupted) {
			return // 被用户主动停止：.stop 命令已回复「已停止」，这里静默
		}
		log.Printf("[agent] %v", err)
		replyTextWithMention(c, "处理失败："+truncStr(err.Error(), 200))
		return
	}
	if reply != "" {
		// AI 回复统一走 markdown（纯文本作为 markdown 也合法，渲染基本无害）；
		// 群/频道开头 @发送人，多人群聊里能看清是在回谁。记忆里存不带 @ 的原文。
		if _, serr := c.ReplyMarkdown(mentionPrefix(c) + reply); serr == nil {
			p.recordReply(c, reply)
		} else if _, terr := c.ReplyText(mentionPrefix(c) + reply); terr == nil {
			p.recordReply(c, reply) // markdown 发不出（如语料审核拒绝）时回退纯文本
		}
	}
}

// mentionPrefix 群聊/频道回复开头带上 @发送人（群 <@openid>，频道 <@!id>）；
// 私聊一对一不需要，返回空。艾特语法在 markdown 消息里生效，文本消息未确认支持，
// 故需要艾特的短文本统一经 markdown 发。
func mentionPrefix(c *bot.Ctx) string {
	if c.UserID() == "" {
		return ""
	}
	switch c.Source {
	case "group":
		return "<@" + c.UserID() + "> "
	case "channel":
		return "<@!" + c.UserID() + "> "
	}
	return ""
}

// replyTextWithMention 发一条带 @ 前缀的短文本：优先 markdown（艾特可渲染），
// 失败回退纯文本（不带艾特，保证消息能出去）。
func replyTextWithMention(c *bot.Ctx, text string) {
	if m := mentionPrefix(c); m != "" {
		if _, err := c.ReplyMarkdown(m + text); err == nil {
			return
		}
	}
	_, _ = c.ReplyText(text)
}

// truncStr 截断长文本（错误信息回给用户时防止刷屏）。
func truncStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// scopeOf 由消息来源构造记忆查询范围（群按群、私聊按用户；search_history 群隔离的依据）。
func (p *AgentPlugin) scopeOf(c *bot.Ctx) memory.Scope {
	if c.IsGroup() {
		return memory.Scope{Source: "group", GroupID: c.GroupID()}
	}
	return memory.Scope{Source: "c2c", UserID: c.UserID()}
}

// ctxBlockOf 构造本次的持久补充块（增量注入，作为普通 user 消息并进 session、
// 随历史落盘——下轮自动加载，不重复注入）。
//
//   - 群聊增量：只注入「上次注入覆盖点之后 → 本条消息之前」的新消息；
//     期间没人说话（或全被过滤）则不注入。
//   - 新群首次接触：全量窗口铺底（recentWindow/recentMax）。marker 落盘后
//     重启不回退，增量无缝续上。
//   - 排除 bot 自己的回复（session 里已有）和当前这条消息（马上作为问题追加）。
//   - 私聊不注入：私聊每条消息本就走 agent，全在 session 里。
//
// 注入点时间戳随注入推进；Run 失败也不回退（内容已在 session 里，且长期库可查）。
func (p *AgentPlugin) ctxBlockOf(c *bot.Ctx) string {
	if p.mem == nil || !c.IsGroup() {
		return ""
	}
	sc := p.scopeOf(c)

	p.gapMu.Lock()
	since, hasMarker := p.lastGap[c.GroupID()]
	p.gapMu.Unlock()

	var (
		rows  []memory.Message
		err   error
		label string
	)
	if hasMarker {
		rows, err = p.mem.RecentAfter(sc, since, p.recentMax)
		label = "上次对话以来的新消息"
	} else {
		rows, err = p.mem.Recent(sc, p.recentWindow, p.recentMax)
		label = "最近 " + shortenDur(p.recentWindow) + " 的消息"
	}
	if err != nil {
		log.Printf("[agent] 查询群聊动态失败（忽略）: %v", err)
		return ""
	}

	filtered := rows[:0]
	for _, m := range rows {
		if m.UserID == "bot" || m.MsgID == c.MessageID() {
			continue
		}
		filtered = append(filtered, m)
	}
	if len(filtered) == 0 {
		return ""
	}

	// 注入点推进到「当前这条 @ 消息的时间」（而非最后一条被注入行）：上一轮的 @ 消息
	// 已作为问题进了 session 历史，不该在下一轮增量里重复出现；期间没人说话时
	// RecentAfter 恰好取空。+1s 兜底同秒精度差；解析失败退化为最后注入行的时间。
	marker := filtered[len(filtered)-1].Ts
	if ct, err := time.Parse(time.RFC3339, c.Timestamp()); err == nil {
		if ct = ct.Add(time.Second); ct.After(marker) {
			marker = ct
		}
	}
	p.gapMu.Lock()
	p.lastGap[c.GroupID()] = marker
	p.persistMarkers()
	p.gapMu.Unlock()

	return "[群聊记录（" + label + "，系统自动记录，非对bot直接说的话）]\n" +
		memory.FormatMessages(filtered)
}

// recordReply 把 bot 的成功回复也记进长期记忆（UserID=bot；MsgID 由触发消息派生保证唯一）。
func (p *AgentPlugin) recordReply(c *bot.Ctx, reply string) {
	if p.mem == nil {
		return
	}
	p.mem.Record(memory.Message{
		MsgID:    "bot:" + c.MessageID(),
		Ts:       time.Now(),
		Source:   c.Source,
		GroupID:  c.GroupID(),
		UserID:   "bot",
		Username: "机器人",
		Content:  reply,
		AtBot:    true,
	})
}

// shortenDur 把时长转成人话（60m→1h0m / 90m→1h30m / 30m→30m）。
func shortenDur(d time.Duration) string {
	mins := int(d.Minutes())
	if mins%60 == 0 {
		return itoa(mins/60) + "小时"
	}
	if mins >= 60 {
		return itoa(mins/60) + "小时" + itoa(mins%60) + "分钟"
	}
	return itoa(mins) + "分钟"
}

// imageResponder 把 *bot.Ctx 包成 agent.Responder，让 agent 工具（send_image）能发图到当前会话。
// url 为 http(s)/genimg: 引用时直发；否则视为本地文件路径，经图床转公网 URL 后发
// （QQ 富媒体是 URL 模式，QQ 服务器拉不到 localhost，必须先上图床）。
type imageResponder struct {
	c    *bot.Ctx
	host *imgstore.Host
}

// SendImage 实现 agent.Responder。
func (r *imageResponder) SendImage(ctx context.Context, url string) error {
	if !isRemoteImage(url) {
		path := os.ExpandEnv(url)
		if strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, path[2:])
			}
		}
		if !r.host.Configured() {
			return fmt.Errorf("本地图片需上传图床，但图床未配置（IMGHOST_BASE_URL 为空）: %s", url)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("读本地图片失败: %w", err)
		}
		publicURL, err := r.host.Upload(ctx, data, filepath.Ext(path))
		if err != nil {
			return err
		}
		url = publicURL
	}
	_, err := r.c.ReplyImage(url)
	return err
}

// isRemoteImage 判断发图参数是否为可直接发送的远程引用（http(s) URL 或 genimg: 引用）；
// 否则按本地文件路径处理。
func isRemoteImage(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "genimg:")
}

// appendImagesToQuestion 把用户发送的图片 URL 拼进 agent 输入文本。
func appendImagesToQuestion(question string, imgs []bot.Image) string {
	var sb strings.Builder
	sb.WriteString(question)
	if question != "" {
		sb.WriteString("\n\n")
	}
	if len(imgs) == 1 {
		sb.WriteString("[用户发送了1张图片，URL：")
		sb.WriteString(imgs[0].URL)
		sb.WriteString("]")
		return sb.String()
	}
	sb.WriteString("[用户发送了")
	sb.WriteString(itoa(len(imgs)))
	sb.WriteString("张图片，URL：]\n")
	for i, img := range imgs {
		sb.WriteString(itoa(i + 1))
		sb.WriteString(". ")
		sb.WriteString(img.URL)
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

// itoa 简单正整数转字符串（避免引入 strconv）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
