// 短视频解析插件：群聊/私聊出现抖音、快手分享链接时自动解析并发视频 + 简介。
// 注册顺序须在 Agent 插件之前（命中即 Abort，不触发 AI 兜底）。
package plugins

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"qqbot/internal/bot"
)

// shareLinkRE 从分享文案中提取抖音/快手短链（链接混在口令文字中间，必须正则扫）。
var shareLinkRE = regexp.MustCompile(`https?://(?:v\.douyin\.com|v\.kuaishou\.com)/[A-Za-z0-9_-]+/?`)

// videoDedupTTL 同一条链接的去重窗口：群消息事件可能重复投递，且分享文案
// 常被多人连发，短时间内只解析一次。
const videoDedupTTL = 10 * time.Minute

// maxProxyBytes 分片直传允许下载的最大视频体积（官方分片上传硬限 200MB）。
const maxProxyBytes = 200 << 20

// VideoPlugin 短视频解析插件。
type VideoPlugin struct {
	chain    *ParserChain      // 多源解析链（按优先级故障转移，源定义见 videoparser.go/luoying.go/zhishuzhan.go）
	zs       *ZhishuzhanClient // zhishuzhan 源引用（注入扫码续期通知用；默认启用，不为 nil）
	notifier *chatNotifier     // 续期消息 → 触发解析的聊天（Install 时创建并注入 zs）
	client   *http.Client      // 下载视频直链用（独立于 resty，放宽超时）

	mu sync.Map // link-hash -> 解析开始时间（去重）

	pendMu   sync.Mutex // 护 pendLink/pendCtx
	pendLink string     // 最近一条解析失败的链接（扫码续期成功后由 retryPending 重试）
	pendCtx  *bot.Ctx   // 对应消息上下文（重试时重建发送锚点用）
}

// NewVideo 创建插件。
func NewVideo() *VideoPlugin {
	// 默认序：zhishuzhan 首选（默认启用、凭据可空，扫码登录自动引导，见 zhishuzhan.go），
	// luoying 备份（未配 key 返回 nil，orderParsers 剔除）；VIDEO_PARSERS 可重排/取子集。
	zs := NewZhishuzhanClientFromEnv()
	return &VideoPlugin{
		chain:  NewParserChain(orderParsers([]Parser{zs, NewLuoyingParserFromEnv()}, os.Getenv("VIDEO_PARSERS"))...),
		zs:     zs,
		client: &http.Client{Timeout: 10 * time.Minute},
	}
}

// Install 注册消息监听。
func (p *VideoPlugin) Install(b *bot.Bot) {
	if p.zs != nil {
		p.notifier = &chatNotifier{b: b}
		p.zs.SetNotifier(p.notifier)
		p.zs.SetOnCredsRefreshed(p.retryPending) // 扫码成功 → 自动重试刚才失败的链接
	}
	h := func(c *bot.Ctx) { p.onMessage(c) }
	b.OnGroupMessage(h)
	b.OnPrivateMessage(h)
}

// chatNotifier 把扫码续期消息发到「触发解析的那条消息所在的聊天」（群里/私聊里
// 直接出现二维码，谁都能扫）。流程最长 5 分钟，被动锚点会过期，统一用空锚点
// 主动发送（群/私聊主动消息已开通，机制同 plugins/online.go）。
type chatNotifier struct {
	b *bot.Bot

	mu                   sync.Mutex
	src, groupID, userID string // 最近一条触发解析的消息来源（Target 每次命中链接时刷新）
}

// Target 记录续期消息的投递目标（onMessage 命中分享链接时调用，晚写覆盖）。
func (n *chatNotifier) Target(c *bot.Ctx) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.src, n.groupID, n.userID = c.Source, c.GroupID(), c.UserID()
}

func (n *chatNotifier) SendText(ctx context.Context, text string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	n.mu.Lock()
	src, gid, uid := n.src, n.groupID, n.userID
	n.mu.Unlock()
	switch src {
	case "group":
		if _, err := n.b.SendGroupText(ctx, gid, "", text); err != nil {
			log.Printf("[video] 续期通知发群失败: %v", err)
		}
	case "c2c":
		if _, err := n.b.SendC2CText(ctx, uid, "", text); err != nil {
			log.Printf("[video] 续期通知发私聊失败: %v", err)
		}
	default:
		log.Printf("[video] 续期通知无处可发（尚无触发聊天）：%s", truncStr(text, 60))
	}
}

