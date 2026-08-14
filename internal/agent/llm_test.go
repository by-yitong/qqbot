package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// llmFixture 起一个假的 /chat/completions 端点，固定返回 respJSON。
func llmFixture(t *testing.T, respJSON string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respJSON))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCompleteParsesUsageOpenAIStyle(t *testing.T) {
	// OpenAI 风格：prompt_tokens_details.cached_tokens（bigmodel 等走这种）。
	srv := llmFixture(t, `{
		"choices": [{"message": {"role": "assistant", "content": "hi"}, "finish_reason": "stop"}],
		"usage": {
			"prompt_tokens": 1200,
			"completion_tokens": 300,
			"total_tokens": 1500,
			"prompt_tokens_details": {"cached_tokens": 1000}
		}
	}`)
	var mu sync.Mutex
	var got []Usage
	c := NewLLMClient(srv.URL, "k", "glm-5-turbo")
	c.OnUsage = func(u Usage) {
		mu.Lock()
		got = append(got, u)
		mu.Unlock()
	}
	res, err := c.Complete(context.Background(), "sys", []Message{NewUser("hi")}, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Usage == nil {
		t.Fatal("Usage 应非 nil")
	}
	u := res.Usage
	if u.PromptTokens != 1200 || u.CompletionTokens != 300 || u.TotalTokens != 1500 {
		t.Fatalf("token 数错: %+v", u)
	}
	if !u.CacheSupported || u.CachedTokens != 1000 {
		t.Fatalf("缓存解析错: %+v", u)
	}
	if u.DurationMs < 0 {
		t.Fatalf("DurationMs=%d", u.DurationMs)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].CachedTokens != 1000 || got[0].Model != "glm-5-turbo" {
		t.Fatalf("OnUsage 回调数据错: %+v", got)
	}
}

func TestCompleteParsesUsageDeepSeekStyle(t *testing.T) {
	// DeepSeek 风格：prompt_cache_hit_tokens / prompt_cache_miss_tokens。
	srv := llmFixture(t, `{
		"choices": [{"message": {"role": "assistant", "content": "hi"}, "finish_reason": "stop"}],
		"usage": {
			"prompt_tokens": 500,
			"completion_tokens": 100,
			"total_tokens": 600,
			"prompt_cache_hit_tokens": 400,
			"prompt_cache_miss_tokens": 100
		}
	}`)
	c := NewLLMClient(srv.URL, "k", "deepseek-chat")
	res, err := c.Complete(context.Background(), "", []Message{NewUser("hi")}, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !res.Usage.CacheSupported || res.Usage.CachedTokens != 400 {
		t.Fatalf("DeepSeek 缓存解析错: %+v", res.Usage)
	}
}

func TestCompleteUsageAbsent(t *testing.T) {
	// 网关不回 usage：Usage 非 nil 但全零、CacheSupported=false（不瞎编）。
	srv := llmFixture(t, `{
		"choices": [{"message": {"role": "assistant", "content": "hi"}, "finish_reason": "stop"}]
	}`)
	c := NewLLMClient(srv.URL, "k", "m")
	res, err := c.Complete(context.Background(), "", []Message{NewUser("hi")}, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Usage == nil {
		t.Fatal("usage 缺失时 Usage 应为零值结构而非 nil")
	}
	if res.Usage.CacheSupported || res.Usage.PromptTokens != 0 {
		t.Fatalf("应全零: %+v", res.Usage)
	}
}

func TestUsageString(t *testing.T) {
	u := Usage{Model: "m", PromptTokens: 1000, CompletionTokens: 100, TotalTokens: 1100,
		CachedTokens: 800, CacheSupported: true, DurationMs: 2000}
	s := u.String()
	for _, want := range []string{"m", "输入1000", "输出100", "缓存命中800(80%)", "50tok/s"} {
		if !strings.Contains(s, want) {
			t.Errorf("String()=%q 应含 %q", s, want)
		}
	}
	// 不支持缓存：不显示缓存段
	u2 := Usage{Model: "m", PromptTokens: 100, CompletionTokens: 10, DurationMs: 1000}
	if strings.Contains(u2.String(), "缓存") {
		t.Errorf("CacheSupported=false 不应显示缓存: %q", u2.String())
	}
}
