package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileWriteReadRoundTrip(t *testing.T) {
	root := t.TempDir()
	w := &FileWrite{Root: root}
	r := &FileRead{Root: root}

	if _, err := w.Execute(context.Background(), `{"path":"a/b.txt","content":"hello"}`); err != nil {
		t.Fatal(err)
	}
	out, err := r.Execute(context.Background(), `{"path":"a/b.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "hello" {
		t.Errorf("got %q", out)
	}
}

func TestFileReadEscapeRejected(t *testing.T) {
	root := t.TempDir()
	r := &FileRead{Root: root}
	if _, err := r.Execute(context.Background(), `{"path":"../../etc/passwd"}`); err == nil {
		t.Fatal("越界路径应被拒")
	}
}

func TestResolveUnderRoot(t *testing.T) {
	root := t.TempDir()
	abs, _ := filepath.Abs(root)

	p, err := resolveUnder(root, "sub/f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p, abs) {
		t.Errorf("应在 root 下: %s", p)
	}
	if _, err := resolveUnder(root, "../escape"); err == nil {
		t.Fatal("越界应报错")
	}
}
