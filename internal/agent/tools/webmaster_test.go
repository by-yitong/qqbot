package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestAssertSafeHost 只用 IP 字面量与 allow 匹配，不依赖真实 DNS，可离线运行。
func TestAssertSafeHost(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.1.1", "0.0.0.0"} {
		if err := assertSafeHost(host, nil); err == nil {
			t.Errorf("%s 应被拒绝", host)
		}
	}
	if err := assertSafeHost("127.0.0.1", []string{"127.0.0.1"}); err != nil {
		t.Errorf("allow 放行失败: %v", err)
	}
	if err := assertSafeHost("8.8.8.8", nil); err != nil {
		t.Errorf("公网 IP 应放行: %v", err)
	}
	if err := assertSafeHost("-evil", nil); err == nil {
		t.Error("以 - 开头应被拒绝（防 ping 参数注入）")
	}
	if err := assertSafeHost("   ", nil); err == nil {
		t.Error("空主机应被拒绝")
	}
}

// TestPingHostGuard 内网目标默认拒绝（不打真实 ping）。
func TestPingHostGuard(t *testing.T) {
	if _, err := PingHost(context.Background(), "192.168.0.1", 1, nil); err == nil {
		t.Error("内网目标默认应被拒绝")
	}
}

// TestPingRender 用真实 iputils 输出验证 ping 的纯文本与 markdown 两种渲染。
func TestPingRender(t *testing.T) {
	raw := "PING phpcurl.cn (103.117.137.83) 56(84) bytes of data.\n" +
		"64 bytes from 103.117.137.83 (103.117.137.83): icmp_seq=1 ttl=251 time=46.9 ms\n" +
		"64 bytes from 103.117.137.83 (103.117.137.83): icmp_seq=2 ttl=251 time=47.1 ms\n" +
		"64 bytes from 103.117.137.83 (103.117.137.83): icmp_seq=3 ttl=251 time=47.1 ms\n" +
		"64 bytes from 103.117.137.83 (103.117.137.83): icmp_seq=4 ttl=251 time=47.2 ms\n" +
		"\n--- phpcurl.cn ping statistics ---\n" +
		"4 packets transmitted, 4 received, 0% packet loss, time 3004ms\n" +
		"rtt min/avg/max/mdev = 46.941/47.080/47.192/0.094 ms\n"

	r := parsePingResult("phpcurl.cn", raw)
	if r == nil {
		t.Fatal("应能解析 iputils 输出")
	}
	text, md := r.text(), r.markdown()
	for _, want := range []string{
		"📶 ping phpcurl.cn (103.117.137.83)",
		"✅ 4/4 收到，丢包 0%，TTL 251",
		"⏱ 平均 47.1 ms（46.9 ~ 47.2，抖动 0.1 ms）",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("文本渲染缺 %q:\n%s", want, text)
		}
	}
	for _, want := range []string{
		"**📶 Ping phpcurl.cn**（103.117.137.83）",
		"- ✅ 收到 **4/4**，丢包 **0%**，TTL 251",
		"- ⏱ 延迟：平均 **47.1ms**，最小 46.9ms / 最大 47.2ms，抖动 0.1ms",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown 渲染缺 %q:\n%s", want, md)
		}
	}
	if strings.Contains(text, "64 bytes") || strings.Contains(md, "64 bytes") {
		t.Errorf("不应再输出原始逐包行:\n%s\n%s", text, md)
	}

	// 100% 丢包
	loss := "PING x.cn (1.2.3.4) 56(84) bytes of data.\n\n--- x.cn ping statistics ---\n4 packets transmitted, 0 received, 100% packet loss, time 8010ms\n"
	r = parsePingResult("x.cn", loss)
	if r == nil {
		t.Fatal("全丢包输出应能解析")
	}
	if got := r.text(); !strings.Contains(got, "❌ 0/4 收到，丢包 100%") {
		t.Errorf("全丢包文本不对: %q", got)
	}
	if got := r.markdown(); !strings.Contains(got, "❌ 收到 **0/4**，丢包 **100%**") {
		t.Errorf("全丢包 markdown 不对: %q", got)
	}

	// 目标本身是 IP：不重复打印
	ipOnly := "PING 8.8.8.8 (8.8.8.8) 56(84) bytes of data.\n64 bytes from 8.8.8.8: icmp_seq=1 ttl=117 time=1.0 ms\n\n--- 8.8.8.8 ping statistics ---\n1 packets transmitted, 1 received, 0% packet loss, time 0ms\nrtt min/avg/max/mdev = 1.000/1.000/1.000/0.000 ms\n"
	r = parsePingResult("8.8.8.8", ipOnly)
	if got := r.text(); strings.Count(got, "8.8.8.8") != 1 {
		t.Errorf("IP 目标文本不应重复打印: %q", got)
	}
	if got := r.markdown(); strings.Count(got, "8.8.8.8") != 1 {
		t.Errorf("IP 目标 markdown 不应重复打印: %q", got)
	}

	// 不认识的格式：解析返回 nil（调用方回退原文）
	if parsePingResult("h", "some busybox variant output") != nil {
		t.Error("无法解析应返回 nil")
	}
}

