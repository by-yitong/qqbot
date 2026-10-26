package plugins

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeParser 测试用假解析源：可配置返回结果/错误，统计调用次数；顺序执行下
// 也可直接改字段模拟「上一轮失败、这一轮恢复」。
type fakeParser struct {
	name  string
	res   *VideoResult
	err   error
	block bool // true：挂住直到 ctx 结束（模拟慢/卡死的源，吃满单源超时）

	log *callLog // 可选：跨源记录调用先后顺序（验证冷却重排用）

	mu    sync.Mutex
	calls int
}

func (f *fakeParser) Name() string { return f.name }

func (f *fakeParser) Parse(ctx context.Context, _ string) (*VideoResult, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.log != nil {
		f.log.add(f.name)
	}
	if f.block {
		<-ctx.Done() // 模拟慢源：直到链给的单源超时到期
		return nil, ctx.Err()
	}
	return f.res, f.err
}

func (f *fakeParser) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// callLog 记录跨源的调用先后顺序（并发安全）。
type callLog struct {
	mu sync.Mutex
	v  []string
}

func (l *callLog) add(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.v = append(l.v, name)
}

func (l *callLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.v...)
}

// 1. 首选成功 → 次源不被调用。
func TestParserChainFirstSuccessSkipsRest(t *testing.T) {
	a := &fakeParser{name: "a", res: &VideoResult{Title: "from-a"}}
	b := &fakeParser{name: "b", res: &VideoResult{Title: "from-b"}}
	ch := NewParserChain(a, b)
	res, err := ch.Parse(context.Background(), "https://v.douyin.com/x/")
	if err != nil || res.Title != "from-a" {
		t.Fatalf("首选成功应直接返回: %v %+v", err, res)
	}
	if b.callCount() != 0 {
		t.Errorf("首选成功后次源不应被调用，b 被调了 %d 次", b.callCount())
	}
}

// 2. 首败次成 → 返回次源结果、无错误。
func TestParserChainFailoverToNext(t *testing.T) {
	a := &fakeParser{name: "a", err: errors.New("源 a 挂了")}
	b := &fakeParser{name: "b", res: &VideoResult{Title: "from-b"}}
	ch := NewParserChain(a, b)
	res, err := ch.Parse(context.Background(), "https://v.douyin.com/x/")
	if err != nil {
		t.Fatalf("首败次成应成功: %v", err)
	}
	if res.Title != "from-b" {
		t.Errorf("应返回次源结果: %+v", res)
	}
}

// 3. 全败 → 错误聚合两个源名；首源连续失败计数达到阈值后进冷却（顺序被重排）。
func TestParserChainAllFailAggregatesAndCooldownOrder(t *testing.T) {
	log := &callLog{}
	a := &fakeParser{name: "a", err: errors.New("a 挂了"), log: log}
	b := &fakeParser{name: "b", res: &VideoResult{Title: "from-b"}, log: log}
	ch := NewParserChain(a, b)
	ch.failThreshold = 2
	ctx := context.Background()

	// 第 1、2 轮：a 连续失败（计到阈值进冷却），b 成功兜住。
	for i := 0; i < 2; i++ {
		res, err := ch.Parse(ctx, "https://v.douyin.com/x/")
		if err != nil || res.Title != "from-b" {
			t.Fatalf("第 %d 轮应由 b 兜住: %v %+v", i+1, err, res)
		}
	}
	// 第 3 轮：a 已冷却（健康轮不再优先）、b 也失败 → 全败，错误聚合两个源。
	b.err, b.res = errors.New("b 也挂了"), nil
	_, err := ch.Parse(ctx, "https://v.douyin.com/x/")
	if err == nil || !strings.Contains(err.Error(), "a:") || !strings.Contains(err.Error(), "b:") {
		t.Fatalf("全败应聚合两个源的错误: %v", err)
	}
	if !strings.Contains(err.Error(), "；") {
		t.Errorf("聚合错误应以「；」连接: %v", err)
	}
	// 调用顺序：前两轮 a,b｜a,b；第 3 轮 b 在前——证明 a 因连续失败被移出健康优先序。
	want := []string{"a", "b", "a", "b", "b", "a"}
	if got := log.snapshot(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("调用顺序 = %v, want %v", got, want)
	}
}

