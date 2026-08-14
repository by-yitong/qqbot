package plugins

import (
	"strings"
	"testing"
)

func TestHumanSize(t *testing.T) {
	cases := map[uint64]string{
		0:          "0 B",
		512:        "512 B",
		1024:       "1.0 KiB",
		1536:       "1.5 KiB",
		1048576:    "1.0 MiB",
		1073741824: "1.0 GiB",
	}
	for in, want := range cases {
		if got := humanSize(in); got != want {
			t.Errorf("humanSize(%d)=%q want %q", in, got, want)
		}
	}
}

func TestPct(t *testing.T) {
	if got := pct(25, 100); got != 25 {
		t.Errorf("pct(25,100)=%v want 25", got)
	}
	if got := pct(1, 3); got < 33 || got > 34 {
		t.Errorf("pct(1,3)=%v want ~33.3", got)
	}
	if got := pct(0, 0); got != 0 {
		t.Errorf("pct(0,0)=%v want 0", got)
	}
}

func TestSystemRender(t *testing.T) {
	// render 不应 panic，且包含关键字段。
	md := NewSystem().render()
	for _, want := range []string{"服务器状态", "Go 版本", "CPU", "当前时间"} {
		if !strings.Contains(md, want) {
			t.Errorf("render 缺少 %q", want)
		}
	}
}
