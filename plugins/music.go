// 点歌插件：接入 itapi.top 的 6 个音乐接口（搜索 + 取播放直链），统一成
// 「搜索列表(≤6条，内嵌指令标签点选) → 发音频」的交互。
//
// 音源与发送方式（itapi 返回的直链格式决定）：
//
//	5sign    5sing 音乐（翻唱社区）  mp3   → 语音（自动转 silk）
//	qq       QQ 音乐                mp3/flac（VIP 曲目 flac）→ mp3 语音，flac 文件
//	kuwo     酷我音乐（老接口）      mp3   → 语音
//	kuwohq   酷我高音质（新接口）    ogg/flac → 文件
//	qishui   汽水音乐（抖音系）      m4a   → 文件
//	netease  网易云音乐             mp3   → 语音（2026-09 上游接口空响应，见 pick 注释）
//
// QQ 官方机器人没有音乐卡片消息，语音只认 silk 且本项目转码仅支持 mp3/wav，
// 故非 mp3 音源走「文件」通道（文件名用「歌名 - 歌手.ext」，QQ 可直接预览播放），
// 全部失败再退直链文本。音频始终经本机中转（下载→转码/分片上传），不依赖
// QQ 拉取 itapi 的直链（多为 http 且带时效签名，QQ URL 模式拉不动）。
//
// 命令：.点歌 歌名（默认音源，.音源 可按聊天切换并落盘）；
// .QQ点歌/.网易点歌/.酷我点歌/.酷我高音质点歌/.汽水点歌/.5sing点歌 指定音源；
// 搜索唯一命中直接播放，多条则歌单（qqbot-cmd-input 内嵌标签点选）；选 序号 取歌。
package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"

	"qqbot/internal/bot"
	"qqbot/internal/utils"
)

// musicHTTP itapi 请求客户端：选歌接口偶发慢（实测 QQ 音乐取链 3~10s），
// 不能用共享的 10s 客户端。
var musicHTTP = &http.Client{Timeout: 45 * time.Second}

// musicMaxList 搜索列表最多展示条数（按钮 3 个/行 × 2 行）。
const musicMaxList = 6

// musicPendTTL 待选列表有效期：过期后 .选 序号 提示重新搜索。
const musicPendTTL = 10 * time.Minute

// musicPendClean 每次访问待选列表时顺手清理超过此时长的过期项，防膨胀。
const musicPendClean = time.Hour

// musicEntry 一条搜索结果。N 是传给 Pick 的取值：多数音源等于显示序号，
// 5sign 是接口返回的 ID 字段。
type musicEntry struct {
	N      int
	Title  string
	Singer string
	Extra  string // 展示用附加信息（时长/付费等）
}

// musicTrack 取链结果：一首可直接播放的歌。
type musicTrack struct {
	Title    string
	Singer   string
	Cover    string
	PageURL  string
	AudioURL string
	Format   string // mp3/flac/ogg/m4a…
	Quality  string // 品质描述（higher/无损…）
	Duration string // 展示用时长（"3:41"）
}

// musicSource 一个音源：Search 出列表，Pick 按序号取歌。
type musicSource struct {
	Key     string   // 稳定标识（.音源 切换用）
	Name    string   // 展示名
	Aliases []string // .音源 切换时可接受的别名
	Cmds    []string // 命令名：第一个为主名，其余为别名（都注册成 <名>点歌）
	Note    string   // .音源 列表里的一行说明

	search func(ctx context.Context, keyword string) ([]musicEntry, error)
	pick   func(ctx context.Context, keyword string, n int) (*musicTrack, error)
}

// Music 点歌插件。
type Music struct {
	apiBase      string
	kuwoBR       int    // kuwohq 音质：1 无损 flac / 2 高品质 / 3 标准（默认）
	neteaseLevel string // 网易音质 standard/higher/exhigh/lossless

	def     string // 全局默认音源（MUSIC_DEFAULT_SOURCE，默认 5sign）
	sources []*musicSource

	mu      sync.Mutex
	prefs   musicPrefs // 每聊天默认音源覆盖，落盘
	prefFn  string
	pendMu  sync.Mutex
	pending map[string]*musicPending // chatID -> 待选列表
}

// musicPrefs 落盘的偏好（data/music.json）。
type musicPrefs struct {
	Chats map[string]string `json:"chats"`
}

// musicPending 一个聊天的待选状态。
type musicPending struct {
	src     *musicSource
	keyword string
	entries []musicEntry
	expires time.Time
}

