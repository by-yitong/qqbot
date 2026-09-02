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
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/tidwall/gjson"

	"qqbot/internal/bot"
	"qqbot/internal/utils"
)

// Webhook 是飞书式 webhook 插件：外部系统 POST 一个地址，bot 把内容主动发到对应群。
//
//	群内 /webhook        → 生成/刷新本群的推送地址（含签名，group_id + sign 定位）。
//	                       地址私聊发给触发的主人，群里只回确认——sign 是唯一凭证，
//	                       不能在群聊里裸回（私聊主动消息需 q.qq.com 开通权限，
//	                       失败会提示去私聊窗口重取：那里是被动回复，不依赖权限）。
//	私聊 /webhook [群id] → 同上。缺省用该主人最近在群里触发过的群（内存记忆，重启即失）。
//	/webhook off [群id]  → 作废地址（群里触发/私聊带参/私聊用记忆，同上）。
//	/webhook white add|del|list <ip> → 管理来源 IP 白名单
//	POST {base}/webhook/{group_id}/{sign}  → 推送（兼容飞书自定义机器人 JSON 格式）
//
// 命令仅主人可用（.OwnerOnly，BOT_OWNERS 配置）——生成地址等于放开外部向群里
// 注入消息的入口，不能交给任意群成员。白名单为空时允许所有来源（内部用图省事；
// 要收紧就配 white）。群主动消息需在 q.qq.com 开通权限，受官方频控/额度限制。
type Webhook struct {
	file       string // 落盘路径（data/webhook.json）
	listenAddr string // HTTP 监听地址，空则不启服务
	baseURL    string // 生成地址时的公网前缀；空则用请求 Host

	bot       *bot.Bot
	mu        sync.Mutex
	conf      webhookConf
	lastGroup map[string]string // 主人 openid -> 最近 gen/off 的目标群（私聊免参用，不落盘）

	srv *http.Server
}

type webhookConf struct {
	Groups    map[string]string `json:"groups"`    // 群 openid -> 签名
	Whitelist []string          `json:"whitelist"` // 来源 IP 白名单
}

// NewWebhook 创建插件。file 是配置落盘路径；listenAddr 空 = 不启 HTTP 服务（只留命令）。
func NewWebhook(file, listenAddr, baseURL string) *Webhook {
	return &Webhook{file: file, listenAddr: listenAddr, baseURL: baseURL, lastGroup: map[string]string{}}
}

// NewWebhookFromEnv 读 WEBHOOK_LISTEN 等变量构造；未配监听地址返回 nil（不注册）。
// /webhook 仅主人可用（BOT_OWNERS）；没配主人 = 命令谁都用不了，这里提醒一句。
func NewWebhookFromEnv() bot.Plugin {
	addr := utils.EnvOr("WEBHOOK_LISTEN", "")
	if addr == "" {
		return nil
	}
	log.Println("[webhook] 已启用 webhook 推送，监听 " + addr)
	if len(utils.SplitList(os.Getenv("BOT_OWNERS"))) == 0 {
		log.Println("[webhook] ⚠️ 未配置 BOT_OWNERS：/webhook 命令将对所有人拒绝（用 /id 查 openid 后配置）")
	}
	return NewWebhook(utils.EnvOr("WEBHOOK_FILE", "./data/webhook.json"), addr, utils.EnvOr("WEBHOOK_BASE_URL", ""))
}

// Install 加载配置、注册命令与 HTTP 服务。
func (p *Webhook) Install(b *bot.Bot) {
	p.bot = b
	if err := p.load(); err != nil {
		log.Printf("[webhook] 配置加载失败（从空配置开始）: %v", err)
	}
	b.Command("webhook", p.cmd).
		Desc("生成外部推送地址（POST 消息进本群）；white 管理 IP 白名单").
		Alias("推送地址").
		OwnerOnly()
	if p.listenAddr != "" {
		b.OnStart(p.serve)
	}
}

// --- 配置落盘 ---

