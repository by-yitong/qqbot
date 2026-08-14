package imgstore

import (
	"os"
	"testing"
	"time"
)

func TestPutAndResolve(t *testing.T) {
	s := New(0, 0) // 用默认 ttl/max
	ref := s.Put([]byte("hello"), "png")
	if ref == "" || !startsWith(ref, Scheme) {
		t.Fatalf("Put 应返回 %s 开头的引用, got %q", Scheme, ref)
	}
	data, ct, fn, ok := s.Resolve(ref)
	if !ok {
		t.Fatalf("Resolve(%q) 未命中", ref)
	}
	if string(data) != "hello" {
		t.Errorf("data=%q want hello", data)
	}
	if ct != "image/png" {
		t.Errorf("contentType=%q want image/png", ct)
	}
	if fn == "" || !endsWith(fn, ".png") {
		t.Errorf("filename=%q 应以 .png 结尾", fn)
	}
}

func TestResolveNonGenimg(t *testing.T) {
	s := New(0, 0)
	for _, ref := range []string{"https://x/a.png", "genimg:", "data:image/png;base64,xxx", ""} {
		if _, _, _, ok := s.Resolve(ref); ok {
			t.Errorf("非 genimg: 引用 %q 不应命中", ref)
		}
	}
}

func TestResolveUnknownID(t *testing.T) {
	s := New(0, 0)
	if _, _, _, ok := s.Resolve("genimg:deadbeef.png"); ok {
		t.Error("未知 id 不应命中")
	}
}

func TestExtNormalization(t *testing.T) {
	s := New(0, 0)
	cases := map[string]string{
		"PNG":     "image/png",
		".jpg":    "image/jpeg",
		"jpeg":    "image/jpeg",
		"webp":    "image/webp",
		"":        "image/png",
		"unknown": "image/png",
	}
	for ext, wantCT := range cases {
		ref := s.Put([]byte("x"), ext)
		_, ct, _, ok := s.Resolve(ref)
		if !ok {
			t.Errorf("ext=%q Resolve 未命中", ext)
			continue
		}
		if ct != wantCT {
			t.Errorf("ext=%q contentType=%q want %q", ext, ct, wantCT)
		}
	}
}

func TestCapacityEviction(t *testing.T) {
	s := New(time.Hour, 3) // ttl 长，只测容量淘汰
	for i := 0; i < 3; i++ {
		s.Put([]byte("a"), "png")
	}
	if s.Len() != 3 {
		t.Fatalf("Len=%d want 3", s.Len())
	}
	// 第 4 张：最旧（第 1 张）应被淘汰。记录所有 id。
	ref1 := s.Put([]byte("first"), "png")
	_ = ref1
	// 再填到上限淘汰，确保最早的那批被删
	time.Sleep(5 * time.Millisecond) // 确保 createdAt 不同
	s.Put([]byte("b"), "png")
	// 现在 Len 应 <= 3
	if got := s.Len(); got > 3 {
		t.Errorf("超过 max 应淘汰, Len=%d", got)
	}
}

func TestEvictExpired(t *testing.T) {
	s := New(1*time.Millisecond, 100)
	s.Put([]byte("x"), "png")
	time.Sleep(20 * time.Millisecond)
	s.evictExpired()
	if s.Len() != 0 {
		t.Errorf("过期后应清空, Len=%d", s.Len())
	}
}

func TestEvictOldestKeepsNewest(t *testing.T) {
	s := New(time.Hour, 100)
	r1 := s.Put([]byte("1"), "png")
	time.Sleep(2 * time.Millisecond)
	r2 := s.Put([]byte("2"), "png")
	s.evictOldestLocked() // 删 r1（最旧）
	if _, _, _, ok := s.Resolve(r1); ok {
		t.Error("最旧 r1 应被删")
	}
	if _, _, _, ok := s.Resolve(r2); !ok {
		t.Error("较新 r2 应保留")
	}
}

func TestDefaultShared(t *testing.T) {
	// Default 是进程级单例：两次调用同一实例。
	a, b := Default(), Default()
	if a != b {
		t.Error("Default() 应返回同一实例")
	}
}

func TestSaveDirWritesFile(t *testing.T) {
	dir := t.TempDir()
	s := New(0, 0)
	s.SaveDir = dir
	ref := s.Put([]byte("FILEBYTES"), "png")
	name := ref[len(Scheme):] // id.png
	got, err := os.ReadFile(dir + "/" + name)
	if err != nil {
		t.Fatalf("落盘文件应存在: %v", err)
	}
	if string(got) != "FILEBYTES" {
		t.Errorf("落盘内容=%q want FILEBYTES", string(got))
	}
}

func TestNoSaveDirSkipsDisk(t *testing.T) {
	// SaveDir 为空：只在内存，不落盘（不应 panic）。
	s := New(0, 0)
	ref := s.Put([]byte("x"), "png")
	if _, _, _, ok := s.Resolve(ref); !ok {
		t.Error("内存存取应正常")
	}
}

// 简易字符串前缀/后缀判断（避免引入 strings 仅用于测试可读性）。
func startsWith(s, prefix string) bool { return len(s) >= len(prefix) && s[:len(prefix)] == prefix }
func endsWith(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