// NewMusic 创建点歌插件。env：
//
//	MUSIC_API_BASE       itapi 基址（默认 https://itapi.top/api）
//	MUSIC_DEFAULT_SOURCE 默认音源 key（默认 5sign）
//	MUSIC_FILE           偏好落盘路径（默认 ./data/music.json）
//	MUSIC_KUWO_BR        酷我高音质音质档（默认 2）
//	MUSIC_NETEASE_LEVEL  网易音质（默认 exhigh）
func NewMusic() *Music {
	p := &Music{
		apiBase:      utils.EnvOr("MUSIC_API_BASE", "https://itapi.top/api"),
		kuwoBR:       utils.EnvInt("MUSIC_KUWO_BR", 2),
		neteaseLevel: utils.EnvOr("MUSIC_NETEASE_LEVEL", "exhigh"),
		prefFn:       utils.EnvOr("MUSIC_FILE", "./data/music.json"),
		pending:      map[string]*musicPending{},
	}
	p.sources = p.buildSources()
	p.def = p.resolveKey(utils.EnvOr("MUSIC_DEFAULT_SOURCE", "5sign"))
	p.loadPrefs()
	return p
}

// buildSources 注册 6 个音源。请求路径/参数均按 2026-09 实测的 itapi 行为编写
// （文档页的响应示例为空，字段以实测为准）。
func (p *Music) buildSources() []*musicSource {
	return []*musicSource{
		{
			Key: "5sign", Name: "5sing音乐",
			Aliases: []string{"5sing", "5sign音乐", "fivesign"},
			Cmds:    []string{"5sing点歌", "5sign点歌"},
			Note:    "5sing 翻唱社区（原创/翻唱，mp3 发语音）",
			search:  p.search5Sign,
			pick:    p.pick5Sign,
		},
		{
			Key: "qq", Name: "QQ音乐",
			Aliases: []string{"qq音乐", "qqmusic", "tencent"},
			Cmds:    []string{"QQ点歌", "qq点歌", "qq音乐点歌"},
			Note:    "曲库最大（免费曲 mp3 发语音，VIP 曲 flac 发文件）",
			search:  p.searchQQ,
			pick:    p.pickQQ,
		},
		{
			Key: "kuwo", Name: "酷我音乐",
			Aliases: []string{"酷我", "kw"},
			Cmds:    []string{"酷我点歌", "kuwo点歌"},
			Note:    "酷我（mp3 发语音）",
			search:  p.searchKw,
			pick:    p.pickKw,
		},
		{
			Key: "kuwohq", Name: "酷我高音质",
			Aliases: []string{"酷我无损", "无损", "kuwohq音乐"},
			Cmds:    []string{"酷我高音质点歌", "无损点歌", "kuwohq点歌"},
			Note:    "酷我高音质/无损（ogg/flac 发文件）",
			search:  p.searchKuwoHQ,
			pick:    p.pickKuwoHQ,
		},
		{
			Key: "qishui", Name: "汽水音乐",
			Aliases: []string{"汽水", "qsyy"},
			Cmds:    []string{"汽水点歌", "qsyy点歌"},
			Note:    "抖音汽水音乐（m4a 发文件）",
			search:  p.searchQishui,
			pick:    p.pickQishui,
		},
		{
			Key: "netease", Name: "网易云音乐",
			Aliases: []string{"网易", "网易云", "wy", "wangyiyun"},
			Cmds:    []string{"网易点歌", "wy点歌", "网易云点歌"},
			Note:    "网易云（2026-09 上游接口空响应，恢复即用）",
			search:  p.searchNetease,
			pick:    p.pickNetease,
		},
	}
}

// Install 注册命令。全部归「音乐」分类（.帮助 分组展示）；点歌/音源进指令面板；
// 各音源变体与「选」是交互命令，只进 /help。
func (p *Music) Install(b *bot.Bot) {
	b.Command("点歌", p.cmdSong).
		Desc("点歌：.点歌 歌名（默认音源，.音源 可切换；多于一条时 .选 序号 播放）").
		Usage("<歌名>").
		Category(bot.CatMusic)
	for _, s := range p.sources {
		src := s
		b.Command(src.Cmds[0], func(c *bot.Ctx) { p.cmdSongFrom(c, src) }).
			Desc(src.Name + "点歌").
			Usage("<歌名>").
			HideBotMenu().
			Category(bot.CatMusic)
		for _, alias := range src.Cmds[1:] {
			b.Command(alias, func(c *bot.Ctx) { p.cmdSongFrom(c, src) }).Hidden().HideBotMenu().Category(bot.CatMusic)
		}
	}
	b.Command("选", p.cmdPick).
		Alias("选歌").
		Desc("点歌出列表后按序号取歌（点列表标签，或发送 选 2）").
		HideBotMenu().
		Category(bot.CatMusic)
	b.Command("音源", p.cmdSource).
		Desc("查看/切换本聊天的默认点歌音源（.音源 酷我 / .音源 重置）").
		Category(bot.CatMusic)
}