// 4. 不支持平台不计失败：源 A 恒 Unsupported、源 B 成功；连打 3 轮 A 也不进冷却，
// 后续轮次 A 仍按健康序首个被试。
func TestParserChainUnsupportedNotCountedAsFailure(t *testing.T) {
	a := &fakeParser{name: "a", err: ErrUnsupportedPlatform}
	b := &fakeParser{name: "b", res: &VideoResult{Title: "from-b"}}
	ch := NewParserChain(a, b)
	ch.failThreshold = 3 // 若 Unsupported 被误计失败，3 轮正好触发冷却
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		res, err := ch.Parse(ctx, "https://example.com/x")
		if err != nil || res.Title != "from-b" {
			t.Fatalf("第 %d 轮应由 b 成功: %v %+v", i+1, err, res)
		}
	}
	// 第 4 轮让 a 成功：若 a 在冷却，会被排到 b 之后（b 先成功则 a 根本不被调）；
	// a 仍健康时应首个被试并直接成功。
	a.res, a.err = &VideoResult{Title: "from-a"}, nil
	bCalls := b.callCount()
	res, err := ch.Parse(ctx, "https://example.com/x")
	if err != nil || res.Title != "from-a" {
		t.Fatalf("不支持平台不应累计失败，a 应仍健康: %v %+v", err, res)
	}
	if b.callCount() != bCalls {
		t.Error("a 首个成功后 b 不应再被调用")
	}
	// 全链都不认识该链接 → 专用错误（而非聚合为空）。
	a.res, a.err = nil, ErrUnsupportedPlatform
	b.res, b.err = nil, ErrUnsupportedPlatform
	if _, err := ch.Parse(ctx, "https://example.com/x"); err == nil || !strings.Contains(err.Error(), "没有解析源支持该链接") {
		t.Errorf("全 Unsupported 应报没有源支持: %v", err)
	}
}

// 5. 冷却与兜底：A 连续失败进冷却后不被优先尝试；健康源失败时 A 作兜底可救回并解封，
// 解封后 A 恢复正常优先序。
func TestParserChainCooldownFallbackAndRecovery(t *testing.T) {
	log := &callLog{}
	a := &fakeParser{name: "a", err: errors.New("a 挂了"), log: log}
	b := &fakeParser{name: "b", res: &VideoResult{Title: "from-b"}, log: log}
	ch := NewParserChain(a, b)
	ch.failThreshold = 2
	ch.cooldown = time.Minute // 测试全程毫秒级，远短于冷却，无需等过期
	ctx := context.Background()

	// 第 1、2 轮：a 连续失败 2 次进冷却，b 成功兜住。
	for i := 0; i < 2; i++ {
		res, err := ch.Parse(ctx, "https://v.douyin.com/x/")
		if err != nil || res.Title != "from-b" {
			t.Fatalf("第 %d 轮应由 b 兜住: %v %+v", i+1, err, res)
		}
	}
	// a 冷却中：健康轮只剩 b；b 成功时 a 完全不被尝试（调用计数不增）。
	aCalls := a.callCount()
	if res, err := ch.Parse(ctx, "https://v.douyin.com/x/"); err != nil || res.Title != "from-b" {
		t.Fatalf("冷却中 a 不参与健康轮: %v", err)
	}
	if a.callCount() != aCalls {
		t.Fatalf("b 成功时冷却中的 a 不应被调用: %d -> %d", aCalls, a.callCount())
	}
	// b 失败 → 冷却中的 a 作兜底被试，成功 → 救回且解封（冷却状态清零）。
	b.err, b.res = errors.New("b 也挂了"), nil
	a.res, a.err = &VideoResult{Title: "from-a"}, nil
	if res, err := ch.Parse(ctx, "https://v.douyin.com/x/"); err != nil || res.Title != "from-a" {
		t.Fatalf("冷却中的 a 应作为兜底救回: %v %+v", err, res)
	}
	// 解封后 a 恢复健康优先序：下一轮 a 首个被试、成功后 b 不被调。
	bCalls, aCalls := b.callCount(), a.callCount()
	if res, err := ch.Parse(ctx, "https://v.douyin.com/x/"); err != nil || res.Title != "from-a" {
		t.Fatalf("解封后 a 应正常参与: %v %+v", err, res)
	}
	if a.callCount() != aCalls+1 || b.callCount() != bCalls {
		t.Errorf("解封后 a 应首个被试且 b 不被调: a %d->%d, b %d->%d",
			aCalls, a.callCount(), bCalls, b.callCount())
	}
	// 全程顺序：a,b｜a,b｜b｜b,a（b 败后 a 兜底）｜a（恢复优先序）。
	want := []string{"a", "b", "a", "b", "b", "b", "a", "a"}
	if got := log.snapshot(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("调用顺序 = %v, want %v", got, want)
	}
}

