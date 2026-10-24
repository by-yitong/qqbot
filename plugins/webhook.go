package plugins

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/tidwall/gjson"

	"qqbot/internal/bot"
	"qqbot/internal/imgstore"
	"qqbot/internal/utils"
)

// Webhook 是飞书式 webhook 插件：外部系统 POST 一个地址，bot 把内容主动发到对应群或个人。
//
//	群内 /webhook        → 获取本群推送地址：没有则生成，已有直接沿用（调用方存的
//	                       地址不会因重取而失效）。地址私聊发给触发的主人并带群名称
//	                       （/v2/groups/{id}/info 取，非白名单机器人取不到则退回 openid），
//	                       群里只回确认——sign 是唯一凭证，不能在群聊里裸回（私聊主动
//	                       消息需 q.qq.com 开通权限，失败会提示去私聊窗口重取：那里是
//	                       被动回复，不依赖权限）。
//	群内 /webhook new    → 重新生成本群地址（旧地址即刻作废）。
//	私聊 /webhook        → 获取个人推送地址（没有则生成）：POST 到该地址的内容会
//	                       私聊发回本人。任何用户可用——sign 只能推到自己，泄漏顶多
//	                       被骚扰（/webhook off 作废即可）。
//	私聊 /webhook new    → 重新生成个人地址（旧地址即刻作废）。
//	私聊 /webhook [new] <群openid> → 获取/重新生成指定群的推送地址（群里触发失败后
//	                       的兜底路径提示的就是这个形式）。
//	/webhook off         → 群里=作废本群地址；私聊不带参=作废个人地址；
//	                       /webhook off <群openid> 作废指定群地址。
//	/webhook white add|del|list <ip> → 管理来源 IP 白名单
//	POST {base}/webhook/{openid}/{sign}  → 推送：openid 命中群则发群、命中用户则私聊
//	                         发本人（群/用户 openid 命名空间独立，随机 sign 不会撞对）。
//	                         三种 body：
//	  1. 文本/Markdown：飞书机器人 JSON {"msg_type":"text|markdown","content":{"text":"..."}}
//	     / {"text":"..."} / 纯文本；markdown 走原生 md（群内受官方语料校验/审核），
//	     ?title=xx 让文本加粗标题（text 类型也会因此切成 markdown 发送）
//	  2. 富媒体 URL：{"msg_type":"image|video|voice|file","content":{"url":"https://..."}}，
//	     url 须公网可达（QQ 是 URL 模式自行下载，大小受官方限制：图 20M/视频 30M/
//	     语音 20M 软限）；file 例外——官方对通用文件不拉 URL，服务端代下载（200MB
//	     上限）后分片直传；同 body 可带 content.text，文字与文件各发一条
//	  3. multipart/form-data 直传文件：file 字段 + 可选 type(image/video/voice/file)、
//	     text。file 走官方分片上传直推腾讯（不经图床）；image/video 经图床走 URL
//	     模式（图床未配置时兜底分片直传）；语音必须经图床（mp3/wav 转 silk）
//	?title=xx 参数让文本部分走 markdown（加粗标题）。
//
// 群地址相关操作（群里触发/私聊带群 id/off 群地址/white）仅主人可用（BOT_OWNERS
// 配置，handler 内校验）——群地址等于放开外部向群里注入消息的入口，不能交给任意
// 群成员；个人地址对任何用户开放。白名单为空时允许所有来源（内部用图省事；要收紧
// 就配 white）。主动消息需在 q.qq.com 开通权限，受官方频控/额度限制。
type Webhook struct {
	file       string         // 落盘路径（data/webhook.json）
	listenAddr string         // HTTP 监听地址，空则不启服务
	baseURL    string         // 生成地址时的公网前缀；空则用请求 Host
	imgHost    *imgstore.Host // multipart 直传文件 → 公网 URL（未配置则该模式不可用）

	bot        *bot.Bot
	mu         sync.Mutex
	conf       webhookConf
	groupNames map[string]string // 群 openid -> 群名称缓存（/v2/groups/{id}/info 是 30QPM 白名单接口，失败也缓存空值防反复打）

	srv *http.Server
}

type webhookConf struct {
	Groups    map[string]string `json:"groups"`    // 群 openid -> 签名
	Users     map[string]string `json:"users"`     // 用户 openid -> 签名（个人推送）
	Whitelist []string          `json:"whitelist"` // 来源 IP 白名单
}

