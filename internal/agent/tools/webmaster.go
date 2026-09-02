package tools

// 站长诊断工具集：ping / tcp_ping / whois / dns_lookup / cert_info。
//
// 连接到用户指定目标的工具（ping / tcp_ping / cert_info）复用 http_get 的
// SSRF 守卫（默认拒绝内网/回环，Allow 可放行）；whois 只连固定的 WHOIS 服务器、
// dns_lookup 走系统解析器，均无 SSRF 面。

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

// --- ping ---

// PingTool 对域名/IP 做 ping 测试。
type PingTool struct {
	Allow []string // SSRF 放行名单（同 http_get 的 AGENT_HTTP_ALLOW）
}

// Name 返回工具名。
func (t *PingTool) Name() string { return "ping" }

// Description 返回工具描述。
func (t *PingTool) Description() string {
	return "ping 一个域名/IP，返回延迟与丢包统计（优先系统 ICMP ping，无 ping 命令时退化为 80/443 端口 TCP 探测）。"
}

// Parameters 返回参数 JSON Schema。
func (t *PingTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"host": map[string]any{
				"type":        "string",
				"description": "要 ping 的域名或 IP",
			},
			"count": map[string]any{
				"type":        "integer",
				"description": "ping 次数，默认 4，最大 10",
			},
		},
		"required": []string{"host"},
	}
}

// Guidelines 贡献使用要点。
func (t *PingTool) Guidelines() []string {
	return []string{
		"站长诊断工具（ping/tcp_ping/dns_lookup/whois/cert_info）面向公网目标；内网/回环默认拒绝（AGENT_HTTP_ALLOW 可放行）",
	}
}

// Execute 执行 ping。
func (t *PingTool) Execute(ctx context.Context, arguments string) (string, error) {
	host := strings.TrimSpace(gjson.Get(arguments, "host").String())
	if host == "" {
		return "", fmt.Errorf("缺少 host 参数")
	}
	return PingHost(ctx, host, int(gjson.Get(arguments, "count").Int()), t.Allow)
}

// hasSystemPing 探测系统 ping 命令是否存在（容器里常没有，只探一次）。
var hasSystemPing = sync.OnceValue(func() bool {
	_, err := exec.LookPath("ping")
	return err == nil
})

// PingHost 对 host 做 count 次 ping，返回纯文本摘要（AI ping 工具用）。
// 有系统 ping 走 ICMP；否则退化为 443/80 端口 TCP 探测。
func PingHost(ctx context.Context, host string, count int, allow []string) (string, error) {
	_, text, err := PingHostRender(ctx, host, count, allow)
	return text, err
}

// PingHostRender 跑一次 ping，返回 markdown 与纯文本两种渲染（信息一致）。
// /ping 命令优先发 markdown（渲染更好看），群/私聊公域语料审核拒发时回退纯文本。
// 解析不了的原始输出与 TCP 退化路径两者相同——纯文本本身也是合法 markdown。
func PingHostRender(ctx context.Context, host string, count int, allow []string) (string, string, error) {
	if err := assertSafeHost(host, allow); err != nil {
		return "", "", err
	}
	if count <= 0 || count > 10 {
		count = 4
	}
	if hasSystemPing() {
		raw, timedOut, err := systemPingRaw(ctx, host, count)
		if raw == "" && err != nil {
			return "", "", fmt.Errorf("ping %s 失败: %w", host, err)
		}
		md, text := truncate(raw, 1500), truncate(raw, 1500)
		if r := parsePingResult(host, raw); r != nil {
			md, text = r.markdown(), r.text()
		}
		if timedOut {
			md, text = md+"\n（超时中止）", text+"\n（超时中止）"
		}
		return md, text, nil
	}
	for _, port := range []string{"443", "80"} {
		out, err := TCPPing(ctx, host, port, count, allow)
		if err == nil {
			s := "（系统无 ping 命令，以 " + port + " 端口 TCP 探测代替）\n" + out
			return s, s, nil
		}
	}
	return "", "", fmt.Errorf("ping %s 失败：443/80 端口均无法连接", host)
}

