package tools

import "testing"

// TestAssertSafeURL 只用 IP 字面量与 host 匹配，不依赖真实 DNS，可在离线环境运行。
func TestAssertSafeURL(t *testing.T) {
	cases := []struct {
		url     string
		allow   []string
		wantErr bool
	}{
		{"https://8.8.8.8/a", nil, false},                        // 公网 IP，放行
		{"http://8.8.8.8", nil, false},                           // 公网 IP，放行
		{"ftp://8.8.8.8", nil, true},                             // 非 http(s)
		{"http://127.0.0.1", nil, true},                          // 回环
		{"http://192.168.1.1", nil, true},                        // 私网
		{"http://10.0.0.1", nil, true},                           // 私网
		{"http://169.254.1.1", nil, true},                        // 链路本地
		{"http://internal.svc", []string{"internal.svc"}, false}, // allow 放行（host 直接匹配，不解析）
		{"http://internal.svc", nil, true},                       // 未在 allow 且无法解析 → 报错
	}
	for _, c := range cases {
		err := assertSafeURL(c.url, c.allow)
		got := err != nil
		if got != c.wantErr {
			t.Errorf("assertSafeURL(%q, allow=%v) err=%v wantErr=%v", c.url, c.allow, err, c.wantErr)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("abc", 10); got != "abc" {
		t.Errorf("短串应原样返回，得到 %q", got)
	}
	got := truncate("abcdefghij", 3)
	if !startsWith(got, "abc") {
		t.Errorf("应截断到前 3 字符，得到 %q", got)
	}
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