// NewWebhook 创建插件。file 是配置落盘路径；listenAddr 空 = 不启 HTTP 服务（只留命令）；
// imgHost 用于 multipart 直传文件发布公网 URL（nil = 直传不可用，JSON url 模式不受影响）。
func NewWebhook(file, listenAddr, baseURL string, imgHost *imgstore.Host) *Webhook {
	return &Webhook{file: file, listenAddr: listenAddr, baseURL: baseURL, imgHost: imgHost,
		groupNames: map[string]string{}}
}

// NewWebhookFromEnv 读 WEBHOOK_LISTEN 等变量构造；未配监听地址返回 nil（不注册）。
// 群地址相关操作仅主人（BOT_OWNERS）；没配主人 = 群地址谁都用不了，这里提醒一句。
func NewWebhookFromEnv(imgHost *imgstore.Host) bot.Plugin {
	addr := utils.EnvOr("WEBHOOK_LISTEN", "")
	if addr == "" {
		return nil
	}
	log.Println("[webhook] 已启用 webhook 推送，监听 " + addr)
	if len(utils.SplitList(os.Getenv("BOT_OWNERS"))) == 0 {
		log.Println("[webhook] ⚠️ 未配置 BOT_OWNERS：/webhook 的群地址相关操作将对所有人拒绝（个人地址不受影响；用 /id 查 openid 后配置）")
	}
	return NewWebhook(utils.EnvOr("WEBHOOK_FILE", "./data/webhook.json"), addr, utils.EnvOr("WEBHOOK_BASE_URL", ""), imgHost)
}

// Install 加载配置、注册命令与 HTTP 服务。
func (p *Webhook) Install(b *bot.Bot) {
	p.bot = b
	if err := p.load(); err != nil {
		log.Printf("[webhook] 配置加载失败（从空配置开始）: %v", err)
	}
	// 不再整体 OwnerOnly：个人地址（私聊 /webhook）对任何用户开放；群地址相关
	// 路径在 handler 内校验主人（见 cmd）。
	b.Command("webhook", p.cmd).
		Desc("获取推送地址（POST 消息进来）：群里=本群、私聊=个人；new 重新生成；white 管理 IP 白名单").
		Alias("推送地址").
		Category(bot.CatAdmin)
	if p.listenAddr != "" {
		b.OnStart(p.serve)
	}
}

// --- 配置落盘 ---

func (p *Webhook) load() error {
	data, err := os.ReadFile(p.file)
	if errors.Is(err, os.ErrNotExist) {
		p.conf = webhookConf{Groups: map[string]string{}, Users: map[string]string{}}
		return nil
	}
	if err != nil {
		return err
	}
	var c webhookConf
	if err := json.Unmarshal(data, &c); err != nil {
		return err
	}
	if c.Groups == nil {
		c.Groups = map[string]string{}
	}
	if c.Users == nil {
		c.Users = map[string]string{}
	}
	p.conf = c
	return nil
}

func (p *Webhook) save() {
	if err := os.MkdirAll(filepath.Dir(p.file), 0o755); err != nil {
		log.Printf("[webhook] 创建配置目录失败: %v", err)
		return
	}
	data, _ := json.MarshalIndent(p.conf, "", "  ")
	if err := os.WriteFile(p.file, data, 0o600); err != nil {
		log.Printf("[webhook] 配置落盘失败: %v", err)
	}
}

// newSign 生成 16 字节随机 hex 签名。
func newSign() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// --- 命令 ---

// webhookUsage 用法提示（群里未知子命令 / 频道触发时回）。
const webhookUsage = "用法：/webhook（获取本群推送地址，没有则生成）｜/webhook new（重新生成）｜" +
	"/webhook off（作废）｜/webhook white add|del|list <ip>（仅主人）\n" +
	"私聊发 /webhook 获取个人推送地址（POST 内容私聊发回你）；私聊 /webhook <群openid> 获取指定群地址"