// systemPingRaw 调系统 ping（Linux：ping -c N -W 2 host，每包最多等 2s）返回原始输出。
// 固定 LANG=C 保证可解析（服务器是中文 locale 时 iputils 会把 time= 本地化）。
func systemPingRaw(ctx context.Context, host string, count int) (string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(count*3+5)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ping", "-c", strconv.Itoa(count), "-W", "2", host)
	cmd.Env = append(os.Environ(), "LANG=C", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), ctx.Err() != nil, err
}

// --- ping 输出解析与排版 ---

var (
	// pingStatRe 匹配 iputils 汇总行（busybox 的 "4 packets received" 也兼容）：
	// "4 packets transmitted, 4 received, 0% packet loss, time 3004ms"
	pingStatRe = regexp.MustCompile(`(\d+) packets? transmitted, (\d+)(?: packets?)? received.*?([\d.]+)% packet loss`)
	// pingRTTRe 匹配 "rtt min/avg/max/mdev = 46.941/47.080/47.192/0.094 ms"
	pingRTTRe = regexp.MustCompile(`= ([\d.]+)/([\d.]+)/([\d.]+)/([\d.]+)\s*ms`)
	// pingIPRe 匹配表头 "PING phpcurl.cn (103.117.137.83) 56(84) bytes of data."
	pingIPRe  = regexp.MustCompile(`PING \S+ \(([^)]+)\)`)
	pingTTLRe = regexp.MustCompile(`ttl=(\d+)`)
)

// pingResult 是 ping 结果的结构化数据，text/markdown 两种渲染共用。
type pingResult struct {
	host                string
	ip                  string
	sent, recv          int
	loss                string // 百分数（不带 %）
	ttl                 string
	minMs, avgMs, maxMs string // 1 位小数
	mdevMs              string // 抖动
}

// parsePingResult 从系统 ping 原始输出解析；格式不认识（busybox 等变体）返回 nil，
// 调用方回退显示原文，信息不丢。
func parsePingResult(host, out string) *pingResult {
	stat := pingStatRe.FindStringSubmatch(out)
	if stat == nil {
		return nil
	}
	sent, _ := strconv.Atoi(stat[1])
	recv, _ := strconv.Atoi(stat[2])
	r := &pingResult{host: host, ip: host, sent: sent, recv: recv, loss: stat[3]}
	if m := pingIPRe.FindStringSubmatch(out); m != nil {
		r.ip = m[1]
	}
	if m := pingTTLRe.FindStringSubmatch(out); m != nil {
		r.ttl = m[1]
	}
	if rtt := pingRTTRe.FindStringSubmatch(out); rtt != nil {
		r.minMs, r.avgMs, r.maxMs, r.mdevMs = ms1(rtt[1]), ms1(rtt[2]), ms1(rtt[3]), ms1(rtt[4])
	}
	return r
}

// title 是两版渲染共用的标题段（host 本身是 IP 时不重复带括号）。
func (r *pingResult) title() string {
	if r.ip != r.host {
		return "📶 ping " + r.host + " (" + r.ip + ")"
	}
	return "📶 ping " + r.host
}

// text 渲染成紧凑纯文本（AI 工具结果 / markdown 拒发时的回退）。
func (r *pingResult) text() string {
	var sb strings.Builder
	if r.recv == 0 {
		fmt.Fprintf(&sb, "%s\n❌ %d/%d 收到，丢包 %s%%", r.title(), r.recv, r.sent, r.loss)
		return sb.String()
	}
	ttl := ""
	if r.ttl != "" {
		ttl = "，TTL " + r.ttl
	}
	fmt.Fprintf(&sb, "%s\n✅ %d/%d 收到，丢包 %s%%%s\n", r.title(), r.recv, r.sent, r.loss, ttl)
	fmt.Fprintf(&sb, "⏱ 平均 %s ms（%s ~ %s，抖动 %s ms）", r.avgMs, r.minMs, r.maxMs, r.mdevMs)
	return sb.String()
}

