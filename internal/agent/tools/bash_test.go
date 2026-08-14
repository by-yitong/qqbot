package tools

import (
	"context"
	"strings"
	"testing"
)

func TestBashEcho(t *testing.T) {
	b := &Bash{}
	out, err := b.Execute(context.Background(), `{"command":"echo hello"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "hello" {
		t.Errorf("got %q", out)
	}
}

func TestBashNonZeroExit(t *testing.T) {
	b := &Bash{}
	out, err := b.Execute(context.Background(), `{"command":"exit 3"}`)
	if err != nil {
		t.Fatal("非零退出不应返回 error")
	}
	if !strings.Contains(out, "exit code 3") {
		t.Errorf("应包含 exit code: %q", out)
	}
}

func TestBashAllowListReject(t *testing.T) {
	b := &Bash{Allow: []string{"git"}}
	if _, err := b.Execute(context.Background(), `{"command":"echo hi"}`); err == nil {
		t.Fatal("非白名单命令应被拒")
	}
}

func TestBashAllowListMetachar(t *testing.T) {
	b := &Bash{Allow: []string{"echo"}}
	if _, err := b.Execute(context.Background(), `{"command":"echo a; rm -rf x"}`); err == nil {
		t.Fatal("白名单模式下元字符应被拒")
	}
}

func TestBashAllowListOK(t *testing.T) {
	b := &Bash{Allow: []string{"echo"}}
	out, err := b.Execute(context.Background(), `{"command":"echo ok"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "ok" {
		t.Errorf("got %q", out)
	}
}

func TestBashMissingCommand(t *testing.T) {
	b := &Bash{}
	if _, err := b.Execute(context.Background(), `{}`); err == nil {
		t.Fatal("缺 command 应报错")
	}
}