func (p *Webhook) cmd(c *bot.Ctx) {
	a0 := c.Args.Get(0)
	switch a0 {
	case "white", "whitelist", "白名单":
		p.requireOwner(c, p.cmdWhite)
		return

	case "off", "close":
		p.cmdOff(c)
		return

	case "", "gen", "new":
	default:
		// 私聊里 /webhook <群openid>：群里触发失败后的兜底路径提示的就是这个形式，
		// 群 id 直接跟在命令后。群里发未知子命令多半是手误，回用法即可。
		if !c.IsPrivate() {
			c.ReplyText(webhookUsage)
			return
		}
	}

	forceNew := a0 == "gen" || a0 == "new"
	explicit := ""
	if forceNew {
		explicit = c.Args.Get(1)
	} else if c.IsPrivate() {
		explicit = a0
	}

	// 群地址路径（群里触发或私聊带群 id）仅主人——sign 等于放开外部向群里注入消息。
	if c.IsGroup() || explicit != "" {
		p.requireOwner(c, func(cc *bot.Ctx) { p.cmdGroup(cc, explicit, forceNew) })
		return
	}
	if c.IsPrivate() {
		p.cmdPersonal(c, forceNew) // 个人地址只能推给本人，对任何用户开放
		return
	}
	c.ReplyText(webhookUsage) // 频道：群/个人地址都不适用
}

// requireOwner 主人检查（与框架 OwnerOnly 命令同一话术）：非主人拒绝，主人执行 h。
func (p *Webhook) requireOwner(c *bot.Ctx, h func(*bot.Ctx)) {
	if !c.IsOwner() {
		c.ReplyText("该命令仅主人可用")
		return
	}
	h(c)
}

// targetGroup 解析命令针对的群：群里触发 = 本群；私聊触发 = explicit 参数里的群 openid。
func (p *Webhook) targetGroup(c *bot.Ctx, explicit string) (string, bool) {
	if c.IsGroup() {
		return c.GroupID(), true
	}
	if id := strings.TrimSpace(explicit); id != "" {
		return id, true
	}
	return "", false
}

// groupLabel 群显示标签：优先群名称（GET /v2/groups/{id}/info，白名单接口、30QPM，
// 结果连同失败一起进程内缓存，避免重取地址时反复打接口），格式 「名称」(openid)；
// 取不到名称（如机器人不在接口白名单报 11253）回落纯 openid。
func (p *Webhook) groupLabel(ctx context.Context, groupID string) string {
	p.mu.Lock()
	name, cached := p.groupNames[groupID]
	p.mu.Unlock()
	if !cached {
		if gi, err := p.bot.GetGroupInfo(ctx, groupID); err != nil {
			log.Printf("[webhook] 获取群名称失败（显示回落 openid）: %v", err)
		} else if gi != nil {
			name = gi.GroupName
		}
		p.mu.Lock()
		p.groupNames[groupID] = name
		p.mu.Unlock()
	}
	if name == "" {
		return groupID
	}
	return "「" + name + "」(" + groupID + ")"
}

// cmdGroup 获取/生成本群（或 explicit 指定群）的推送地址：已有直接沿用（不再每次
// 重新生成，调用方脚本里存的地址不会失效），forceNew 才换新签名（旧签名即刻作废）。
// 地址走私聊发给主人并带群名称；群里只回确认，不暴露 sign。私聊触发：被动回复
// 直接带地址（不依赖主动消息权限）。
func (p *Webhook) cmdGroup(c *bot.Ctx, explicit string, forceNew bool) {
	groupID, ok := p.targetGroup(c, explicit)
	if !ok {
		c.ReplyText("请告诉我目标群：群里直接发 /webhook（获取本群地址），或私聊发 /webhook <群openid>")
		return
	}
	p.mu.Lock()
	sign, exists := p.conf.Groups[groupID]
	created := !exists || forceNew
	if created {
		sign = newSign()
		p.conf.Groups[groupID] = sign
		p.save()
	}
	p.mu.Unlock()

	state := "（已有，直接沿用；换新发 /webhook new）"
	if created {
		state = "（新生成，旧地址即刻作废）"
	}
	dm := "群 " + p.groupLabel(c.Context, groupID) + " 的推送地址" + state + "：\n" + p.url(groupID, sign) +
		"\nPOST 调用：文本/Markdown 用 {\"msg_type\":\"text|markdown\",\"content\":{\"text\":\"...\"}} 或纯文本；" +
		"发文件用 {\"msg_type\":\"image|video|voice|file\",\"content\":{\"url\":\"https://...\"}}，或 multipart 直传（file 字段）"

	if !c.IsGroup() {
		c.ReplyText(dm)
		return
	}
	// 私聊主动消息（msgAnchor 空）需 q.qq.com 开通权限、受额度限制；失败给自救路径：
	// 私聊窗口重取是被动回复，必达（get-or-create 语义下重取拿到的是同一地址）。
	if _, err := c.Bot.SendC2CText(c.Context, c.UserID(), "", dm); err != nil {
		c.ReplyText("地址已就绪，但私聊发送失败（可能未开通主动消息或额度用尽）。\n" +
			"请到 bot 的私聊窗口发送（会拿到同一地址，不会作废重建）：\n/webhook " + groupID)
		return
	}
	c.ReplyText("本群推送地址已私聊发给你")
}

