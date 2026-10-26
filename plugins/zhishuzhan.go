// zhishuzhan.com 视频解析源（抖音/快手，2026-10 接入），以 Parser 身份接入解析链
// （plugins/videoparser.go），与 luoying 互为备份。
//
//	POST {base}/v2/parse/index    body {"PageUrl":"<分享短链>"}    Content-Type: application/json
//
// 凭据是浏览器会话 cookie（tokens + user_id）——会过期，失效特征是解析返回
// Code=201 且 Data 为「请退出app重新打开尝试」（区别于「请检查视频是否审核通过或私密」
// = 链接无效，不触发续期）。凭据获取与恢复统一走微信扫码自动续期（ensureRelogin）：
// 无凭据起跑或失效时把二维码发回触发解析的聊天，轮询扫码结果，成功后更新凭据并落盘
// （sessionFile，优先于 env）。服务端按浏览器会话校验，必带 cookie/origin/桌面 UA 三个头。
// 响应外层 {Code, Msg, Data}：Code=200 成功（Data 为对象）；失败时 Data 是原因字符串。
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// zszDefaultBase / zszUserAgent / zszDefaultSession 服务端按浏览器会话校验，
// UA 必须是桌面形态。
const (
	zszDefaultBase    = "https://zhishuzhan.com"
	zszUserAgent      = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"
	zszDefaultSession = "data/zhishuzhan-session.json"
)

// ReloginNotifier 扫码续期消息的推送通道（由 VideoPlugin.Install 注入 QQ 实现：
// 发回触发解析的那个聊天，QQ 胶水不进源文件，源逻辑才能脱离 QQ 独立测试）。
type ReloginNotifier interface {
	SendText(ctx context.Context, text string)
	SendImage(ctx context.Context, imageURL string)
}

// ZhishuzhanClient 是 zhishuzhan.com 解析客户端。凭据可被扫码续期在线更换，
// Tokens/UserID 与续期状态都在 mu 保护下读写；Base/HTTP/各时长参数构造后不变。
type ZhishuzhanClient struct {
	Base   string // API 基址
	Tokens string // 会话 cookie tokens（初始来自 env 或落盘 session）
	UserID string // 会话 cookie user_id
	HTTP   *http.Client

	maxQuality int // 清晰度上限（0=最高档）；ZHISHUZHAN_QUALITY=highest|数字

	mu                 sync.RWMutex    // 护 Tokens/UserID 与续期状态
	notifier           ReloginNotifier // 扫码通知通道，Install 时注入
	onCredsRefreshed   func()          // 凭据就绪回调（扫码成功后触发，VideoPlugin 重试失败链接用）
	sessionFile        string          // 凭据落盘路径，默认 data/zhishuzhan-session.json
	autoRelogin        bool            // ZHISHUZHAN_AUTO_RELOGIN=0 关闭，默认开
	pollInterval       time.Duration   // 扫码轮询间隔，默认 3s
	pollTimeout        time.Duration   // 扫码等待上限，默认 5min（二维码有效期约 5min）
	reloginMinInterval time.Duration   // 两次续期发起的最小间隔（频控），默认 30min
	relogining         bool            // 有续期流程在跑（防并发）
	lastRelogin        time.Time       // 上次发起时刻（频控）
}

// Name 实现 Parser 接口。
func (c *ZhishuzhanClient) Name() string { return "zhishuzhan" }

// SetNotifier 注入扫码通知通道（VideoPlugin.Install 时调用）。
func (c *ZhishuzhanClient) SetNotifier(n ReloginNotifier) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notifier = n
}

// SetOnCredsRefreshed 注入凭据就绪回调：扫码成功、凭据已生效后异步触发一次。
func (c *ZhishuzhanClient) SetOnCredsRefreshed(f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onCredsRefreshed = f
}

// creds 读当前凭据（扫码成功后会被 relogin 在锁内换掉）。
func (c *ZhishuzhanClient) creds() (tokens, uid string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Tokens, c.UserID
}

// Parse 按链接域名分流平台，实现 Parser 接口；不认识的域名返回
// ErrUnsupportedPlatform，把机会让给链上的下一个源。
func (c *ZhishuzhanClient) Parse(ctx context.Context, link string) (*VideoResult, error) {
	switch {
	case strings.Contains(link, "douyin.com"), strings.Contains(link, "kuaishou.com"):
		return c.parse(ctx, link)
	default:
		return nil, ErrUnsupportedPlatform
	}
}