// ---- 命令处理 ----

// cmdSong 默认音源点歌。
func (p *Music) cmdSong(c *bot.Ctx) {
	p.cmdSongFrom(c, p.sourceFor(c))
}

// cmdSongFrom 指定音源点歌。
func (p *Music) cmdSongFrom(c *bot.Ctx, src *musicSource) {
	keyword := strings.TrimSpace(c.Args.Join())
	if keyword == "" {
		replyPlain(c, "用法："+c.Args.Name+" 歌名\n当前音源："+p.sourceFor(c).Name+"（.音源 可切换）")
		return
	}
	p.goMusic(c, src, func(ctx context.Context) error {
		return p.searchAndShow(ctx, c, src, keyword)
	})
}

// cmdPick 选 序号：从待选列表取歌（列表标签点击填入的就是裸「选 N」）。
func (p *Music) cmdPick(c *bot.Ctx) {
	n, err := strconv.Atoi(strings.TrimSpace(c.Args.Get(0)))
	if err != nil {
		return // 不是序号（如口癖「选一下」），安静忽略
	}
	pend := p.takePending(c)
	if pend == nil {
		// 无待选（列表过期/没搜过）也安静返回：标签误点/口癖不刷屏；
		// 命令已消费该消息，不会漏给 AI 兜底。
		return
	}
	if n < 1 || n > len(pend.entries) {
		replyPlain(c, fmt.Sprintf("序号超出范围（1~%d）", len(pend.entries)))
		return
	}
	e := pend.entries[n-1]
	p.goMusic(c, pend.src, func(ctx context.Context) error {
		return p.play(ctx, c, pend.src, pend.keyword, e)
	})
}

// cmdSource .音源：查看/切换本聊天默认音源。
func (p *Music) cmdSource(c *bot.Ctx) {
	arg := strings.TrimSpace(c.Args.Join())
	if arg == "" {
		c.ReplyTaggedOrPlain(p.sourceHelp(c, true), p.sourceHelp(c, false))
		return
	}
	if arg == "重置" || strings.EqualFold(arg, "reset") {
		p.setChatSource(c, "")
		replyPlain(c, "已恢复默认音源："+p.sourceFor(c).Name)
		return
	}
	key := p.resolveKey(arg)
	if key == "" {
		replyPlain(c, "未知音源「"+arg+"」\n"+p.sourceHelp(c, false))
		return
	}
	p.setChatSource(c, key)
	replyPlain(c, "本聊天默认音源已切换为："+p.sourceByKey(key).Name+"（重启保留，.音源 重置 恢复默认）")
}

// sourceHelp .音源 的说明文本。tagged=true 时音源名/直接点歌/重置都是内嵌指令
// 标签（点击填入输入框）；false 为纯文本（频道与 markdown 降级用）。
func (p *Music) sourceHelp(c *bot.Ctx, tagged bool) string {
	var b strings.Builder
	cur := p.sourceFor(c)
	b.WriteString("当前音源：" + cur.Name + "（" + cur.Key + "）\n可用音源：\n")
	for _, s := range p.sources {
		if tagged {
			b.WriteString("• " + bot.CmdInputTag("音源 "+s.Key, s.Name) + " — " + s.Note +
				"  " + bot.CmdInputTag(s.Cmds[0]+" ", "直接点歌→") + "\n")
		} else {
			b.WriteString("• " + s.Key + " / " + s.Name + " — " + s.Note + "\n")
		}
	}
	if tagged {
		b.WriteString("点音源名切换默认（对本聊天生效，重启保留）｜" + bot.CmdInputTag("音源 重置", "重置为默认"))
	} else {
		b.WriteString("切换：.音源 <名称>（对本聊天生效，重启保留）｜重置：.音源 重置")
	}
	return b.String()
}

// ---- 搜索与播放流程 ----