// markdown 渲染成原生 markdown（/ping 命令优先用它回复，更好看）。
func (r *pingResult) markdown() string {
	var sb strings.Builder
	if r.ip != r.host {
		fmt.Fprintf(&sb, "**📶 Ping %s**（%s）\n\n", r.host, r.ip)
	} else {
		fmt.Fprintf(&sb, "**📶 Ping %s**\n\n", r.host)
	}
	if r.recv == 0 {
		fmt.Fprintf(&sb, "- ❌ 收到 **%d/%d**，丢包 **%s%%**", r.recv, r.sent, r.loss)
		return sb.String()
	}
	ttl := ""
	if r.ttl != "" {
		ttl = "，TTL " + r.ttl
	}
	fmt.Fprintf(&sb, "- ✅ 收到 **%d/%d**，丢包 **%s%%**%s\n", r.recv, r.sent, r.loss, ttl)
	fmt.Fprintf(&sb, "- ⏱ 延迟：平均 **%sms**，最小 %sms / 最大 %sms，抖动 %sms",
		r.avgMs, r.minMs, r.maxMs, r.mdevMs)
	return sb.String()
}

// ms1 把毫秒数值规整成 1 位小数（46.941 → 46.9）；解析失败原样返回。
func ms1(s string) string {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return s
	}
	return strconv.FormatFloat(f, 'f', 1, 64)
}

// --- tcp_ping ---

// TCPPingTool 探测 host:port 的 TCP 连通性并测连接延迟。
type TCPPingTool struct {
	Allow []string // SSRF 放行名单
}

// Name 返回工具名。
func (t *TCPPingTool) Name() string { return "tcp_ping" }

// Description 返回工具描述。
func (t *TCPPingTool) Description() string {
	return "TCP ping：对 host:port 做多次 TCP 连接探测，判断端口是否开放并统计 min/avg/max 延迟与成功率。"
}

// Parameters 返回参数 JSON Schema。
func (t *TCPPingTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"host": map[string]any{
				"type":        "string",
				"description": "域名或 IP",
			},
			"port": map[string]any{
				"type":        "string",
				"description": "端口号，如 443、8080",
			},
			"count": map[string]any{
				"type":        "integer",
				"description": "探测次数，默认 4，最大 10",
			},
		},
		"required": []string{"host", "port"},
	}
}

// Execute 执行 TCP ping。
func (t *TCPPingTool) Execute(ctx context.Context, arguments string) (string, error) {
	host := strings.TrimSpace(gjson.Get(arguments, "host").String())
	port := strings.TrimSpace(gjson.Get(arguments, "port").String())
	if host == "" || port == "" {
		return "", fmt.Errorf("缺少 host/port 参数")
	}
	return TCPPing(ctx, host, port, int(gjson.Get(arguments, "count").Int()), t.Allow)
}

