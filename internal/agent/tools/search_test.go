package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newFakeMCP 起一个假 anysearch MCP 端点，记录请求并按 resp 回复。
func newFakeMCP(t *testing.T, resp string, check func(body, auth, client string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if check != nil {
			check(string(b), r.Header.Get("Authorization"), r.Header.Get("X-Anysearch-Client"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, resp)
	}))
}

func TestWebSearchOK(t *testing.T) {
	srv := newFakeMCP(t, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"结果A"}]}}`,
		func(body, auth, client string) {
			if !strings.Contains(body, "tools/call") || !strings.Contains(body, `"search"`) {
				t.Errorf("请求体应含 tools/call + search: %s", body)
			}
			if client != "skill/3.0.1" {
				t.Errorf("X-Anysearch-Client=%q want skill/3.0.1", client)
			}
			if auth != "" {
				t.Errorf("无 key 时不应带 Authorization，得到 %q", auth)
			}
		})
	defer srv.Close()

	a := &AnySearch{BaseURL: srv.URL}
	ws := &WebSearch{Owner: a}
	out, err := ws.Execute(context.Background(), `{"query":"猫","max_results":3}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "结果A" {
		t.Errorf("got %q want 结果A", out)
	}
}

func TestWebSearchAPIKeyHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`)
	}))
	defer srv.Close()

	a := &AnySearch{BaseURL: srv.URL, APIKey: "abc123"}
	if _, err := (&WebSearch{Owner: a}).Execute(context.Background(), `{"query":"x"}`); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer abc123" {
		t.Errorf("Authorization=%q want Bearer abc123", gotAuth)
	}
}

func TestWebSearchRPCError(t *testing.T) {
	srv := newFakeMCP(t, `{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"rate limited"}}`, nil)
	defer srv.Close()
	a := &AnySearch{BaseURL: srv.URL}
	if _, err := (&WebSearch{Owner: a}).Execute(context.Background(), `{"query":"x"}`); err == nil {
		t.Fatal("JSON-RPC error 应返回错误")
	}
}

func TestWebSearchHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"msg":"no key"}`)
	}))
	defer srv.Close()
	a := &AnySearch{BaseURL: srv.URL}
	if _, err := (&WebSearch{Owner: a}).Execute(context.Background(), `{"query":"x"}`); err == nil {
		t.Fatal("HTTP 401 应返回错误")
	}
}

func TestWebSearchMissingQuery(t *testing.T) {
	a := &AnySearch{BaseURL: "http://unused"}
	if _, err := (&WebSearch{Owner: a}).Execute(context.Background(), `{}`); err == nil {
		t.Fatal("缺 query 应报错")
	}
}

func TestWebExtract(t *testing.T) {
	srv := newFakeMCP(t, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"# 标题\n正文"}]}}`,
		func(body, _, _ string) {
			if !strings.Contains(body, `"extract"`) || !strings.Contains(body, "http://example.com") {
				t.Errorf("extract 请求应含 url: %s", body)
			}
		})
	defer srv.Close()
	a := &AnySearch{BaseURL: srv.URL}
	out, err := (&WebExtract{Owner: a}).Execute(context.Background(), `{"url":"http://example.com"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "正文") {
		t.Errorf("got %q", out)
	}
}

func TestWebExtractMissingURL(t *testing.T) {
	a := &AnySearch{BaseURL: "http://unused"}
	if _, err := (&WebExtract{Owner: a}).Execute(context.Background(), `{}`); err == nil {
		t.Fatal("缺 url 应报错")
	}
}

func TestWebSearchEmptyResult(t *testing.T) {
	// result.content 为空 → 返回占位。
	srv := newFakeMCP(t, `{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`, nil)
	defer srv.Close()
	a := &AnySearch{BaseURL: srv.URL}
	out, err := (&WebSearch{Owner: a}).Execute(context.Background(), `{"query":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "(无结果)" {
		t.Errorf("空结果应返回占位，got %q", out)
	}
}