// NewZhishuzhanClientFromEnv 装配 zhishuzhan 客户端（默认启用，永不返回 nil——
// 想彻底下线该源用 VIDEO_PARSERS）。凭据优先级：落盘 session（扫码续期写入的）
// > env 种子 > 空。空凭据即可工作：首个解析请求会撞「请退出」失效特征，自动
// 在触发解析的聊天里弹二维码，扫码即完成凭据引导，无需任何人工配置。
func NewZhishuzhanClientFromEnv() *ZhishuzhanClient {
	c := &ZhishuzhanClient{
		Base:               zszDefaultBase,
		HTTP:               &http.Client{Timeout: 60 * time.Second},
		autoRelogin:        os.Getenv("ZHISHUZHAN_AUTO_RELOGIN") != "0",
		pollInterval:       3 * time.Second,
		pollTimeout:        5 * time.Minute,
		reloginMinInterval: 30 * time.Minute,
	}
	if f := os.Getenv("ZHISHUZHAN_SESSION_FILE"); f != "" {
		c.sessionFile = f
	} else {
		c.sessionFile = zszDefaultSession
	}
	// 落盘 session 优先（扫码续期写的新凭据），读不到/缺字段/非法则回退 env 种子；
	// 都没有就空凭据起跑，靠扫码登录引导。
	if s, err := loadZszSession(c.sessionFile); err == nil && s.Tokens != "" && s.UserID != "" {
		c.Tokens, c.UserID = s.Tokens, s.UserID
	} else {
		c.Tokens = os.Getenv("ZHISHUZHAN_TOKENS")
		c.UserID = os.Getenv("ZHISHUZHAN_USER_ID")
	}
	// 清晰度：highest/不配 = 最高档；数字（如 720）= 不超过它的最高档（流量友好）。
	switch q := strings.TrimSpace(os.Getenv("ZHISHUZHAN_QUALITY")); {
	case q == "", q == "highest":
	default:
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			c.maxQuality = n
		}
	}
	return c
}

// zszSession 凭据落盘格式（0600，含更新时间便于排查 cookie 何时换的）。
type zszSession struct {
	Tokens    string `json:"tokens"`
	UserID    string `json:"user_id"`
	UpdatedAt string `json:"updated_at"`
}