// searchAndShow 搜索并展示：唯一命中直接播，多条则出歌单（内嵌指令标签点选）。
func (p *Music) searchAndShow(ctx context.Context, c *bot.Ctx, src *musicSource, keyword string) error {
	entries, err := src.search(ctx, keyword)
	if err != nil {
		return fmt.Errorf("%s: %w", src.Name, err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("%s 没搜到「%s」相关的歌", src.Name, keyword)
	}
	if len(entries) == 1 {
		return p.play(ctx, c, src, keyword, entries[0])
	}
	p.storePending(c, &musicPending{src: src, keyword: keyword, entries: entries, expires: time.Now().Add(musicPendTTL)})

	// 群聊/私聊：歌单每行用官方内嵌指令标签（点击把「选 N」插入输入框，用户补发送）；
	// 频道与 markdown 被拒发时降级纯文本（见 ReplyTaggedOrPlain）。
	c.ReplyTaggedOrPlain(musicListText(src, keyword, entries, true), musicListText(src, keyword, entries, false))
	return nil
}

// musicListText 生成歌单文本。tagged=true 时每首歌用内嵌指令标签包裹（show 是
// 用户看到的整行文字）；tagged=false 为纯文本（频道与 markdown 降级用——标签在
// 纯文本里就是乱码）。
func musicListText(src *musicSource, keyword string, entries []musicEntry, tagged bool) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("🎵 「%s」%s 搜索到 %d 条：\n", keyword, src.Name, len(entries)))
	for i, e := range entries {
		line := fmt.Sprintf("%d. %s - %s", i+1, e.Title, e.Singer)
		if e.Extra != "" {
			line += " [" + e.Extra + "]"
		}
		if tagged {
			b.WriteString(bot.CmdInputTag(fmt.Sprintf("选 %d", i+1), line) + "\n")
		} else {
			b.WriteString(line + "\n")
		}
	}
	if tagged {
		b.WriteString("点歌名标签选歌（或发送「选 序号」）")
	} else {
		b.WriteString("发送「选 序号」选歌（如 选 1），列表 10 分钟内有效")
	}
	return b.String()
}

// musicIntro 生成歌曲介绍文本：md 版用 ![图片](url) 内嵌封面（介绍+封面一条消息，
// QQ 会转存该图）；末尾带「歌曲发送中」提示（音频转码/上传要等一会儿）；
// plain 是 markdown 被拒发时的降级文本（无封面，不另发图控制消息数）。
func musicIntro(src *musicSource, t *musicTrack) (md, plain string) {
	meta := []string{src.Name}
	if t.Duration != "" {
		meta = append(meta, t.Duration)
	}
	if f := musicFormatOf(t); f != "" {
		meta = append(meta, f)
	}
	if t.Quality != "" {
		meta = append(meta, t.Quality)
	}
	title := oneLine(t.Title, 60) + " - " + oneLine(t.Singer, 60)
	head := "🎵 " + title + "\n" + strings.Join(meta, " ｜ ")
	link := ""
	if t.PageURL != "" {
		link = "\n🔗 " + t.PageURL
	}
	body := head + link + "\n⏳ 歌曲发送中…"
	if t.Cover != "" {
		md = "![封面](" + t.Cover + ")\n" + body
	} else {
		md = body
	}
	return md, body
}

// play 取链并发送一首歌：介绍（markdown 内嵌封面，一条）→ 音频（语音优先，文件兜底）。
func (p *Music) play(ctx context.Context, c *bot.Ctx, src *musicSource, keyword string, e musicEntry) error {
	track, err := src.pick(ctx, keyword, e.N)
	if err != nil {
		return fmt.Errorf("%s: %w", src.Name, err)
	}
	if track.AudioURL == "" {
		return fmt.Errorf("「%s - %s」拿不到播放地址（可能版权受限），换一首或换个音源试试", track.Title, track.Singer)
	}

	md, plain := musicIntro(src, track)
	if _, err := c.ReplyMarkdown(md); err != nil {
		log.Printf("[music] 介绍 markdown 发送失败，降级纯文本: %v", err)
		replyPlain(c, plain)
	}
	return p.sendAudio(ctx, c, track)
}

// sendAudio 发音频：mp3/wav 走语音（SendVoice 自动转 silk，需配图床）；
// 其余或语音失败走文件（命名「歌名 - 歌手.ext」）；再失败发直链文本。
func (p *Music) sendAudio(ctx context.Context, c *bot.Ctx, track *musicTrack) error {
	ext := musicFormatOf(track)
	if ext == "mp3" || ext == "wav" {
		if _, err := c.ReplyVoice(track.AudioURL); err == nil {
			return nil
		} else {
			log.Printf("[music] 语音发送失败，改发文件: %v", err)
		}
	}
	name := musicFileName(track.Title, track.Singer, ext)
	if _, err := c.ReplyFileNamed(track.AudioURL, name); err == nil {
		return nil
	} else {
		log.Printf("[music] 文件发送失败: %v", err)
	}
	_, err := c.ReplyText("音频发送失败，直链（短期有效）：" + track.AudioURL)
	return err
}

// goMusic 异步执行音乐流程（搜索/取链可到几十秒，语音转码更久），
// 统一 panic 兜底与超时；被动回复锚点 5 分钟内完成。
func (p *Music) goMusic(c *bot.Ctx, src *musicSource, fn func(ctx context.Context) error) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[music] panic: %v\n%s", r, debug.Stack())
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		if err := fn(ctx); err != nil {
			log.Printf("[music] %s 失败: %v", src.Name, err)
			replyPlain(c, "点歌失败："+truncStr(err.Error(), 150))
		}
	}()
}