// TestTCPPingLocal 用 httptest 本地服务验证 TCP 探测与 allow 放行。
func TestTCPPingLocal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	host, port := u.Hostname(), u.Port()

	out, err := TCPPing(context.Background(), host, port, 2, []string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("TCPPing 失败: %v", err)
	}
	if !strings.Contains(out, "2/2 连通") || !strings.Contains(out, "平均") {
		t.Errorf("应含摘要统计: %q", out)
	}
	// 不在 allow：拒绝
	if _, err := TCPPing(context.Background(), host, port, 1, nil); err == nil {
		t.Error("内网目标默认应被拒绝")
	}
	// 端口不合法
	if _, err := TCPPing(context.Background(), host, "99999", 1, []string{"127.0.0.1"}); err == nil {
		t.Error("非法端口应报错")
	}
}

// TestCertInfoLocal 用 httptest TLS 服务验证证书读取（自签证书走「校验失败仍读取」分支）。
func TestCertInfoLocal(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	tool := &CertInfoTool{Allow: []string{"127.0.0.1"}}
	out, err := tool.Execute(context.Background(),
		`{"host":"`+u.Hostname()+`","port":"`+u.Port()+`"}`)
	if err != nil {
		t.Fatalf("cert_info 失败: %v", err)
	}
	for _, want := range []string{"证书链校验失败", "到期:", "剩余"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺 %q: %q", want, out)
		}
	}
	// 不在 allow：拒绝
	if _, err := tool.Execute(context.Background(), `{"host":"10.0.0.5"}`); err == nil {
		t.Error("内网目标默认应被拒绝")
	}
}

// TestWhoisReferral 验证 referral 解析：registrar 优先、iana 格式次之、无 referral 为空。
func TestWhoisReferral(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"registrar 优先", "Whois Server: a.example\nRegistrar WHOIS Server: whois.godaddy.com\n", "whois.godaddy.com"},
		{"iana 格式", "Domain Name: COM\nWhois: whois.verisign-grs.com\n", "whois.verisign-grs.com"},
		{"whois server 带端口忽略", "Whois Server: http://x.example:43/a\n", ""},
		{"无 referral", "Domain Name: EXAMPLE.COM\n Creation Date: 1\n", ""},
		{"空应答", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := whoisReferral(tc.body); got != tc.want {
				t.Errorf("whoisReferral = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNormalizeWhoisTarget 验证查询目标规整。
func TestNormalizeWhoisTarget(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://Example.COM/path?q=1", "example.com"},
		{"EXAMPLE.com.", "example.com"},
		{"example.com:443", "example.com"},
		{" example.com ", "example.com"},
		{"2001:db8::1", "2001:db8::1"},
	}
	for _, tc := range cases {
		got, err := normalizeWhoisTarget(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("normalizeWhoisTarget(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "   ", "a b.com"} {
		if _, err := normalizeWhoisTarget(bad); err == nil {
			t.Errorf("%q 应报错", bad)
		}
	}
}

// TestDNSLookupLocalhost 走系统解析器查 localhost，不依赖外网。
func TestDNSLookupLocalhost(t *testing.T) {
	tool := &DNSLookupTool{}
	out, err := tool.Execute(context.Background(), `{"name":"localhost","type":"A"}`)
	if err != nil {
		t.Fatalf("dns_lookup 失败: %v", err)
	}
	if !strings.Contains(out, "127.0.0.1") {
		t.Errorf("localhost 应解析到 127.0.0.1: %q", out)
	}
	if _, err := tool.Execute(context.Background(), `{"name":"x.com","type":"BOGUS"}`); err == nil {
		t.Error("不支持的记录类型应报错")
	}
}