func loadZszSession(path string) (*zszSession, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s zszSession
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// saveSession 把新凭据落盘（MkdirAll 父目录；0600 防泄露）。
func (c *ZhishuzhanClient) saveSession(tokens, uid string) error {
	if dir := filepath.Dir(c.sessionFile); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	b, err := json.Marshal(zszSession{Tokens: tokens, UserID: uid, UpdatedAt: time.Now().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	return os.WriteFile(c.sessionFile, b, 0o600)
}

// zszResp 统一外层。Data 成功时是对象、失败时是原因字符串，先收 RawMessage 再分流。
type zszResp struct {
	Code int             `json:"Code"`
	Msg  string          `json:"Msg"`
	Data json.RawMessage `json:"Data"`
}

// zszData 成功时的 Data 对象（只接关键字段；lives/audio/coverUrls/encodeUrl/header 不接）。
// author 实测恒 null，不接。
type zszData struct {
	Title     string   `json:"title"`
	IsVideo   bool     `json:"isVideo"`
	VideoURLs string   `json:"videoUrls"`
	Pics      []string `json:"pics"`
	Formats   []struct {
		QualityNote string `json:"qualityNote"`
		Vext        string `json:"vext"`
		Video       string `json:"video"`
		Audio       string `json:"audio"`
		// separate 不接：实测线上给的是数字 0/1（不是 bool），按 bool 反序列化会整个
		// 解析失败；且本层未用到——未知字段 json 自动忽略，格式漂移也炸不到。
	} `json:"Formats"`
}

// parse 只调一次接口（快速失败）；重试/故障转移归 ParserChain，不在源内重试。
// 检测到失效特征时照常把错误返回给链（该次失败照计），续期流程异步另行发起。
func (c *ZhishuzhanClient) parse(ctx context.Context, link string) (*VideoResult, error) {
	tokens, uid := c.creds()
	body, err := json.Marshal(struct {
		PageUrl string `json:"PageUrl"`
	}{PageUrl: link})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/v2/parse/index", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("cookie", "tokens="+tokens+"; user_id="+uid)
	req.Header.Set("origin", zszDefaultBase)
	req.Header.Set("user-agent", zszUserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求解析接口失败: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var r zszResp
	if json.Unmarshal(b, &r) != nil {
		return nil, fmt.Errorf("解析接口响应异常（HTTP %d）: %s", resp.StatusCode, truncStr(string(b), 120))
	}
	if r.Code != 200 {
		// 失败时 Data 是原因字符串（cookie 失效/链接无效的提示），解不出就退回 Msg。
		var reason string
		if len(r.Data) > 0 {
			_ = json.Unmarshal(r.Data, &reason)
		}
		if reason == "" {
			reason = r.Msg
		}
		if strings.Contains(reason, "请退出") { // 会话 cookie 失效特征（链接无效的提示不含「请退出」）
			c.ensureRelogin()
		}
		return nil, fmt.Errorf("解析失败: %s", reason)
	}
	var d zszData
	if err := json.Unmarshal(r.Data, &d); err != nil {
		return nil, fmt.Errorf("解析结果格式异常: %w", err)
	}
	res := &VideoResult{Title: d.Title}
	if d.IsVideo {
		// 从 Formats 按清晰度选档（默认最高，ZHISHUZHAN_QUALITY 可设上限），
		// 显示的 Quality 即选中那档；没有可用档位（缺 Formats/全是推广）回落
		// 顶层 videoUrls（站点默认档，实测约 540P）。
		if u, note := d.pickFormat(c.maxQuality); u != "" {
			res.VideoURL, res.Quality = u, note
		} else {
			res.VideoURL = d.VideoURLs
		}
		if res.VideoURL != "" {
			return res, nil
		}
	}
	if len(d.Pics) > 0 { // 图集：VideoURL 必须为空（同 luoying 图集语义）
		res.Images = d.Pics
		return res, nil
	}
	return nil, fmt.Errorf("解析结果里没有视频/图片")
}

// pickFormat 从 Formats 里选清晰度档位：maxHeight=0 取最高；>0 取不超过它的
// 最高档（全都超过时取最低档兜底，流量友好）。daf0.cn 推广条目与读不出数字的
// 档位跳过（「最高画质」实测就是推广位）；无可用档位返回空串。
func (d *zszData) pickFormat(maxHeight int) (videoURL, note string) {
	hs := make([]int, len(d.Formats)) // 0 = 不可用（推广/读不出数字）
	best, lowest := -1, -1
	for i, f := range d.Formats {
		if strings.Contains(f.Video, "daf0.cn") {
			continue
		}
		hs[i] = heightOfNote(f.QualityNote)
		if hs[i] == 0 {
			continue
		}
		if best == -1 || hs[i] > hs[best] {
			best = i
		}
		if lowest == -1 || hs[i] < hs[lowest] {
			lowest = i
		}
	}
	if best == -1 {
		return "", ""
	}
	pick := best
	if maxHeight > 0 && hs[best] > maxHeight {
		pick = lowest // 上限低于当前最高档：找不超过上限的最高档，找不到就最低档
		for i, h := range hs {
			if h > 0 && h <= maxHeight && h > hs[pick] {
				pick = i
			}
		}
	}
	return d.Formats[pick].Video, d.Formats[pick].QualityNote
}

// zszHeightRE 画质说明开头的数字（"720p"/"1292P(线路二)"）。
var zszHeightRE = regexp.MustCompile(`^(\d+)`)

// heightOfNote 读画质说明里的高度；读不出（如「最高画质」）返回 0。
func heightOfNote(note string) int {
	m := zszHeightRE.FindStringSubmatch(strings.TrimSpace(note))
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// ---- 微信扫码自动续期 ----

// ensureRelogin 异步发起扫码续期。静默跳过的情形：未注入 notifier / 开关关闭 /
// 已有流程在跑 / 距上次发起不足 reloginMinInterval——解析链的冷却源每 10min 兜底
// 重试一次会反复命中失效特征，靠这个频控消化，不另加停用逻辑。
func (c *ZhishuzhanClient) ensureRelogin() {
	c.mu.Lock()
	if c.notifier == nil || !c.autoRelogin || c.relogining ||
		(!c.lastRelogin.IsZero() && time.Since(c.lastRelogin) < c.reloginMinInterval) {
		c.mu.Unlock()
		return
	}
	c.relogining = true
	c.lastRelogin = time.Now()
	n := c.notifier
	c.mu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[zhishuzhan] 扫码续期 panic: %v", r)
			}
			c.mu.Lock()
			c.relogining = false
			c.mu.Unlock()
		}()
		// 总预算 6min：二维码约 5min 有效 + 轮询余量；pollTimeout（默认 5min）通常先到。
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		c.relogin(ctx, n)
	}()
}

// relogin 一轮完整续期流程：取码 → 推二维码 → 轮询扫码 → 换凭据落盘（或超时收尾）。
func (c *ZhishuzhanClient) relogin(ctx context.Context, n ReloginNotifier) {
	// 1. 取登录二维码。
	qrURL, ticket, err := c.fetchQrcode(ctx)
	if err != nil {
		log.Printf("[zhishuzhan] 续期启动失败: %v", err)
		n.SendText(ctx, "zhishuzhan 续期启动失败: "+err.Error())
		return
	}
	// 2. 推给主人（Url 是 mp.weixin.qq.com 公网 JPEG，QQ 可直接拉取发图）。
	n.SendImage(ctx, qrURL)
	n.SendText(ctx, "zhishuzhan 登录已过期，请用微信扫码续期（二维码约 5 分钟内有效）")
	// 3. 轮询扫码状态直到成功或超时。
	deadline := time.Now().Add(c.pollTimeout)
	for {
		tok, uid, ok, err := c.checkQrcode(ctx, ticket)
		if err != nil {
			log.Printf("[zhishuzhan] 轮询扫码状态失败: %v", err) // 网络抖动不当失败，继续等到超时
		} else if ok {
			c.mu.Lock()
			c.Tokens, c.UserID = tok, uid
			hook := c.onCredsRefreshed
			c.mu.Unlock()
			if serr := c.saveSession(tok, uid); serr != nil {
				log.Printf("[zhishuzhan] 凭据落盘失败: %v", serr)
			}
			n.SendText(ctx, "✅ zhishuzhan 扫码续期成功")
			if hook != nil { // 凭据就绪回调（VideoPlugin 用来重试刚才失败的链接），异步免阻塞
				go hook()
			}
			return
		}
		if time.Now().After(deadline) {
			n.SendText(ctx, "⏰ zhishuzhan 扫码超时未续期，下次解析需要时会再次提醒")
			return
		}
		select {
		case <-ctx.Done(): // 总预算耗尽，同超时语义收尾
			n.SendText(ctx, "⏰ zhishuzhan 扫码超时未续期，下次解析需要时会再次提醒")
			return
		case <-time.After(c.pollInterval):
		}
	}
}

// fetchQrcode 取微信登录二维码（无需 cookie，带桌面 UA 即可）。
func (c *ZhishuzhanClient) fetchQrcode(ctx context.Context) (qrURL, ticket string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/v1/qrcode/get", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("user-agent", zszUserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("请求二维码接口失败: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	var r zszResp
	if json.Unmarshal(b, &r) != nil || r.Code != 200 {
		return "", "", fmt.Errorf("获取二维码失败: %s", truncStr(string(b), 120))
	}
	var d struct {
		Url    string `json:"Url"`
		Ticket string `json:"Ticket"`
	}
	if err := json.Unmarshal(r.Data, &d); err != nil || d.Url == "" || d.Ticket == "" {
		return "", "", fmt.Errorf("二维码响应格式异常: %s", truncStr(string(b), 120))
	}
	return d.Url, d.Ticket, nil
}

// checkQrcode 查扫码状态：未扫码返回 ok=false；扫码确认后返回新凭据
// （正是解析接口的两个 cookie，前端 setCookie 300 天）。
func (c *ZhishuzhanClient) checkQrcode(ctx context.Context, ticket string) (tokens, uid string, ok bool, err error) {
	u := c.Base + "/v2/qrcode/check?ticket=" + url.QueryEscape(ticket)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", false, err
	}
	req.Header.Set("user-agent", zszUserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", "", false, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", false, err
	}
	var r zszResp
	if json.Unmarshal(b, &r) != nil {
		return "", "", false, fmt.Errorf("扫码状态响应异常: %s", truncStr(string(b), 120))
	}
	if r.Code != 200 { // 201 = 还没扫码，继续等
		return "", "", false, nil
	}
	// UserId 服务端给的是数字（实测 2026-10-26 线上：721427），Token 是字符串；
	// RawMessage 收下再去引号，两种形态都兼容。
	var d struct {
		Token  string          `json:"Token"`
		UserId json.RawMessage `json:"UserId"`
	}
	if err := json.Unmarshal(r.Data, &d); err != nil {
		return "", "", false, fmt.Errorf("扫码结果格式异常: %s", truncStr(string(b), 120))
	}
	uid = strings.Trim(strings.TrimSpace(string(d.UserId)), `"`)
	if d.Token == "" || uid == "" { // 结构对但凭据没就绪：当作还没扫完，继续等（超时兜底）
		return "", "", false, nil
	}
	return d.Token, uid, true, nil
}