// ---- 待选列表（内存，按聊天） ----

// musicChatID 聊天维度的 key：群/私聊/频道各自独立。
func musicChatID(c *bot.Ctx) string {
	switch {
	case c.IsGroup():
		return "group:" + c.GroupID()
	case c.IsChannel():
		return "channel:" + c.ChannelID()
	default:
		return "c2c:" + c.UserID()
	}
}

// takePending 取本聊天的待选列表；过期或不存在返回 nil（顺手清理）。
func (p *Music) takePending(c *bot.Ctx) *musicPending {
	id := musicChatID(c)
	now := time.Now()
	p.pendMu.Lock()
	defer p.pendMu.Unlock()
	for k, v := range p.pending {
		if now.Sub(v.expires) > musicPendClean {
			delete(p.pending, k)
		}
	}
	v := p.pending[id]
	if v == nil || now.After(v.expires) {
		return nil
	}
	return v
}

// storePending 覆盖本聊天的待选列表。
func (p *Music) storePending(c *bot.Ctx, v *musicPending) {
	p.pendMu.Lock()
	p.pending[musicChatID(c)] = v
	p.pendMu.Unlock()
}

// ---- 默认音源（按聊天，落盘） ----

// sourceFor 本聊天当前生效的音源。
func (p *Music) sourceFor(c *bot.Ctx) *musicSource {
	p.mu.Lock()
	key := p.prefs.Chats[musicChatID(c)]
	p.mu.Unlock()
	if key == "" {
		key = p.def
	}
	return p.sourceByKey(key)
}

// setChatSource 设置/清除本聊天的音源覆盖并落盘；空串为清除。
func (p *Music) setChatSource(c *bot.Ctx, key string) {
	p.mu.Lock()
	if p.prefs.Chats == nil {
		p.prefs.Chats = map[string]string{}
	}
	if key == "" {
		delete(p.prefs.Chats, musicChatID(c))
	} else {
		p.prefs.Chats[musicChatID(c)] = key
	}
	prefs := p.prefs
	p.mu.Unlock()

	data, err := json.MarshalIndent(prefs, "", "  ")
	if err != nil {
		log.Printf("[music] 偏好序列化失败: %v", err)
		return
	}
	if err := os.WriteFile(p.prefFn, data, 0o644); err != nil {
		log.Printf("[music] 偏好落盘失败: %v", err)
	}
}

// sourceByKey 按 key 找音源，找不到回落默认。
func (p *Music) sourceByKey(key string) *musicSource {
	for _, s := range p.sources {
		if s.Key == key {
			return s
		}
	}
	for _, s := range p.sources {
		if s.Key == p.def {
			return s
		}
	}
	return p.sources[0]
}

// resolveKey 把用户输入（key/别名/中文名）解析成音源 key；无效返回空串。
func (p *Music) resolveKey(in string) string {
	in = strings.ToLower(strings.TrimSpace(in))
	if in == "" {
		return ""
	}
	for _, s := range p.sources {
		if strings.EqualFold(s.Key, in) || strings.EqualFold(s.Name, in) {
			return s.Key
		}
		for _, a := range s.Aliases {
			if strings.EqualFold(a, in) {
				return s.Key
			}
		}
	}
	return ""
}

// loadPrefs 启动时读偏好文件（无文件/坏文件从空开始，不阻断启动）。
func (p *Music) loadPrefs() {
	data, err := os.ReadFile(p.prefFn)
	if err != nil {
		return
	}
	var prefs musicPrefs
	if err := json.Unmarshal(data, &prefs); err != nil {
		log.Printf("[music] 偏好文件损坏（忽略）: %v", err)
		return
	}
	p.prefs = prefs
}

// ---- itapi 音源实现 ----

// musicGet GET 一个 itapi 接口，返回响应体（HTTP>=400 报错）。
func musicGet(ctx context.Context, rawURL string, params url.Values) ([]byte, error) {
	u := rawURL + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "qqbot/1.0")
	resp, err := musicHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncStr(string(body), 120))
	}
	return body, nil
}

// search5Sign 5sing：data 为数组，ID 是选歌取值。
func (p *Music) search5Sign(ctx context.Context, keyword string) ([]musicEntry, error) {
	body, err := musicGet(ctx, p.apiBase+"/5sign", url.Values{"msg": {keyword}, "type": {"json"}})
	if err != nil {
		return nil, err
	}
	arr := gjsonParse(body).Get("data").Array()
	var out []musicEntry
	for _, it := range arr {
		if it.Get("title").String() == "" {
			continue
		}
		e := musicEntry{
			N:      int(it.Get("ID").Int()),
			Title:  it.Get("title").String(),
			Singer: it.Get("singer").String(),
		}
		if t := it.Get("songtype").String(); t != "" && t != "原唱" {
			e.Extra = t
		}
		out = append(out, e)
		if len(out) >= musicMaxList {
			break
		}
	}
	return out, nil
}