// TCPPing 对 host:port 做 count 次 TCP 连接（每次 3s 超时），返回逐次结果与延迟统计。
// 全部失败算错误（PingHost 的退化探测靠它换下一个端口）。
func TCPPing(ctx context.Context, host, port string, count int, allow []string) (string, error) {
	if err := assertSafeHost(host, allow); err != nil {
		return "", err
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return "", fmt.Errorf("端口不合法: %q", port)
	}
	if count <= 0 || count > 10 {
		count = 4
	}
	addr := net.JoinHostPort(host, port)
	var okCount int
	var total, minDur, maxDur time.Duration
	times := make([]string, 0, count)
	for i := 1; i <= count; i++ {
		start := time.Now()
		conn, derr := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", addr)
		el := time.Since(start)
		if derr != nil {
			times = append(times, "失败("+shortErr(derr)+")")
			continue
		}
		_ = conn.Close()
		okCount++
		total += el
		if minDur == 0 || el < minDur {
			minDur = el
		}
		if el > maxDur {
			maxDur = el
		}
		times = append(times, msStr(el))
	}
	if okCount == 0 {
		return "", fmt.Errorf("TCP %s：%d 次连接全部失败（端口不通或被防火墙过滤）", addr, count)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "🔌 TCP %s\n✅ %d/%d 连通\n", addr, okCount, count)
	fmt.Fprintf(&sb, "⏱ 平均 %s ms（%s ~ %s）\n", msStr(total/time.Duration(okCount)), msStr(minDur), msStr(maxDur))
	sb.WriteString("逐次: " + strings.Join(times, ", ") + " ms")
	return sb.String(), nil
}

// msStr 把时长格式化成毫秒 1 位小数（34.1）。
func msStr(d time.Duration) string {
	return strconv.FormatFloat(float64(d.Microseconds())/1000, 'f', 1, 64)
}

// shortErr 把连接错误压短（逐次明细里全量错误太长）。
func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ":"); i > 0 && len(s)-i < 60 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}

// --- whois ---

// WhoisTool 查询域名/IP 的 whois 注册信息（纯 Go，43 端口 + referral 跟随）。
type WhoisTool struct {
	Timeout time.Duration // 总超时，默认 20s
}

// Name 返回工具名。
func (t *WhoisTool) Name() string { return "whois" }

// Description 返回工具描述。
func (t *WhoisTool) Description() string {
	return "查询域名/IP 的 whois 信息（注册商、注册与到期时间、域名服务器、域名状态等）。"
}

// Parameters 返回参数 JSON Schema。
func (t *WhoisTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"domain": map[string]any{
				"type":        "string",
				"description": "域名（如 example.com）或 IP",
			},
		},
		"required": []string{"domain"},
	}
}

// Execute 执行 whois 查询。
func (t *WhoisTool) Execute(ctx context.Context, arguments string) (string, error) {
	domain := strings.TrimSpace(gjson.Get(arguments, "domain").String())
	if domain == "" {
		return "", fmt.Errorf("缺少 domain 参数")
	}
	return WhoisQuery(ctx, domain, t.Timeout)
}