// cmdPersonal 私聊获取/生成个人推送地址：POST 到该地址的内容会私聊发回本人。
// 直接回复在当前私聊（被动回复，必达，不依赖主动消息权限）。
func (p *Webhook) cmdPersonal(c *bot.Ctx, forceNew bool) {
	uid := c.UserID()
	p.mu.Lock()
	sign, exists := p.conf.Users[uid]
	created := !exists || forceNew
	if created {
		sign = newSign()
		p.conf.Users[uid] = sign
		p.save()
	}
	p.mu.Unlock()

	state := "（已有，直接沿用；换新发 /webhook new）"
	if created {
		state = "（新生成，旧地址即刻作废）"
	}
	c.ReplyText("你的个人推送地址" + state + "，POST 调用后内容会私聊发给你：\n" + p.url(uid, sign) +
		"\nPOST 调用：文本/Markdown 用 {\"msg_type\":\"text|markdown\",\"content\":{\"text\":\"...\"}} 或纯文本；" +
		"发文件用 {\"msg_type\":\"image|video|voice|file\",\"content\":{\"url\":\"https://...\"}}，或 multipart 直传（file 字段）\n" +
		"作废发 /webhook off")
}

// cmdOff 作废推送地址：群里=本群地址（仅主人）；私聊带群 id=该群地址（仅主人）、
// 不带=个人地址（本人在私聊发即可）。
func (p *Webhook) cmdOff(c *bot.Ctx) {
	arg := strings.TrimSpace(c.Args.Get(1))
	if c.IsGroup() || arg != "" {
		p.requireOwner(c, func(cc *bot.Ctx) {
			// 群里固定作废本群（同 targetGroup：explicit 仅私聊生效）
			groupID := arg
			if cc.IsGroup() {
				groupID = cc.GroupID()
			}
			p.mu.Lock()
			delete(p.conf.Groups, groupID)
			p.save()
			p.mu.Unlock()
			cc.ReplyText("群 " + p.groupLabel(cc.Context, groupID) + " 的推送地址已作废")
		})
		return
	}
	if !c.IsPrivate() {
		c.ReplyText(webhookUsage)
		return
	}
	p.mu.Lock()
	_, exists := p.conf.Users[c.UserID()]
	if exists {
		delete(p.conf.Users, c.UserID())
		p.save()
	}
	p.mu.Unlock()
	if !exists {
		c.ReplyText("你还没有个人推送地址（私聊发 /webhook 生成）")
		return
	}
	c.ReplyText("你的个人推送地址已作废")
}