// pick5Sign 5sing 取链：data.url 为 mp3 直链。
func (p *Music) pick5Sign(ctx context.Context, keyword string, n int) (*musicTrack, error) {
	body, err := musicGet(ctx, p.apiBase+"/5sign", url.Values{"msg": {keyword}, "n": {strconv.Itoa(n)}, "type": {"json"}})
	if err != nil {
		return nil, err
	}
	d := gjsonParse(body).Get("data")
	if d.Get("url").String() == "" {
		return nil, fmt.Errorf("拿不到音频（接口返回: %s）", truncStr(d.Get("title").String()+d.Raw, 120))
	}
	return &musicTrack{
		Title: d.Get("title").String(), Singer: d.Get("singer").String(),
		Cover: d.Get("cover").String(), PageURL: d.Get("link").String(),
		AudioURL: d.Get("url").String(), Format: "mp3",
	}, nil
}

// searchQQ QQ 音乐：data.list 数组，含时长与付费标记。
func (p *Music) searchQQ(ctx context.Context, keyword string) ([]musicEntry, error) {
	body, err := musicGet(ctx, p.apiBase+"/qqmisic", url.Values{"ss": {keyword}, "format": {"json"}})
	if err != nil {
		return nil, err
	}
	var out []musicEntry
	for _, it := range gjsonParse(body).Get("data.list").Array() {
		if it.Get("name").String() == "" {
			continue
		}
		e := musicEntry{
			N:      len(out) + 1,
			Title:  it.Get("name").String(),
			Singer: it.Get("artist").String(),
		}
		var meta []string
		if d := it.Get("duration").String(); d != "" {
			meta = append(meta, d)
		}
		if pay := it.Get("paytext").String(); pay != "" && pay != "免费" {
			meta = append(meta, pay)
		}
		e.Extra = strings.Join(meta, "·")
		out = append(out, e)
		if len(out) >= musicMaxList {
			break
		}
	}
	return out, nil
}

// pickQQ QQ 音乐取链：免费曲回 mp3、VIP 曲回 flac（play_url 后缀即格式）。
func (p *Music) pickQQ(ctx context.Context, keyword string, n int) (*musicTrack, error) {
	body, err := musicGet(ctx, p.apiBase+"/qqmisic", url.Values{"ss": {keyword}, "n": {strconv.Itoa(n)}, "format": {"json"}})
	if err != nil {
		return nil, err
	}
	d := gjsonParse(body).Get("data")
	if d.Get("play_url").String() == "" {
		return nil, fmt.Errorf("拿不到播放地址（可能版权受限）: %s", truncStr(d.Get("name").String(), 60))
	}
	return &musicTrack{
		Title: d.Get("name").String(), Singer: d.Get("singer").String(),
		Cover:    d.Get("cover_url").String(),
		AudioURL: d.Get("play_url").String(),
		Format:   d.Get("extName").String(),
		Quality:  d.Get("quality").String(), Duration: d.Get("duration").String(),
	}, nil
}

// musicKwLineRE 酷我（老接口）搜索列表行：`1：天下——张杰`（分隔符为全角双破折号）。
var musicKwLineRE = regexp.MustCompile(`(?m)^\s*(\d{1,2})\s*[：:.、]\s*(.+?)\s*——\s*(.+?)\s*$`)

// searchKw 酷我老接口：响应是纯文本列表（type=json 对搜索步不生效），正则解析。
func (p *Music) searchKw(ctx context.Context, keyword string) ([]musicEntry, error) {
	body, err := musicGet(ctx, p.apiBase+"/kw", url.Values{"msg": {keyword}})
	if err != nil {
		return nil, err
	}
	var out []musicEntry
	for _, m := range musicKwLineRE.FindAllStringSubmatch(string(body), -1) {
		n, _ := strconv.Atoi(m[1])
		out = append(out, musicEntry{N: n, Title: m[2], Singer: m[3]})
		if len(out) >= musicMaxList {
			break
		}
	}
	return out, nil
}

