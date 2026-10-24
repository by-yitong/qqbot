package plugins

// progressHint 生命周期测试：短任务不发、长任务发+撤回、超窗滚动换新、finish 幂等、
// 发送失败降级、禁用 no-op。全部用注入的假 send/recall，不触网。

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeHint 记录发送/撤回的消息 id 序列。
type fakeHint struct {
	mu      sync.Mutex
	sends   []string
	recalls []string
	failNth int // 第 n 次发送失败（1 起，0=从不失败）
	n       int
}

func (f *fakeHint) send() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	if f.failNth == f.n {
		return "", fmt.Errorf("模拟发送失败")
	}
	id := fmt.Sprintf("m%d", f.n)
	f.sends = append(f.sends, id)
	return id, nil
}

func (f *fakeHint) recall(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recalls = append(f.recalls, id)
	return nil
}

func (f *fakeHint) snapshot() (sends, recalls []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.sends...), append([]string{}, f.recalls...)
}

func TestProgressHintShortTask(t *testing.T) {
	f := &fakeHint{}
	h := &progressHint{send: f.send, recall: f.recall, delay: 50 * time.Millisecond, refresh: time.Minute}
	finish := h.start()
	time.Sleep(10 * time.Millisecond) // delay 内结束
	finish()
	time.Sleep(80 * time.Millisecond) // 给可能误启动的 goroutine 时间
	sends, recalls := f.snapshot()
	if len(sends) != 0 || len(recalls) != 0 {
		t.Fatalf("delay 内结束不应发/撤提示，sends=%v recalls=%v", sends, recalls)
	}
}

func TestProgressHintLongTask(t *testing.T) {
	f := &fakeHint{}
	h := &progressHint{send: f.send, recall: f.recall, delay: 10 * time.Millisecond, refresh: time.Minute}
	finish := h.start()
	time.Sleep(50 * time.Millisecond)
	sends, _ := f.snapshot()
	if len(sends) != 1 {
		t.Fatalf("应只发一条提示，sends=%v", sends)
	}
	finish()
	time.Sleep(30 * time.Millisecond)
	sends, recalls := f.snapshot()
	if len(sends) != 1 || len(recalls) != 1 || recalls[0] != sends[0] {
		t.Fatalf("结束应撤回该提示，sends=%v recalls=%v", sends, recalls)
	}
	// finish 幂等：重复调用不再撤回。
	finish()
	time.Sleep(20 * time.Millisecond)
	_, recalls = f.snapshot()
	if len(recalls) != 1 {
		t.Fatalf("finish 应幂等，recalls=%v", recalls)
	}
}

func TestProgressHintRefresh(t *testing.T) {
	f := &fakeHint{}
	h := &progressHint{send: f.send, recall: f.recall, delay: 10 * time.Millisecond, refresh: 20 * time.Millisecond}
	finish := h.start()
	time.Sleep(75 * time.Millisecond) // 覆盖 ≥2 次 refresh
	finish()
	time.Sleep(30 * time.Millisecond)
	sends, recalls := f.snapshot()
	if len(sends) < 3 {
		t.Fatalf("滚动换新应至少发 3 条（初始+2 次换新），sends=%v", sends)
	}
	// 结束时：除最后一条外的全部已撤回（换新撤旧 + finish 撤最新），且最新那条被撤。
	if len(recalls) != len(sends) {
		t.Fatalf("每条提示都应被撤回，sends=%v recalls=%v", sends, recalls)
	}
	last := sends[len(sends)-1]
	if recalls[len(recalls)-1] != last {
		t.Fatalf("最后撤回的应是最新提示 %s，recalls=%v", last, recalls)
	}
}

func TestProgressHintSendFailAndDisabled(t *testing.T) {
	// 首次发送失败：不滚动、无撤回、不 panic。
	f := &fakeHint{failNth: 1}
	h := &progressHint{send: f.send, recall: f.recall, delay: 10 * time.Millisecond, refresh: 20 * time.Millisecond}
	finish := h.start()
	time.Sleep(60 * time.Millisecond)
	finish()
	time.Sleep(30 * time.Millisecond)
	sends, recalls := f.snapshot()
	if len(sends) != 0 || len(recalls) != 0 {
		t.Fatalf("发送失败后不应有发送/撤回记录，sends=%v recalls=%v", sends, recalls)
	}

	// delay<=0：禁用，finish 是 no-op。
	f2 := &fakeHint{}
	h2 := &progressHint{send: f2.send, recall: f2.recall, delay: 0}
	finish2 := h2.start()
	time.Sleep(20 * time.Millisecond)
	finish2()
	sends, _ = f2.snapshot()
	if len(sends) != 0 {
		t.Fatalf("禁用时应无发送，sends=%v", sends)
	}

	// 零值/nil 安全。
	var h3 *progressHint
	h3.start()()
}