// WhoisQuery 查询 domain 的 whois：先问 whois.iana.org 拿 TLD 注册局 referral，
// 再问注册局；薄注册局（.com/.net 等）的应答里还有 "Registrar WHOIS Server:"，
// 跟过去拿完整注册商记录。最多 3 跳。
func WhoisQuery(ctx context.Context, domain string, timeout time.Duration) (string, error) {
	target, err := normalizeWhoisTarget(domain)
	if err != nil {
		return "", err
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	qctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	server := "whois.iana.org"
	seen := map[string]bool{server: true}
	var last string
	for hop := 0; hop < 3; hop++ {
		body, aerr := whoisAsk(qctx, server, target)
		if aerr != nil {
			if last != "" {
				return compressWhois(last) + "\n\n（进一步查询 " + server + " 失败: " + aerr.Error() + "）", nil
			}
			return "", fmt.Errorf("whois %s 失败: %w", target, aerr)
		}
		last = body
		next := whoisReferral(body)
		if next == "" || seen[next] {
			break
		}
		seen[next] = true
		server = next
	}
	out := compressWhois(last)
	if out == "" {
		return "", fmt.Errorf("%s 无 whois 记录（或该 TLD 不支持 whois）", target)
	}
	return out, nil
}

// WhoisRender 查询 whois 并返回 markdown 与纯文本两种渲染（/whois 命令用）。
// markdown 把记录放进代码块保持逐行排版；AI 工具仍用 WhoisQuery 的纯文本。
func WhoisRender(ctx context.Context, domain string, timeout time.Duration) (string, string, error) {
	target, err := normalizeWhoisTarget(domain)
	if err != nil {
		return "", "", err
	}
	rec, err := WhoisQuery(ctx, domain, timeout)
	if err != nil {
		return "", "", err
	}
	text := "🔍 whois " + target + "\n" + rec
	md := "**🔍 Whois " + target + "**\n\n```\n" + rec + "\n```"
	return md, text, nil
}

// whoisAsk 向 server:43 发一次 whois 查询（服务器答完主动断开，读到 EOF）。
func whoisAsk(ctx context.Context, server, query string) (string, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(server, "43"))
	if err != nil {
		return "", fmt.Errorf("连接 %s:43 失败: %w", server, err)
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if deadline.IsZero() {
		deadline = time.Now().Add(10 * time.Second)
	}
	_ = conn.SetDeadline(deadline)
	if _, err := io.WriteString(conn, query+"\r\n"); err != nil {
		return "", fmt.Errorf("发送查询失败: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(conn, 64<<10))
	if err != nil && len(data) == 0 {
		return "", fmt.Errorf("读取应答失败: %w", err)
	}
	return string(data), nil
}

// whoisReferral 按行解析应答里的下一跳 whois 服务器：优先 "Registrar WHOIS Server:"
// （薄注册局指到注册商），其次 "Whois Server:"/"whois:"（iana 指到 TLD 注册局）。
func whoisReferral(body string) string {
	registrar, generic := "", ""
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		var val string
		switch {
		case strings.HasPrefix(lower, "registrar whois server:"):
			val = strings.TrimSpace(line[len("registrar whois server:"):])
			if validWhoisHost(val) && registrar == "" {
				registrar = val
			}
		case strings.HasPrefix(lower, "whois server:"), strings.HasPrefix(lower, "whois:"):
			val = strings.TrimSpace(line[strings.Index(lower, ":")+1:])
			if validWhoisHost(val) && generic == "" {
				generic = val
			}
		}
	}
	if registrar != "" {
		return registrar
	}
	return generic
}

// validWhoisHost 判定 referral 值像个 whois 服务器域名。
func validWhoisHost(s string) bool {
	return s != "" && strings.Contains(s, ".") && !strings.ContainsAny(s, " \t/:")
}

// normalizeWhoisTarget 规整 whois 查询目标：容忍贴 URL（取主机名）、host:port、
// 尾部圆点；统一小写。IPv6 字面量（含多个冒号）原样保留。
func normalizeWhoisTarget(s string) (string, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "", fmt.Errorf("缺少 domain 参数")
	}
	if strings.Contains(s, "://") || strings.ContainsAny(s, "/?#") {
		u, err := url.Parse(s)
		if err != nil || u.Hostname() == "" {
			return "", fmt.Errorf("无法从 %q 解析出域名", s)
		}
		s = u.Hostname()
	} else if strings.Count(s, ":") == 1 { // 裸 host:port
		if _, err := strconv.Atoi(s[strings.Index(s, ":")+1:]); err == nil {
			s = s[:strings.Index(s, ":")]
		}
	}
	s = strings.TrimSuffix(s, ".")
	if s == "" || len(s) > 253 || strings.ContainsAny(s, " \t") {
		return "", fmt.Errorf("不合法的查询目标 %q", s)
	}
	return s, nil
}

// compressWhois 压缩 whois 应答：合并连续空行、截断（记录常带大段法律声明）。
func compressWhois(s string) string {
	var sb strings.Builder
	blank := false
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if strings.TrimSpace(line) == "" {
			blank = true
			continue
		}
		if blank && sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		blank = false
		sb.WriteString(strings.TrimRight(line, "\r"))
		sb.WriteByte('\n')
	}
	return truncate(strings.TrimRight(sb.String(), "\n"), 2000)
}

// --- dns_lookup ---