func (p *Webhook) cmdWhite(c *bot.Ctx) {
	sub := c.Args.Get(1)
	ip := strings.TrimSpace(c.Args.Get(2))
	switch sub {
	case "add":
		if ip == "" {
			c.ReplyText("用法：/webhook white add <ip>（可用 CIDR，如 10.0.0.0/8）")
			return
		}
		if net.ParseIP(ip) == nil {
			if _, _, err := net.ParseCIDR(ip); err != nil {
				c.ReplyText("不合法的 ip/CIDR：" + ip)
				return
			}
		}
		p.mu.Lock()
		if !containsStr(p.conf.Whitelist, ip) {
			p.conf.Whitelist = append(p.conf.Whitelist, ip)
			p.save()
		}
		p.mu.Unlock()
		c.ReplyText("已加白 " + ip)
	case "del", "rm":
		if ip == "" {
			c.ReplyText("用法：/webhook white del <ip>")
			return
		}
		p.mu.Lock()
		out := p.conf.Whitelist[:0]
		for _, v := range p.conf.Whitelist {
			if v != ip {
				out = append(out, v)
			}
		}
		p.conf.Whitelist = out
		p.save()
		p.mu.Unlock()
		c.ReplyText("已移除 " + ip)
	case "", "list":
		p.mu.Lock()
		wl := append([]string(nil), p.conf.Whitelist...)
		p.mu.Unlock()
		if len(wl) == 0 {
			c.ReplyText("白名单为空 = 不限制来源 IP。/webhook white add <ip> 添加")
			return
		}
		c.ReplyText("IP 白名单：\n" + strings.Join(wl, "\n"))
	default:
		c.ReplyText("用法：/webhook white add|del|list <ip>")
	}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (p *Webhook) url(groupID, sign string) string {
	base := p.baseURL
	if base == "" {
		base = "http://<" + p.listenAddr + ">"
	}
	return fmt.Sprintf("%s/webhook/%s/%s", strings.TrimSuffix(base, "/"), groupID, sign)
}

// --- HTTP 服务 ---

// serve 启动 HTTP 服务，随 bot 退出而关闭。
func (p *Webhook) serve(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/", p.handle)
	p.srv = &http.Server{Addr: p.listenAddr, Handler: mux}

	ln, err := net.Listen("tcp", p.listenAddr)
	if err != nil {
		log.Printf("[webhook] HTTP 监听失败（推送功能不可用）: %v", err)
		return
	}
	log.Printf("[webhook] 推送服务已监听 http://%s/webhook/{openid}/{sign}（群 openid 发群、用户 openid 私聊发本人）", ln.Addr())
	go func() {
		<-ctx.Done()
		_ = p.srv.Close()
	}()
	if err := p.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("[webhook] HTTP 服务退出: %v", err)
	}
}

