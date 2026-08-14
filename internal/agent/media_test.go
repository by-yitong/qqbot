package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type fakeResponder struct {
	gotURL string
	err    error
}

func (f *fakeResponder) SendImage(_ context.Context, url string) error {
	f.gotURL = url
	return f.err
}

func TestSendImageToolNoResponder(t *testing.T) {
	tool := &sendImageTool{}
	out, err := tool.Execute(context.Background(), `{"url":"http://x/a.jpg"}`)
	if err != nil {
		t.Fatalf("无 responder 不应返回 error: %v", err)
	}
	if !strings.Contains(out, "不支持") {
		t.Errorf("无 responder 应提示不支持，got %q", out)
	}
}

func TestSendImageToolWithResponder(t *testing.T) {
	tool := &sendImageTool{}
	r := &fakeResponder{}
	ctx := context.WithValue(context.Background(), responderKey{}, r)
	out, err := tool.Execute(ctx, `{"url":"http://x/a.jpg"}`)
	if err != nil {
		t.Fatal(err)
	}
	if r.gotURL != "http://x/a.jpg" {
		t.Errorf("responder 收到 url=%q want http://x/a.jpg", r.gotURL)
	}
	if !strings.Contains(out, "已发送") {
		t.Errorf("应提示已发送，got %q", out)
	}
}

func TestSendImageToolResponderError(t *testing.T) {
	tool := &sendImageTool{}
	r := &fakeResponder{err: fmt.Errorf("boom")}
	ctx := context.WithValue(context.Background(), responderKey{}, r)
	out, err := tool.Execute(ctx, `{"url":"http://x/a.jpg"}`)
	if err != nil {
		t.Fatal("responder 出错不应返回 error（应作为文本回给模型）")
	}
	if !strings.Contains(out, "发图失败") || !strings.Contains(out, "boom") {
		t.Errorf("应提示发图失败，got %q", out)
	}
}

func TestSendImageToolMissingURL(t *testing.T) {
	tool := &sendImageTool{}
	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Fatal("缺 url 应报错")
	}
}

func TestResponderFromContext(t *testing.T) {
	if r := ResponderFrom(context.Background()); r != nil {
		t.Error("未绑定应返回 nil")
	}
	r := &fakeResponder{}
	ctx := context.WithValue(context.Background(), responderKey{}, r)
	if ResponderFrom(ctx) == nil {
		t.Error("绑定后应能取到")
	}
}