// pickKw 酷我老接口取链：type=json 时响应是 `json:` 前缀 + QQ 音乐卡片 JSON，
// 从 meta.music 里取 mp3 直链与封面。
func (p *Music) pickKw(ctx context.Context, keyword string, n int) (*musicTrack, error) {
	body, err := musicGet(ctx, p.apiBase+"/kw", url.Values{"msg": {keyword}, "b": {strconv.Itoa(n)}, "type": {"json"}})
	if err != nil {
		return nil, err
	}
	s := strings.TrimPrefix(strings.TrimSpace(string(body)), "json:")
	m := gjsonParse([]byte(s)).Get("meta.music")
	if m.Get("musicUrl").String() == "" {
		return nil, fmt.Errorf("拿不到音频直链（响应: %s）", truncStr(s, 120))
	}
	return &musicTrack{
		Title: m.Get("title").String(), Singer: m.Get("desc").String(),
		Cover: m.Get("preview").String(), AudioURL: m.Get("musicUrl").String(),
		Format: "mp3",
	}, nil
}

// searchKuwoHQ 酷我新接口：limit 控制条数，data 为结构化数组。
func (p *Music) searchKuwoHQ(ctx context.Context, keyword string) ([]musicEntry, error) {
	body, err := musicGet(ctx, p.apiBase+"/Kuwo", url.Values{
		"msg": {keyword}, "page": {"1"}, "limit": {strconv.Itoa(musicMaxList)},
	})
	if err != nil {
		return nil, err
	}
	var out []musicEntry
	for _, it := range gjsonParse(body).Get("data").Array() {
		if it.Get("song").String() == "" {
			continue
		}
		e := musicEntry{
			N:      len(out) + 1,
			Title:  it.Get("song").String(),
			Singer: it.Get("singer").String(),
		}
		if t := musicFmtDur(it.Get("time").String()); t != "" {
			e.Extra = t
		}
		out = append(out, e)
	}
	return out, nil
}

// pickKuwoHQ 酷我新接口取链：br 音质档（1 无损/2 高品质/3 标准），直链 ogg/flac。
func (p *Music) pickKuwoHQ(ctx context.Context, keyword string, n int) (*musicTrack, error) {
	body, err := musicGet(ctx, p.apiBase+"/Kuwo", url.Values{
		"msg": {keyword}, "n": {strconv.Itoa(n)}, "br": {strconv.Itoa(p.kuwoBR)},
	})
	if err != nil {
		return nil, err
	}
	d := gjsonParse(body).Get("data")
	if d.Get("url").String() == "" {
		return nil, fmt.Errorf("拿不到音频直链（响应: %s）", truncStr(d.Raw, 120))
	}
	q := d.Get("level").String()
	switch q {
	case "ff":
		q = "无损"
	case "h":
		q = "高品质"
	case "p", "":
		q = ""
	}
	return &musicTrack{
		Title: d.Get("song").String(), Singer: d.Get("singer").String(),
		Cover:    d.Get("picture").String(),
		AudioURL: d.Get("url").String(),
		Format:   d.Get("format").String(), Quality: q,
		Duration: musicFmtDur(d.Get("time").String()),
	}, nil
}

// searchQishui 汽水：data 数组，n 字段即序号。
func (p *Music) searchQishui(ctx context.Context, keyword string) ([]musicEntry, error) {
	body, err := musicGet(ctx, p.apiBase+"/Qsyy", url.Values{"msg": {keyword}, "type": {"json"}})
	if err != nil {
		return nil, err
	}
	var out []musicEntry
	for _, it := range gjsonParse(body).Get("data").Array() {
		if it.Get("title").String() == "" {
			continue
		}
		out = append(out, musicEntry{
			N: int(it.Get("n").Int()), Title: it.Get("title").String(), Singer: it.Get("singer").String(),
		})
		if len(out) >= musicMaxList {
			break
		}
	}
	return out, nil
}

// pickQishui 汽水取链：响应扁平（无 data 包裹），music 为 m4a 直链；
// 付费曲目 music 为空，msg 带原因。
func (p *Music) pickQishui(ctx context.Context, keyword string, n int) (*musicTrack, error) {
	body, err := musicGet(ctx, p.apiBase+"/Qsyy", url.Values{"msg": {keyword}, "n": {strconv.Itoa(n)}, "type": {"json"}})
	if err != nil {
		return nil, err
	}
	r := gjsonParse(body)
	if r.Get("music").String() == "" {
		return nil, fmt.Errorf("%s", oneLine(r.Get("msg").String(), 80))
	}
	return &musicTrack{
		Title: r.Get("title").String(), Singer: r.Get("singer").String(),
		Cover: r.Get("cover").String(), PageURL: r.Get("link").String(),
		AudioURL: r.Get("music").String(), Format: "m4a",
	}, nil
}