// handle 处理 POST /webhook/{openid}/{sign}（群 openid 发群、用户 openid 私聊发本人）。
func (p *Webhook) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // webhook/{openid}/{sign}
	if len(parts) != 3 {
		http.Error(w, "bad path", http.StatusNotFound)
		return
	}
	openID, sign := parts[1], parts[2]

	p.mu.Lock()
	wl := append([]string(nil), p.conf.Whitelist...)
	p.mu.Unlock()
	if !allowedIP(wl, r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	toUser, ok := p.matchTarget(openID, sign)
	if !ok {
		http.Error(w, "invalid sign", http.StatusForbidden)
		return
	}

	pl, err := p.parseBody(w, r)
	if err != nil {
		writeErr(w, err)
		return
	}
	// 可选 ?title=xx：带了文本部分就走 markdown（加粗标题 + 内容原样拼接，内容本身
	// 是 md，不能再包 ```，否则渲染成一坨代码块）。title 只是展示参数，不参与路径/sign 校验。
	if err := p.deliver(r.Context(), openID, toUser, pl, strings.TrimSpace(r.URL.Query().Get("title"))); err != nil {
		if toUser {
			log.Printf("[webhook] 私聊推送到 %s 失败: %v", openID, err)
		} else {
			log.Printf("[webhook] 发送到群 %s 失败: %v", openID, err)
		}
		http.Error(w, "send: "+err.Error(), http.StatusBadGateway)
		return
	}
	// 飞书自定义机器人的标准返回格式：宝塔等对接方靠它判定接入是否成功。
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"StatusCode":0,"StatusMessage":"success","code":0,"data":{},"msg":"success"}`))
}

// matchTarget 校验 openid+sign 并判定推送目标：先群表后个人表（群/用户 openid 命名
// 空间独立，两边 sign 都是随机 16 字节 hex，不会撞对）。toUser=true 表示个人推送。
func (p *Webhook) matchTarget(id, sign string) (toUser, ok bool) {
	p.mu.Lock()
	wantGroup, okGroup := p.conf.Groups[id]
	wantUser, okUser := p.conf.Users[id]
	p.mu.Unlock()
	switch {
	case okGroup && sign == wantGroup:
		return false, true
	case okUser && sign == wantUser:
		return true, true
	}
	return false, false
}

// --- 请求负载解析 ---

// webhookPayload 一次推送要发的内容：text 与 url/fileData 可各自为空、也可同发
// （先文字后文件）。msgType 是富媒体类型（image/video/voice/file）；直传字节时
// 走 fileData+fileName（file 一律分片直传，图床未配置时 image/video 也兜底）。
type webhookPayload struct {
	msgType  string
	text     string
	url      string
	fileData []byte // multipart 直传的原始字节（分片直传用，不经图床）
	fileName string // multipart 直传的原始文件名
}

// statusErr 带建议 HTTP 状态码的错误（图床/下载侧故障用 5xx）；普通 error 默认 400。
type statusErr struct {
	status int
	msg    string
}

func (e *statusErr) Error() string { return e.msg }

// writeErr 按错误里的状态码回错误，缺省 400（客户端问题）。
func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	var se *statusErr
	if errors.As(err, &se) {
		status = se.status
	}
	http.Error(w, err.Error(), status)
}

// parseBody 按 Content-Type 分流：multipart 直传文件走图床发布公网 URL，
// 其余按 JSON/纯文本解析（富媒体只收公网 url，由 QQ 自行下载）。
func (p *Webhook) parseBody(w http.ResponseWriter, r *http.Request) (webhookPayload, error) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		return p.parseMultipart(w, r)
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return webhookPayload{}, fmt.Errorf("read body: %w", err)
	}
	return extractPayload(body)
}

// extractPayload 从 JSON/纯文本 body 解析推送内容：飞书机器人 JSON
// （content.text / text 字段）、任意 JSON 的 text 字段，或纯文本 body；
// msg_type=image/video/voice/file 时取 content.url / url 发富媒体（可同时带
// content.text 说明文字）。
func extractPayload(body []byte) (webhookPayload, error) {
	s := strings.TrimSpace(string(body))
	if s == "" {
		return webhookPayload{}, errors.New("empty text")
	}
	if s[0] != '{' {
		return webhookPayload{msgType: "text", text: stripLarkTags(s)}, nil
	}
	text := ""
	for _, key := range []string{"content.text", "text"} {
		if v := gjson.Get(s, key); v.Exists() {
			text = strings.TrimSpace(stripLarkTags(v.String()))
			break
		}
	}
	rawURL := ""
	for _, key := range []string{"content.url", "url"} {
		if v := gjson.Get(s, key); v.Exists() {
			rawURL = strings.TrimSpace(v.String())
			break
		}
	}
	switch msgType := strings.ToLower(strings.TrimSpace(gjson.Get(s, "msg_type").String())); msgType {
	case "", "text":
		if text == "" {
			return webhookPayload{}, errors.New("empty text")
		}
		return webhookPayload{msgType: "text", text: text}, nil
	case "markdown", "md":
		// 原生 markdown 群内走语料校验/异步审核，发不出或延迟属官方限制。
		// markdown 只发文本，url 字段忽略（富媒体请用 image/video/voice/file）。
		if text == "" {
			return webhookPayload{}, errors.New("empty text")
		}
		return webhookPayload{msgType: "markdown", text: text}, nil
	case "image", "video", "voice", "file":
		if rawURL == "" {
			return webhookPayload{}, fmt.Errorf("msg_type=%s 需要 url（content.url 或 url 字段）", msgType)
		}
		return webhookPayload{msgType: msgType, text: text, url: rawURL}, nil
	default:
		return webhookPayload{}, fmt.Errorf("不支持的 msg_type: %s（支持 text/markdown/image/video/voice/file）", msgType)
	}
}

// webhookMaxFileBytes multipart 直传的大小上限（官方 file_type 硬限 200MB）。
// 用 var 便于测试覆盖。
var webhookMaxFileBytes int64 = 200 << 20

// parseMultipart 解析 multipart/form-data：file 字段是文件字节（经图床发布公网
// URL——QQ 是 URL 模式下载，直传也绕不开图床），或用 url 字段直接给公网地址；
// 可选 type(image/video/voice/file，缺省按扩展名识别、认不出按通用文件)、
// text（说明文字，先于文件发送）。
func (p *Webhook) parseMultipart(w http.ResponseWriter, r *http.Request) (webhookPayload, error) {
	r.Body = http.MaxBytesReader(w, r.Body, webhookMaxFileBytes)
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return webhookPayload{}, &statusErr{status: http.StatusRequestEntityTooLarge,
				msg: fmt.Sprintf("文件超过大小上限 %d MB", webhookMaxFileBytes>>20)}
		}
		return webhookPayload{}, fmt.Errorf("解析 multipart 失败: %w", err)
	}
	text := stripLarkTags(strings.TrimSpace(r.FormValue("text")))
	msgType := strings.ToLower(strings.TrimSpace(r.FormValue("type")))
	if msgType == "" {
		msgType = strings.ToLower(strings.TrimSpace(r.FormValue("msg_type")))
	}

	ext := ""
	var fileData []byte
	fileName := ""
	rawURL := strings.TrimSpace(r.FormValue("url"))
	if rawURL == "" {
		file, fh, err := r.FormFile("file")
		if err != nil {
			return webhookPayload{}, errors.New("multipart 需要 file（文件）或 url（公网地址）字段")
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			return webhookPayload{}, fmt.Errorf("读文件失败: %w", err)
		}
		ext = strings.ToLower(strings.TrimPrefix(filepath.Ext(fh.Filename), "."))
		fileData, fileName = data, fh.Filename
	} else {
		ext = urlExtOf(rawURL)
	}
	if msgType == "" {
		msgType = mediaKindByExt(ext)
	}
	switch msgType {
	case "image", "video", "voice", "file":
	default:
		return webhookPayload{}, fmt.Errorf("不支持的 type: %s（支持 image/video/voice/file）", msgType)
	}
	// 分发：file 一律分片直传（官方对通用文件不拉 URL，字节直推腾讯）；
	// image/video 优先经图床走 URL 模式（图床未配置时兜底分片直传）；
	// 语音必须经图床（mp3/wav 要转 silk，分片直传改不了格式）。
	if fileData != nil {
		if msgType == "file" || !p.imgHost.Configured() {
			if msgType == "voice" {
				return webhookPayload{}, &statusErr{status: http.StatusServiceUnavailable,
					msg: "语音直传需要图床转码（IMGHOST_BASE_URL 未配置）"}
			}
			return webhookPayload{msgType: msgType, text: text, fileData: fileData, fileName: fileName}, nil
		}
		pub, err := p.imgHost.Upload(r.Context(), fileData, ext)
		if err != nil {
			return webhookPayload{}, &statusErr{status: http.StatusBadGateway, msg: "上传图床失败: " + err.Error()}
		}
		return webhookPayload{msgType: msgType, text: text, url: pub, fileName: fileName}, nil
	}
	return webhookPayload{msgType: msgType, text: text, url: rawURL, fileName: fileName}, nil
}

// extMediaKinds 扩展名 → 富媒体类型。官方图片只认 png/jpg、视频 mp4、语音 silk
// （mp3/wav 走自动转码）；不在表里的扩展名按通用文件（file_type=4）发，无格式要求。
var extMediaKinds = map[string]string{
	"png": "image", "jpg": "image", "jpeg": "image",
	"mp4": "video", "m4v": "video", "mov": "video",
	"silk": "voice", "mp3": "voice", "wav": "voice",
}

// mediaKindByExt 按扩展名猜富媒体类型，认不出按通用文件。
func mediaKindByExt(ext string) string {
	if k, ok := extMediaKinds[ext]; ok {
		return k
	}
	return "file"
}

// urlExtOf 取 URL 路径的扩展名（去点转小写；无扩展返回空）。
func urlExtOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(path.Ext(u.Path), "."))
}

// deliver 把负载发到目标（群或个人私聊）：文本部分先发（msg_type=markdown 或
// ?title= 走原生 markdown，其余纯文本；两者同在时 title 作加粗标题拼在最前），
// 富媒体随后一条。主动消息（msgAnchor 空）需 q.qq.com 开通权限，受官方频控/额度限制。
func (p *Webhook) deliver(ctx context.Context, openID string, toUser bool, pl webhookPayload, title string) error {
	// 群/私聊的 Bot 级发送方法一一对应，按目标选一组，后续编排只写一遍。
	type sendGroup struct {
		text, markdown            func(string) error
		image, video, voice, file func(string) error
		fileBytes                 func(string, []byte) error
		mediaBytes                func(int, string, []byte) error
	}
	var s sendGroup
	if toUser {
		s = sendGroup{
			text:      func(t string) error { _, err := p.bot.SendC2CText(ctx, openID, "", t); return err },
			markdown:  func(m string) error { _, err := p.bot.SendC2CMarkdown(ctx, openID, "", m); return err },
			image:     func(u string) error { _, err := p.bot.SendC2CImage(ctx, openID, "", u); return err },
			video:     func(u string) error { _, err := p.bot.SendC2CVideo(ctx, openID, "", u); return err },
			voice:     func(u string) error { _, err := p.bot.SendC2CVoice(ctx, openID, "", u); return err },
			file:      func(u string) error { _, err := p.bot.SendC2CFile(ctx, openID, "", u); return err },
			fileBytes: func(n string, d []byte) error { _, err := p.bot.SendC2CFileBytes(ctx, openID, "", n, d); return err },
			mediaBytes: func(ft int, n string, d []byte) error {
				_, err := p.bot.SendC2CMediaBytes(ctx, openID, "", ft, n, d)
				return err
			},
		}
	} else {
		s = sendGroup{
			text:      func(t string) error { _, err := p.bot.SendGroupText(ctx, openID, "", t); return err },
			markdown:  func(m string) error { _, err := p.bot.SendGroupMarkdown(ctx, openID, "", m); return err },
			image:     func(u string) error { _, err := p.bot.SendGroupImage(ctx, openID, "", u); return err },
			video:     func(u string) error { _, err := p.bot.SendGroupVideo(ctx, openID, "", u); return err },
			voice:     func(u string) error { _, err := p.bot.SendGroupVoice(ctx, openID, "", u); return err },
			file:      func(u string) error { _, err := p.bot.SendGroupFile(ctx, openID, "", u); return err },
			fileBytes: func(n string, d []byte) error { _, err := p.bot.SendGroupFileBytes(ctx, openID, "", n, d); return err },
			mediaBytes: func(ft int, n string, d []byte) error {
				_, err := p.bot.SendGroupMediaBytes(ctx, openID, "", ft, n, d)
				return err
			},
		}
	}

	if pl.text != "" {
		if pl.msgType == "markdown" || title != "" {
			md := pl.text
			if title != "" {
				md = fmt.Sprintf("**%s**\n\n%s", title, md)
			}
			if err := s.markdown(md); err != nil {
				return err
			}
		} else if err := s.text(pl.text); err != nil {
			return err
		}
	}
	// 直传字节：file 分片直传；image/video 图床缺配时兜底分片。
	if pl.fileData != nil {
		switch pl.msgType {
		case "image":
			return s.mediaBytes(1, pl.fileName, pl.fileData)
		case "video":
			return s.mediaBytes(2, pl.fileName, pl.fileData)
		default: // file
			return s.fileBytes(pl.fileName, pl.fileData)
		}
	}
	if pl.url == "" {
		return nil
	}
	switch pl.msgType {
	case "image":
		return s.image(pl.url)
	case "video":
		return s.video(pl.url)
	case "voice":
		return s.voice(pl.url)
	default: // file：官方不拉 URL，服务端代下载后分片直传
		return s.file(pl.url)
	}
}

// allowedIP 校验来源 IP：白名单支持单 IP 与 CIDR；为空放行。
func allowedIP(wl []string, r *http.Request) bool {
	if len(wl) == 0 {
		return true
	}
	ip := clientIP(r)
	if ip == "" {
		return false
	}
	addr := net.ParseIP(ip)
	if addr == nil {
		return false
	}
	for _, item := range wl {
		if item == ip {
			return true
		}
		if _, cidr, err := net.ParseCIDR(item); err == nil && cidr.Contains(addr) {
			return true
		}
	}
	return false
}

// clientIP 取真实来源 IP：反代场景取 X-Forwarded-For 首个 / X-Real-IP，直连取 RemoteAddr。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			xff = xff[:i]
		}
		return strings.TrimSpace(xff)
	}
	if rip := strings.TrimSpace(r.Header.Get("X-Real-IP")); rip != "" {
		return rip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// larkAtRE 匹配飞书富文本 @ 标签：<at userid='xxx'>名字</at>（引号/有无下划线都兼容）。
var larkAtRE = regexp.MustCompile(`(?i)<at\s+user_?id\s*=\s*['"]?([^'">\s]+)['"]?\s*>(.*?)</at>`)

// stripLarkTags 把飞书消息里的标签转成 QQ 纯文本可读形式：
// @所有人/@all → 直接删除（群消息发不出真 @全体成员，留着也无意义），
// @某人 → 「@名字」（同理发不出真 @，只留可读文本）。
func stripLarkTags(s string) string {
	return larkAtRE.ReplaceAllStringFunc(s, func(m string) string {
		g := larkAtRE.FindStringSubmatch(m)
		if id := strings.ToLower(g[1]); id == "all" || id == "所有人" {
			return ""
		}
		name := g[2]
		if name == "" {
			name = "有人"
		}
		return "@" + name
	})
}