func (p *Webhook) load() error {
	data, err := os.ReadFile(p.file)
	if errors.Is(err, os.ErrNotExist) {
		p.conf = webhookConf{Groups: map[string]string{}}
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

func (p *Webhook) cmd(c *bot.Ctx) {
	switch c.Args.Get(0) {
	case "", "gen", "new":
		p.cmdGen(c, c.Args.Get(1))

	case "white", "whitelist", "白名单":
		p.cmdWhite(c)

	case "off", "close":
		p.cmdOff(c)

	default:
		// 私聊里 /webhook <群openid>：群里触发失败后的兜底路径提示的就是这个形式，
		// 群 id 直接跟在命令后。群里发未知子命令多半是手误，回用法即可。
		if c.IsPrivate() {
			p.cmdGen(c, c.Args.Get(0))
			return
		}
		c.ReplyText("用法：/webhook（生成本群推送地址，地址走私聊）｜/webhook off（作废）｜" +
			"/webhook white add|del|list <ip>｜私聊里可用 /webhook <群openid>")
	}
}

// targetGroup 解析命令针对的群：群里触发 = 本群；私聊触发 = explicit 参数里的群
// openid，缺省用该主人最近在群里触发过的群（lastGroup 记忆，重启即失）。
func (p *Webhook) targetGroup(c *bot.Ctx, explicit string) (string, bool) {
	if c.IsGroup() {
		return c.GroupID(), true
	}
	if id := strings.TrimSpace(explicit); id != "" {
		return id, true
	}
	p.mu.Lock()
	id := p.lastGroup[c.UserID()]
	p.mu.Unlock()
	return id, id != ""
}

// newGroupURL 为 groupID 生成新签名、落盘并返回推送地址；同时记住触发者最近
// 的目标群（私聊免参用）。旧签名即刻作废。
func (p *Webhook) newGroupURL(userID, groupID string) string {
	p.mu.Lock()
	sign := newSign()
	p.conf.Groups[groupID] = sign
	p.save()
	p.lastGroup[userID] = groupID
	p.mu.Unlock()
	return p.url(groupID, sign)
}

// cmdGen 生成/刷新推送地址。私聊触发：被动回复直接带地址（不依赖主动消息权限）；
// 群里触发：地址走私聊发主人（主动消息），群里只回确认，不暴露 sign。
func (p *Webhook) cmdGen(c *bot.Ctx, explicitGroup string) {
	groupID, ok := p.targetGroup(c, explicitGroup)
	if !ok {
		c.ReplyText("请告诉我目标群：群里直接发 /webhook（生成本群地址），或私聊发 /webhook <群openid>")
		return
	}
	url := p.newGroupURL(c.UserID(), groupID)
	dm := "群 " + groupID + " 的推送地址（旧地址即刻作废）：\n" + url +
		"\nPOST 调用，body 为飞书机器人格式 {\"msg_type\":\"text\",\"content\":{\"text\":\"...\"}}，或纯文本"

	if !c.IsGroup() {
		c.ReplyText(dm)
		return
	}
	// 私聊主动消息（msgAnchor 空）需 q.qq.com 开通权限、受额度限制；失败给自救路径：
	// 私聊窗口重取是被动回复，必达。
	if _, err := c.Bot.SendC2CText(c.Context, c.UserID(), "", dm); err != nil {
		c.ReplyText("地址已生成，但私聊发送失败（可能未开通主动消息或额度用尽）。\n" +
			"请到 bot 的私聊窗口发送（会重新生成，旧地址作废）：\n/webhook " + groupID)
		return
	}
	c.ReplyText("本群推送地址已生成，已私聊发给你（旧地址即刻作废）")
}

// cmdOff 作废推送地址并清掉该主人的目标群记忆。
func (p *Webhook) cmdOff(c *bot.Ctx) {
	groupID, ok := p.targetGroup(c, c.Args.Get(1))
	if !ok {
		c.ReplyText("请告诉我目标群：群里直接发 /webhook off，或私聊发 /webhook off <群openid>")
		return
	}
	p.mu.Lock()
	delete(p.conf.Groups, groupID)
	if p.lastGroup[c.UserID()] == groupID {
		delete(p.lastGroup, c.UserID())
	}
	p.save()
	p.mu.Unlock()
	c.ReplyText("群 " + groupID + " 的推送地址已作废")
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
	log.Printf("[webhook] 推送服务已监听 http://%s/webhook/{group_id}/{sign}", ln.Addr())
	go func() {
		<-ctx.Done()
		_ = p.srv.Close()
	}()
	if err := p.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("[webhook] HTTP 服务退出: %v", err)
	}
}

// handle 处理 POST /webhook/{group_id}/{sign}。
func (p *Webhook) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // webhook/{group}/{sign}
	if len(parts) != 3 {
		http.Error(w, "bad path", http.StatusNotFound)
		return
	}
	groupID, sign := parts[1], parts[2]

	p.mu.Lock()
	want, ok := p.conf.Groups[groupID]
	wl := append([]string(nil), p.conf.Whitelist...)
	p.mu.Unlock()
	if !allowedIP(wl, r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !ok || sign != want {
		http.Error(w, "invalid sign", http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	text := extractText(body)
	if text == "" {
		http.Error(w, "empty text", http.StatusBadRequest)
		return
	}
	// 可选 ?title=xx：带了就走 markdown（加粗标题 + 内容原样拼接，内容本身是 md，
	// 不能再包 ```，否则渲染成一坨代码块），不带发纯文本。
	// title 只是展示参数，不参与路径/sign 校验。
	if title := strings.TrimSpace(r.URL.Query().Get("title")); title != "" {
		md := fmt.Sprintf("**%s**\n\n%s", title, text)
		_, err = p.bot.SendGroupMarkdown(r.Context(), groupID, "", md)
	} else {
		_, err = p.bot.SendGroupText(r.Context(), groupID, "", text)
	}
	if err != nil {
		log.Printf("[webhook] 发送到群 %s 失败: %v", groupID, err)
		http.Error(w, "send: "+err.Error(), http.StatusBadGateway)
		return
	}
	// 飞书自定义机器人的标准返回格式：宝塔等对接方靠它判定接入是否成功。
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"StatusCode":0,"StatusMessage":"success","code":0,"data":{},"msg":"success"}`))
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

// extractText 从 body 提取要发的文本：飞书机器人 JSON（content.text / text 字段）、
// 任意 JSON 的 text 字段，或纯文本 body。
func extractText(body []byte) string {
	s := strings.TrimSpace(string(body))
	if s == "" {
		return ""
	}
	if s[0] == '{' {
		for _, key := range []string{"content.text", "text"} {
			if v := gjson.Get(s, key); v.Exists() {
				return strings.TrimSpace(stripLarkTags(v.String()))
			}
		}
		return ""
	}
	return stripLarkTags(s)
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