// searchNetease 网易：文档两步协议（ss 搜 id → id 取链）。字段名文档未给出，
// 用常见命名兜底解析；2026-09 实测上游返回空 body，报「无响应」引导换音源。
func (p *Music) searchNetease(ctx context.Context, keyword string) ([]musicEntry, error) {
	body, err := musicGet(ctx, p.apiBase+"/wangyiyun", url.Values{"ss": {keyword}, "n": {strconv.Itoa(musicMaxList)}})
	if err != nil {
		return nil, err
	}
	j := gjsonParse(body)
	if len(j.Get("data").Array()) == 0 {
		return nil, fmt.Errorf("接口无响应（上游疑似维护中），可先用 .酷我点歌 / .QQ点歌")
	}
	var out []musicEntry
	for _, it := range j.Get("data").Array() {
		title := firstGjson(it, "name", "title", "song")
		if title.String() == "" {
			continue
		}
		out = append(out, musicEntry{
			N: len(out) + 1, Title: title.String(),
			Singer: firstGjson(it, "artist", "singer", "artists.#.name|@join").String(),
		})
	}
	return out, nil
}

// pickNetease 网易取链：重新搜索定位第 n 条的 id，再按 id+level 取 mp3 直链。
func (p *Music) pickNetease(ctx context.Context, keyword string, n int) (*musicTrack, error) {
	body, err := musicGet(ctx, p.apiBase+"/wangyiyun", url.Values{"ss": {keyword}, "n": {strconv.Itoa(n)}})
	if err != nil {
		return nil, err
	}
	j := gjsonParse(body)
	arr := j.Get("data").Array()
	if len(arr) < n {
		return nil, fmt.Errorf("接口无响应或结果不足（上游疑似维护中），可先用 .酷我点歌 / .QQ点歌")
	}
	it := arr[n-1]
	id := it.Get("id").Int()
	if id == 0 {
		return nil, fmt.Errorf("搜索结果缺歌曲 id（字段结构变化）: %s", truncStr(it.Raw, 120))
	}
	pickBody, err := musicGet(ctx, p.apiBase+"/wangyiyun", url.Values{
		"id": {strconv.FormatInt(id, 10)}, "level": {p.neteaseLevel},
	})
	if err != nil {
		return nil, err
	}
	track := gjsonParse(pickBody).Get("data.0")
	if track.Get("url").String() == "" {
		return nil, fmt.Errorf("拿不到播放地址（可能版权受限）")
	}
	return &musicTrack{
		Title:    firstGjson(it, "name", "title", "song").String(),
		Singer:   firstGjson(it, "artist", "singer", "artists.#.name|@join").String(),
		AudioURL: track.Get("url").String(), Format: "mp3",
	}, nil
}

// ---- 小工具 ----

// gjsonParse 容错解析（itapi 偶发 HTML 错误页；解析失败得到空 Result，
// 下游取不到字段自然报「没搜到/拿不到」，不至于 panic）。
func gjsonParse(b []byte) gjson.Result {
	return gjson.ParseBytes(b)
}

// firstGjson 依次返回第一个存在的字段（跨源字段命名不统一时兜底）。
func firstGjson(item gjson.Result, paths ...string) gjson.Result {
	for _, p := range paths {
		if v := item.Get(p); v.Exists() {
			return v
		}
	}
	return gjson.Result{}
}

// musicFormatOf 取曲目音频格式：优先接口给的格式，缺省按直链扩展名猜。
func musicFormatOf(t *musicTrack) string {
	if t.Format != "" {
		return strings.ToLower(t.Format)
	}
	if i := strings.LastIndex(t.AudioURL, "."); i >= 0 {
		ext := t.AudioURL[i+1:]
		if j := strings.IndexAny(ext, "?#"); j >= 0 {
			ext = ext[:j]
		}
		if ext != "" && len(ext) <= 5 {
			return strings.ToLower(ext)
		}
	}
	return ""
}

// musicFileName 生成「歌名 - 歌手.ext」的合法文件名（去路径非法字符、折叠空白，限长）。
func musicFileName(title, singer, ext string) string {
	name := musicTrunc(strings.TrimSpace(title), 40) + " - " + musicTrunc(strings.TrimSpace(singer), 30)
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`\/:*?"<>|`+"\n\r\t", r) {
			return ' '
		}
		return r
	}, name)
	for strings.Contains(name, "  ") {
		name = strings.ReplaceAll(name, "  ", " ")
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "-" {
		name = "歌曲"
	}
	if ext != "" {
		name += "." + ext
	}
	return name
}

// musicTrunc 按字符截断（超长补省略号）。
func musicTrunc(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// musicFmtDur 时长归一化：秒数（"221"）转 "3:41"，已是分秒格式原样返回。
func musicFmtDur(in string) string {
	in = strings.TrimSpace(in)
	if in == "" || strings.ContainsAny(in, ":.") {
		return in
	}
	if sec, err := strconv.Atoi(in); err == nil && sec >= 0 {
		return fmt.Sprintf("%d:%02d", sec/60, sec%60)
	}
	return in
}
