package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeCompleter 可脚本化的 Completer 替身：按 queue 依次返回结果或错误。
type fakeCompleter struct {
	results []*LLMResult
	errs    []error
	calls   int
}

func (f *fakeCompleter) Complete(_ context.Context, _ string, _ []Message, _ []map[string]any) (*LLMResult, error) {
	i := f.calls
	f.calls++
	if i < len(f.errs) && f.errs[i] != nil {
		return nil, f.errs[i]
	}
	if i < len(f.results) {
		return f.results[i], nil
	}
	if len(f.results) > 0 {
		return f.results[len(f.results)-1], nil
	}
	return &LLMResult{Content: "ok"}, nil
}

func TestFailoverBreakerAndRecover(t *testing.T) {
	primary := &fakeCompleter{errs: []error{
		errors.New("HTTP 502"), errors.New("HTTP 502"), errors.New("HTTP 502"), // 3 次熔断
	}}
	fallback := &fakeCompleter{results: []*LLMResult{{Content: "备用回答"}}}

	fo := NewFailover(primary, fallback, WithBreakerThreshold(3), WithProbeInterval(10*time.Second))
	defer fo.Close()

	// 前两次失败：直接返回错误、未熔断
	for i := 0; i < 2; i++ {
		if _, err := fo.Complete(context.Background(), "", []Message{NewUser("q")}, nil); err == nil {
			t.Fatal("前两次应返回主模型错误")
		}
		if fo.UsingFallback() {
			t.Fatal("未达阈值不应熔断")
		}
	}
	// 第 3 次失败：熔断 + 本次即用备用补答
	got, err := fo.Complete(context.Background(), "", []Message{NewUser("q")}, nil)
	if err != nil || got.Content != "备用回答" {
		t.Fatalf("第 3 次应熔断并用备用回答: %v %+v", err, got)
	}
	if !fo.UsingFallback() {
		t.Fatal("应处于熔断状态")
	}
	// 熔断后：走备用（主模型不再被调用）
	if _, err := fo.Complete(context.Background(), "", []Message{NewUser("q")}, nil); err != nil {
		t.Fatal(err)
	}
	if primary.calls != 3 {
		t.Fatalf("主模型应只被调 3 次，实际 %d", primary.calls)
	}
}

func TestFailoverSuccessResetsCounter(t *testing.T) {
	primary := &fakeCompleter{errs: []error{errors.New("HTTP 502"), nil}}
	fallback := &fakeCompleter{}
	fo := NewFailover(primary, fallback, WithBreakerThreshold(3), WithProbeInterval(10*time.Second))
	defer fo.Close()

	if _, err := fo.Complete(context.Background(), "", []Message{NewUser("q")}, nil); err == nil {
		t.Fatal("第一次应失败")
	}
	if _, err := fo.Complete(context.Background(), "", []Message{NewUser("q")}, nil); err != nil {
		t.Fatal("第二次应成功")
	}
	// 成功清零：再失败 2 次也不熔断
	primary.errs = append(primary.errs, nil, errors.New("HTTP 502"), errors.New("HTTP 502"))
	for i := 0; i < 3; i++ {
		fo.Complete(context.Background(), "", []Message{NewUser("q")}, nil)
	}
	if fo.UsingFallback() {
		t.Fatal("计数被成功清零，不应熔断")
	}
}

func TestFailoverContentProblemNotCounted(t *testing.T) {
	primary := &fakeCompleter{errs: []error{
		fmtErr(ErrContextOverflow), fmtErr(ErrContextOverflow), fmtErr(ErrContextOverflow),
	}}
	fo := NewFailover(primary, &fakeCompleter{}, WithBreakerThreshold(3), WithProbeInterval(10*time.Second))
	defer fo.Close()
	for i := 0; i < 3; i++ {
		if _, err := fo.Complete(context.Background(), "", []Message{NewUser("q")}, nil); err == nil {
			t.Fatal("内容类错误应原样返回")
		}
	}
	if fo.UsingFallback() {
		t.Fatal("内容类错误不应触发熔断")
	}
}

func fmtErr(err error) error { return err }