func (n *chatNotifier) SendImage(ctx context.Context, imageURL string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	n.mu.Lock()
	src, gid, uid := n.src, n.groupID, n.userID
	n.mu.Unlock()
	switch src {
	case "group":
		if _, err := n.b.SendGroupImage(ctx, gid, "", imageURL); err != nil {
			log.Printf("[video] 续期二维码发群失败: %v", err)
		}
	case "c2c":
		if _, err := n.b.SendC2CImage(ctx, uid, "", imageURL); err != nil {
			log.Printf("[video] 续期二维码发私聊失败: %v", err)
		}
	default:
		log.Printf("[video] 续期二维码无处可发（尚无触发聊天）")
	}
}

func (p *VideoPlugin) onMessage(c *bot.Ctx) {
	link := shareLinkRE.FindString(c.Text())
	if link == "" {
		return
	}
	if !p.claim(link) {
		c.Abort() // 刚解析过（重复投递/复读），吞掉但不重发
		return
	}
	c.Abort() // 命中即消费，不走 AI 兜底
	if p.notifier != nil {
		p.notifier.Target(c) // 续期消息要发回这个聊天（zhishuzhan 无凭据/失效时弹二维码）
	}
	hintID := replyBestEffort(c, "⏳ 正在解析...") // 多源解析/发视频可能要等，先给个即时反馈
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[video] panic: %v", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		if err := p.handle(ctx, c, link); err != nil {
			log.Printf("[video] %s 解析失败: %v", link, err)
			p.storePending(link, c) // 记住失败的那条：扫码续期成功后自动重试
			replyBestEffort(c, "视频解析失败："+truncStr(err.Error(), 150))
		}
		retractHint(c, hintID) // 视频已发出，撤掉提示
	}()
}

// storePending 记住最近一条解析失败的消息（retryPending 消费，晚写覆盖）。
func (p *VideoPlugin) storePending(link string, c *bot.Ctx) {
	p.pendMu.Lock()
	defer p.pendMu.Unlock()
	p.pendLink, p.pendCtx = link, c
}

// retryPending 扫码登录成功、凭据就绪后自动重试最近一条失败的解析。
// 用原消息字段重建 Ctx（原 c.Context 可能已随消息分发结束被取消），回复锚点仍是
// 原消息 id——扫码通常落在 5 分钟被动窗口内，个别超窗的发送失败会进日志。
func (p *VideoPlugin) retryPending() {
	p.pendMu.Lock()
	link, old := p.pendLink, p.pendCtx
	p.pendLink, p.pendCtx = "", nil
	p.pendMu.Unlock()
	if old == nil {
		return
	}
	p.forget(link) // 清去重键：重试若仍失败，手动重发同一条链接也放行
	nc := bot.NewEventCtx(old.Bot, old.Source, old.Text(), old.UserID(), old.Username(),
		old.GroupID(), old.MessageID(), old.Timestamp(), old.AtBot())
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[video] 重试 panic: %v", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		hintID := replyBestEffort(nc, "⏳ 正在解析...")
		if err := p.handle(ctx, nc, link); err != nil {
			log.Printf("[video] 重试 %s 仍失败: %v", link, err)
			replyBestEffort(nc, "视频解析失败："+truncStr(err.Error(), 150))
		} else {
			retractHint(nc, hintID)
		}
	}()
}

// linkKey 去重键：链接的 fnv 哈希（claim 记录 / forget 清除共用）。
func linkKey(link string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(link))
	return h.Sum64()
}

// forget 清掉链接的去重键（重试路径用）。
func (p *VideoPlugin) forget(link string) { p.mu.Delete(linkKey(link)) }