// DNSLookupTool 查域名的 DNS 记录（走系统解析器）。
type DNSLookupTool struct{}

// Name 返回工具名。
func (t *DNSLookupTool) Name() string { return "dns_lookup" }

// Description 返回工具描述。
func (t *DNSLookupTool) Description() string {
	return "DNS 解析：查域名的 A/AAAA/CNAME/MX/NS/TXT/PTR 记录（走服务器系统解析器，可判断解析是否生效）。"
}

// Parameters 返回参数 JSON Schema。
func (t *DNSLookupTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "域名（查 PTR 时填 IP）",
			},
			"type": map[string]any{
				"type":        "string",
				"description": "记录类型：A/AAAA/CNAME/MX/NS/TXT/PTR，默认 A",
			},
		},
		"required": []string{"name"},
	}
}

// Execute 执行 DNS 查询。
func (t *DNSLookupTool) Execute(ctx context.Context, arguments string) (string, error) {
	name := strings.TrimSpace(gjson.Get(arguments, "name").String())
	qtype := strings.ToUpper(strings.TrimSpace(gjson.Get(arguments, "type").String()))
	if name == "" {
		return "", fmt.Errorf("缺少 name 参数")
	}
	if qtype == "" {
		qtype = "A"
	}
	r := net.DefaultResolver
	start := time.Now()
	var lines []string
	switch qtype {
	case "A", "AAAA":
		ips, err := r.LookupIP(ctx, "ip", name)
		if err != nil {
			return "", fmt.Errorf("解析失败: %w", err)
		}
		for _, ip := range ips {
			if (qtype == "A") == (ip.To4() != nil) {
				lines = append(lines, qtype+" "+ip.String())
			}
		}
	case "CNAME":
		cn, err := r.LookupCNAME(ctx, name)
		if err != nil {
			return "", fmt.Errorf("解析失败: %w", err)
		}
		lines = append(lines, "CNAME "+cn)
	case "MX":
		mxs, err := r.LookupMX(ctx, name)
		if err != nil {
			return "", fmt.Errorf("解析失败: %w", err)
		}
		for _, mx := range mxs {
			lines = append(lines, fmt.Sprintf("MX %s (优先级 %d)", strings.TrimSuffix(mx.Host, "."), mx.Pref))
		}
	case "NS":
		nss, err := r.LookupNS(ctx, name)
		if err != nil {
			return "", fmt.Errorf("解析失败: %w", err)
		}
		for _, ns := range nss {
			lines = append(lines, "NS "+strings.TrimSuffix(ns.Host, "."))
		}
	case "TXT":
		txts, err := r.LookupTXT(ctx, name)
		if err != nil {
			return "", fmt.Errorf("解析失败: %w", err)
		}
		for _, txt := range txts {
			lines = append(lines, "TXT "+txt)
		}
	case "PTR":
		names, err := r.LookupAddr(ctx, name)
		if err != nil {
			return "", fmt.Errorf("反解失败: %w", err)
		}
		for _, n := range names {
			lines = append(lines, "PTR "+strings.TrimSuffix(n, "."))
		}
	default:
		return "", fmt.Errorf("不支持的记录类型 %q（支持 A/AAAA/CNAME/MX/NS/TXT/PTR）", qtype)
	}
	if len(lines) == 0 {
		return name + " 没有 " + qtype + " 记录（解析耗时 " + time.Since(start).Round(time.Millisecond).String() + "）", nil
	}
	return strings.Join(lines, "\n") + "\n（解析耗时 " + time.Since(start).Round(time.Millisecond).String() + "）", nil
}

// --- cert_info ---

// CertInfoTool 读取 HTTPS 证书详情（链校验失败也能读，方便排障自签/过期证书）。
type CertInfoTool struct {
	Allow   []string      // SSRF 放行名单
	Timeout time.Duration // 连接超时，默认 8s
}

// Name 返回工具名。
func (t *CertInfoTool) Name() string { return "cert_info" }

