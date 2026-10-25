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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"qqbot/internal/bot"
	"qqbot/internal/imgstore"
)

// shareLinkRE 从分享文案中提取抖音/快手短链（链接混在口令文字中间，必须正则扫）。
var shareLinkRE = regexp.MustCompile(`https?://(?:v\.douyin\.com|v\.kuaishou\.com)/[A-Za-z0-9_-]+/?`)

// videoDedupTTL 同一条链接的去重窗口：群消息事件可能重复投递，且分享文案
// 常被多人连发，短时间内只解析一次。
const videoDedupTTL = 10 * time.Minute

// maxProxyBytes 走图床中转时允许下载的最大视频体积（QQ URL 模式对大文件不稳）。
const maxProxyBytes = 200 << 20

// VideoPlugin 短视频解析插件。
type VideoPlugin struct {
	ly      *LuoyingClient // luoying 解析 API（抖音/快手）
	imgHost *imgstore.Host
	client  *http.Client // 下载视频直链用（独立于 resty，放宽超时）

	mu sync.Map // link-hash -> 解析开始时间（去重）
}

// NewVideo 创建插件。imgHost 用于视频直链被 QQ 拉取失败时的图床中转。
func NewVideo(imgHost *imgstore.Host) *VideoPlugin {
	return &VideoPlugin{
		ly:      NewLuoyingClient(),
		imgHost: imgHost,
		client:  &http.Client{Timeout: 4 * time.Minute},
	}
}

// Install 注册消息监听。
func (p *VideoPlugin) Install(b *bot.Bot) {
	h := func(c *bot.Ctx) { p.onMessage(c) }
	b.OnGroupMessage(h)
	b.OnPrivateMessage(h)
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
			replyBestEffort(c, "视频解析失败："+truncStr(err.Error(), 150))
		}
	}()
}

// claim 对同一条链接做时间窗去重；返回 false 表示窗口内已处理过。
func (p *VideoPlugin) claim(link string) bool {
	h := fnv.New64a()
	_, _ = h.Write([]byte(link))
	key := h.Sum64()
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
	var (
		res *VideoResult
		err error
	)
	switch {
	case strings.Contains(link, "douyin.com"):
		res, err = p.ly.ParseDouyin(ctx, link)
	case strings.Contains(link, "kuaishou.com"):
		res, err = p.ly.ParseKuaishou(ctx, link)
	default:
		return fmt.Errorf("不支持的链接")
	}
	if err != nil {
		return err
	}

	replyBestEffort(c, res.Summary()) // md 优先，失败回落纯文本（官方保留 md 异步审核权，个别消息可能延迟）

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

// sendVideo 发视频：直链 URL 模式 → 失败则下载经图床中转 → 仍失败发直链文本。
func (p *VideoPlugin) sendVideo(ctx context.Context, c *bot.Ctx, videoURL string) error {
	if _, err := c.ReplyVideo(videoURL); err == nil {
		return nil
	} else {
		log.Printf("[video] 直链发送失败，尝试图床中转: %v", err)
	}
	if p.imgHost != nil && p.imgHost.Configured() {
		data, contentType, err := p.download(ctx, videoURL)
		if err == nil {
			if u, err := p.imgHost.Upload(ctx, data, extOf(contentType, videoURL)); err == nil {
				if _, err := c.ReplyVideo(u); err == nil {
					return nil
				} else {
					log.Printf("[video] 图床中转发送失败: %v", err)
				}
			} else {
				log.Printf("[video] 图床上传失败: %v", err)
			}
		} else {
			log.Printf("[video] 下载视频失败: %v", err)
		}
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
		return nil, "", fmt.Errorf("视频超过 %dMB 中转上限", maxProxyBytes>>20)
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// ---- 解析结果模型 ----

// VideoResult 是各解析源统一后的结果。
type VideoResult struct {
	Title    string
	Author   string
	Like     int64
	Comment  int64
	Share    int64
	VideoURL string
	Images   []string
	Quality  string
	SizeStr  string
	Duration float64 // 秒
}

// Summary 渲染纯文本简介。
func (v *VideoResult) Summary() string {
	var b strings.Builder
	b.WriteString("🎬 ")
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

// ---- 小工具 ----

// replyBestEffort 发文本：优先原生 markdown（本机器人群/私聊实测可发，菜单/点歌在用），
// 失败回落纯文本——官方公域保留 md 异步审核权，个别消息可能延迟或被拒。
func replyBestEffort(c *bot.Ctx, s string) {
	if _, err := c.ReplyMarkdown(s); err != nil {
		_, _ = c.ReplyText(s)
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

// extOf 从 Content-Type 或 URL 推断扩展名（图床中转命名用）。
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
