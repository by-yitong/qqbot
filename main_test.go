package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestGuardSingleInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.pid")

	// 无 pid 文件 → 直接写入自己的 pid
	guardSingleInstance(path)
	if b, err := os.ReadFile(path); err != nil || strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("应写入自身 pid: %v %q", err, b)
	}

	// pid 文件是自己的 pid（重复调用）→ 正常通过
	guardSingleInstance(path)

	// 死 pid（几乎不可能存活的大号）→ 接管并改写
	_ = os.WriteFile(path, []byte("999999999"), 0o644)
	guardSingleInstance(path)
	if b, _ := os.ReadFile(path); strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("死 pid 应被接管: %q", b)
	}

	// 活着但不是本 bot 的 pid（如 pid 1 = init）→ 不误拦，接管
	_ = os.WriteFile(path, []byte("1"), 0o644)
	guardSingleInstance(path)
	if b, _ := os.ReadFile(path); strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("无关进程 pid 应被接管: %q", b)
	}

	// 非法内容 → 当作不存在处理
	_ = os.WriteFile(path, []byte("not-a-pid"), 0o644)
	guardSingleInstance(path)
}

func TestParsePrefixes(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"/", []string{"/"}},
		{".|/", []string{".", "/"}}, // | 分隔
		{"/|!|.", []string{"/", "!", "."}},
		{"  . | /  ", []string{".", "/"}}, // 空格容忍
		{".,/", []string{".,/"}},          // 逗号不再分隔（历史坑：全角逗号被当成三字符前缀）
		{" | ", nil},                      // 全是分隔符 → 空（用内置默认）
		{"", nil},
	}
	for _, tc := range cases {
		if got := parsePrefixes(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parsePrefixes(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