// Description 返回工具描述。
func (t *CertInfoTool) Description() string {
	return "查询 HTTPS 证书详情（主题/颁发者/生效与到期时间/剩余天数/SAN 域名列表），适合检查证书是否即将过期。"
}

// Parameters 返回参数 JSON Schema。
func (t *CertInfoTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"host": map[string]any{
				"type":        "string",
				"description": "域名或 IP",
			},
			"port": map[string]any{
				"type":        "string",
				"description": "端口，默认 443",
			},
		},
		"required": []string{"host"},
	}
}

// Execute 读取并展示证书。
func (t *CertInfoTool) Execute(ctx context.Context, arguments string) (string, error) {
	host := strings.TrimSpace(gjson.Get(arguments, "host").String())
	port := strings.TrimSpace(gjson.Get(arguments, "port").String())
	if host == "" {
		return "", fmt.Errorf("缺少 host 参数")
	}
	if port == "" {
		port = "443"
	}
	if err := assertSafeHost(host, t.Allow); err != nil {
		return "", err
	}
	cert, note, err := fetchPeerCert(ctx, host, port, t.Timeout)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "证书详情 %s\n", net.JoinHostPort(host, port))
	if note != "" {
		sb.WriteString(note + "\n")
	}
	fmt.Fprintf(&sb, "主题: %s\n", cert.Subject.CommonName)
	fmt.Fprintf(&sb, "颁发者: %s\n", cert.Issuer.CommonName)
	if len(cert.DNSNames) > 0 {
		names := cert.DNSNames
		if len(names) > 10 {
			names = names[:10]
			names = append(names, "…")
		}
		fmt.Fprintf(&sb, "SAN: %s\n", strings.Join(names, ", "))
	}
	fmt.Fprintf(&sb, "生效: %s\n", cert.NotBefore.Format("2006-01-02"))
	fmt.Fprintf(&sb, "到期: %s\n", cert.NotAfter.Format("2006-01-02"))
	days := time.Until(cert.NotAfter) / (24 * time.Hour)
	switch {
	case days < 0:
		fmt.Fprintf(&sb, "状态: ⚠️ 已过期 %d 天", -days)
	case days <= 14:
		fmt.Fprintf(&sb, "状态: ⚠️ 仅剩 %d 天，尽快续期", days)
	default:
		fmt.Fprintf(&sb, "状态: 剩余 %d 天", days)
	}
	return sb.String(), nil
}

// fetchPeerCert 与 host:port 做 TLS 握手取对端证书：先按标准校验链；失败
// （自签/过期/域名不匹配）再用 InsecureSkipVerify 读一次，附上失败原因。
func fetchPeerCert(ctx context.Context, host, port string, timeout time.Duration) (*x509.Certificate, string, error) {
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	d := &net.Dialer{Timeout: timeout}
	addr := net.JoinHostPort(host, port)

	plain, err := tls.DialWithDialer(d, "tcp", addr, &tls.Config{ServerName: host})
	if err == nil {
		defer plain.Close()
		if cs := plain.ConnectionState(); len(cs.PeerCertificates) > 0 {
			return cs.PeerCertificates[0], "", nil
		}
		return nil, "", fmt.Errorf("%s 未返回证书", addr)
	}
	bad, err2 := tls.DialWithDialer(d, "tcp", addr, &tls.Config{ServerName: host, InsecureSkipVerify: true})
	if err2 != nil {
		return nil, "", fmt.Errorf("连接 %s 失败: %w", addr, err2)
	}
	defer bad.Close()
	cs := bad.ConnectionState()
	if len(cs.PeerCertificates) == 0 {
		return nil, "", fmt.Errorf("%s 未返回证书", addr)
	}
	return cs.PeerCertificates[0], "⚠️ 证书链校验失败: " + err.Error() + "\n（以下为服务器实际出示的证书）", nil
}