// claim 对同一条链接做时间窗去重；返回 false 表示窗口内已处理过。
func (p *VideoPlugin) claim(link string) bool {
	key := linkKey(link)
	now := time.Now()
	if v, ok := p.mu.LoadOrStore(key, now); ok {
		if t := v.(time.Time); now.Sub(t) < videoDedupTTL {
			return false
		}
	}
	p.mu.Store(key, now)
	// 顺手清理过期条目，防长期膨胀。
	p.mu.Range(func(k, v any) bool {
		if now.Sub(v.(time.Time)) > 24*time.Hour {
			p.mu.Delete(k)
		}
		return true
	})
	return true
}

func (p *VideoPlugin) handle(ctx context.Context, c *bot.Ctx, link string) error {
	res, err := p.chain.Parse(ctx, link) // 链式解析：平台分流/故障转移/聚合报错都在链上
	if err != nil {
		return err
	}

	// 原生 md 是主路径（含平台/清晰度/封面图）；官方保留异步审核权，被拒回落
	// 纯文本简介（无封面图，其余信息都在）。
	if _, err := c.ReplyMarkdown(res.SummaryMD()); err != nil {
		_, _ = c.ReplyText(res.Summary())
	}

	switch {
	case res.VideoURL != "":
		return p.sendVideo(ctx, c, res.VideoURL)
	case len(res.Images) > 0:
		n := 0
		for i, u := range res.Images {
			if i >= 10 {
				replyBestEffort(c, fmt.Sprintf("…共 %d 张图，仅发前 10 张", len(res.Images)))
				break
			}
			if _, err := c.ReplyImage(u); err != nil {
				log.Printf("[video] 发图 %d 失败: %v", i+1, err)
				continue
			}
			n++
		}
		if n == 0 {
			return fmt.Errorf("图集全部发送失败")
		}
		return nil
	default:
		return fmt.Errorf("解析结果里没有视频/图片")
	}
}

// sendVideo 发视频：直链 URL 模式（QQ 服务端代拉，快）→ 失败则下载字节分片直传
// （我们推给 QQ；大视频/源站拉不动时可靠）→ 仍失败发直链文本。
// QQ 拉 URL 有 ~10s 时限（code 850027「富媒体文件上传超时」），几十 MB 的大视频
// URL 模式必然超时，分片直传是唯一可靠路径（2026-10 生产实测，61MB/8min 视频）。
func (p *VideoPlugin) sendVideo(ctx context.Context, c *bot.Ctx, videoURL string) error {
	if _, err := c.ReplyVideo(videoURL); err == nil {
		return nil
	} else {
		log.Printf("[video] 直链发送失败，改分片直传: %v", err)
	}
	if data, contentType, err := p.download(ctx, videoURL); err != nil {
		log.Printf("[video] 下载视频失败: %v", err)
	} else if _, err := c.ReplyMediaBytes(2, "video"+extOf(contentType, videoURL), data); err == nil {
		return nil
	} else {
		log.Printf("[video] 分片直传发送失败: %v", err)
	}
	_, err := c.ReplyText("视频发送失败，直链（短期有效）：" + videoURL)
	return err
}