// 6. 单源超时：把链的单源超时调到 10ms，慢源（阻塞到 ctx 结束）记一次失败，
// 链落到下一源成功；下一轮慢源已在冷却（顺序变为 b,a 证明超时被记为失败）。
func TestParserChainSourceTimeoutFailsOver(t *testing.T) {
	log := &callLog{}
	a := &fakeParser{name: "a", block: true, log: log}
	b := &fakeParser{name: "b", res: &VideoResult{Title: "from-b"}, log: log}
	ch := NewParserChain(a, b)
	ch.timeout = 10 * time.Millisecond
	ch.failThreshold = 1 // 一次超时即进冷却，便于下一轮观察重排
	ctx := context.Background()

	res, err := ch.Parse(ctx, "https://v.douyin.com/x/")
	if err != nil || res.Title != "from-b" {
		t.Fatalf("慢源超时后应落到 b: %v %+v", err, res)
	}
	if a.callCount() != 1 {
		t.Fatalf("a 应被尝试 1 次: %d", a.callCount())
	}
	// 第 2 轮：a 已因超时进冷却，b 失败后 a 才作兜底——顺序 b,a 证明超时记了失败。
	b.err, b.res = errors.New("b 挂了"), nil
	if _, err := ch.Parse(ctx, "https://v.douyin.com/x/"); err == nil {
		t.Fatal("全败应报错")
	}
	if got := log.snapshot(); strings.Join(got, ",") != "a,b,b,a" {
		t.Errorf("调用顺序 = %v, want [a b b a]", got)
	}
}

// 7. 零源链：未配置任何解析源。
func TestParserChainEmpty(t *testing.T) {
	ch := NewParserChain()
	_, err := ch.Parse(context.Background(), "https://v.douyin.com/x/")
	if err == nil || !strings.Contains(err.Error(), "未配置") {
		t.Fatalf("零源应报未配置: %v", err)
	}
}

// 8. orderParsers：空 spec 原序全量（跳过 nil）；非空按 spec 重排取子集，
// 未知名忽略、未列出的源丢弃。
func TestOrderParsers(t *testing.T) {
	a := &fakeParser{name: "a"}
	b := &fakeParser{name: "b"}
	c := &fakeParser{name: "c"}
	names := func(ps []Parser) string {
		var ss []string
		for _, p := range ps {
			ss = append(ss, p.Name())
		}
		return strings.Join(ss, ",")
	}
	if got := names(orderParsers([]Parser{a, nil, b, c}, "")); got != "a,b,c" {
		t.Errorf("空 spec 应过滤 nil 后原序全量: %s", got)
	}
	if got := names(orderParsers([]Parser{a, b, c}, "b,a")); got != "b,a" {
		t.Errorf("spec 应按给定顺序取子集: %s", got)
	}
	if got := names(orderParsers([]Parser{a, b, c}, "b,x,a")); got != "b,a" {
		t.Errorf("spec 里未知名应忽略: %s", got)
	}
	if got := names(orderParsers([]Parser{a, b, c}, "c")); got != "c" {
		t.Errorf("未列出的源应丢弃: %s", got)
	}
	if ps := orderParsers([]Parser{nil}, ""); len(ps) != 0 {
		t.Errorf("nil 应始终跳过: %v", ps)
	}
	if ps := orderParsers([]Parser{nil, a}, "a"); len(ps) != 1 || ps[0].Name() != "a" {
		t.Errorf("spec 模式下 nil 也应跳过: %v", ps)
	}
}
