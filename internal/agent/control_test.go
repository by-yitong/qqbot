package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSkillStoreDynamic(t *testing.T) {
	s := newSkillStore([]Skill{{Name: "a"}})
	if s.Len() != 1 {
		t.Fatalf("初始 len=%d", s.Len())
	}
	s.Add(Skill{Name: "b"})
	if s.Len() != 2 {
		t.Fatalf("加 1 后 len=%d", s.Len())
	}
	// List 返回拷贝：改拷贝不影响内部。
	got := s.List()
	got[0] = Skill{Name: "x"}
	if s.List()[0].Name == "x" {
		t.Error("List 应返回拷贝，外部修改不应影响内部")
	}
}

func TestAddSkillFromURL(t *testing.T) {
	body := "---\nname: greet\ndescription: 问候技能\n---\n打招呼要热情。\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	a := New(Config{LLM: &stubLLM{}})
	sk, err := a.AddSkillFromURL(context.Background(), srv.URL+"/greet.md")
	if err != nil {
		t.Fatalf("AddSkillFromURL: %v", err)
	}
	if sk.Name != "greet" || sk.Description != "问候技能" {
		t.Errorf("技能字段: %+v", sk)
	}
	if !strings.Contains(sk.Content, "热情") {
		t.Errorf("正文: %q", sk.Content)
	}
	if len(a.Skills()) != 1 {
		t.Errorf("应已加入 agent，len=%d", len(a.Skills()))
	}
	if !strings.Contains(a.systemPrompt(), "greet") {
		t.Error("systemPrompt 应包含新技能")
	}
}

func TestAddSkillFromURLBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	a := New(Config{LLM: &stubLLM{}})
	if _, err := a.AddSkillFromURL(context.Background(), srv.URL); err == nil {
		t.Fatal("HTTP 404 应返回错误")
	}
}

func TestSkillAddToolExecute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "---\nname: t\ndescription: d\n---\nbody\n")
	}))
	defer srv.Close()

	a := New(Config{LLM: &stubLLM{}})
	tool := &skillAddTool{ag: a}
	out, err := tool.Execute(context.Background(), `{"url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "t") {
		t.Errorf("返回文本: %q", out)
	}
	if len(a.Skills()) != 1 {
		t.Errorf("工具应已加技能，len=%d", len(a.Skills()))
	}
}

func TestAddMCPFromURLNoTools(t *testing.T) {
	// Tools 为 nil 时应直接报错（不发起网络）。
	a := New(Config{LLM: &stubLLM{}})
	if _, err := a.AddMCPFromURL(context.Background(), "http", "http://example.com/mcp"); err == nil {
		t.Fatal("Tools 为 nil 应报错")
	}
}

func TestMcpNameFromURL(t *testing.T) {
	if got := mcpNameFromURL("https://example.com:8080/mcp"); got == "" {
		t.Error("应返回非空名字")
	}
	if got := mcpNameFromURL("://bad"); got != "server" {
		t.Errorf("坏 URL 应回退到 server，得到 %q", got)
	}
}