// download 拉取视频直链字节（带体积上限）。
func (p *VideoPlugin) download(ctx context.Context, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 13) AppleWebKit/537.36")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("下载 HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxProxyBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxProxyBytes {
		return nil, "", fmt.Errorf("视频超过 %dMB 直传上限", maxProxyBytes>>20)
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// ---- 解析结果模型 ----

// VideoResult 是各解析源统一后的结果。
type VideoResult struct {
	Platform string // 平台名（抖音/快手），简介展示用
	Title    string
	Author   string
	Like     int64
	Comment  int64
	Share    int64
	VideoURL string
	CoverURL string // 封面图（zhishuzhan 提供）；视频时经 md 内嵌到简介
	Images   []string
	Quality  string
	SizeStr  string
	Duration float64 // 秒
}

// Summary 渲染纯文本简介。
func (v *VideoResult) Summary() string {
	var b strings.Builder
	b.WriteString("🎬 ")
	if v.Platform != "" {
		b.WriteString("[" + v.Platform + "] ")
	}
	b.WriteString(oneLine(v.Title, 120))
	b.WriteString("\n")
	var meta []string
	if v.Author != "" {
		meta = append(meta, "作者 "+v.Author)
	}
	if v.Like > 0 || v.Comment > 0 || v.Share > 0 {
		meta = append(meta, fmt.Sprintf("👍%s 💬%s 🔁%s", fmtNum(v.Like), fmtNum(v.Comment), fmtNum(v.Share)))
	}
	var av []string
	if v.Quality != "" {
		av = append(av, v.Quality)
	}
	if v.Duration > 0 {
		av = append(av, fmtDur(v.Duration))
	}
	if v.SizeStr != "" {
		av = append(av, v.SizeStr)
	}
	if len(av) > 0 {
		meta = append(meta, strings.Join(av, " · "))
	}
	if len(meta) > 0 {
		b.WriteString(strings.Join(meta, " ｜ "))
		b.WriteString("\n")
	}
	if v.VideoURL == "" && len(v.Images) > 0 {
		b.WriteString(fmt.Sprintf("图集 %d 张\n", len(v.Images)))
	}
	return b.String()
}

// SummaryMD 渲染 markdown 简介（原生 md 是主路径）：在纯文本基础上内嵌封面图
// （原生 md 的 ![图片](url) 语法，QQ 发送时会转存该图）。图集不嵌——后面会逐张发。
func (v *VideoResult) SummaryMD() string {
	s := v.Summary()
	if v.VideoURL != "" && v.CoverURL != "" {
		return s + fmt.Sprintf("![封面](%s)\n", v.CoverURL)
	}
	return s
}

// ---- 小工具 ----

// replyBestEffort 发文本：优先原生 markdown（本机器人群/私聊实测可发，菜单/点歌在用），
// 失败回落纯文本——官方公域保留 md 异步审核权，个别消息可能延迟或被拒。
// 返回实际发出的消息 id（撤回提示消息用；两端都失败为空串）。
func replyBestEffort(c *bot.Ctx, s string) string {
	if id, err := c.ReplyMarkdown(s); err == nil {
		return id
	}
	id, _ := c.ReplyText(s)
	return id
}

// retractHint 发送完视频后撤回「正在解析」提示。官方限制发出超过 2 分钟的消息
// 不可撤回——慢解析下撤回失败属正常，只记日志，提示残留无害。
func retractHint(c *bot.Ctx, msgID string) {
	if msgID == "" || c.Bot == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var err error
	switch c.Source {
	case "group":
		err = c.Bot.DeleteGroupMessage(ctx, c.GroupID(), msgID)
	case "c2c":
		err = c.Bot.DeleteC2CMessage(ctx, c.UserID(), msgID)
	}
	if err != nil {
		log.Printf("[video] 撤回解析提示失败: %v", err)
	}
}

func oneLine(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
		return string(r) + "…"
	}
	if s == "" {
		return "(无标题)"
	}
	return s
}

func fmtNum(n int64) string {
	switch {
	case n >= 100_000_000:
		return strconv.FormatFloat(float64(n)/100_000_000, 'f', 1, 64) + "亿"
	case n >= 10_000:
		return strconv.FormatFloat(float64(n)/10_000, 'f', 1, 64) + "万"
	default:
		return strconv.FormatInt(n, 10)
	}
}

func fmtDur(sec float64) string {
	s := int(sec)
	if s >= 60 {
		return fmt.Sprintf("%d分%02d秒", s/60, s%60)
	}
	return fmt.Sprintf("%d秒", s)
}

// extOf 从 Content-Type 或 URL 推断扩展名（分片直传文件名用）。
func extOf(contentType, url string) string {
	if i := strings.Index(contentType, "/"); i >= 0 {
		if e := strings.TrimSpace(strings.SplitN(contentType[i+1:], ";", 2)[0]); e != "" && e != "octet-stream" {
			if !strings.ContainsAny(e, "/\\") && len(e) <= 5 {
				return e
			}
		}
	}
	if i := strings.LastIndex(url, "."); i >= 0 {
		if e := url[i+1:]; len(e) <= 5 && strings.Trim(e, "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") == "" {
			return e
		}
	}
	return "mp4"
}
