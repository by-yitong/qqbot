package agent

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

// FailoverClient 是主/备双模型客户端：主模型连续失败达到阈值后熔断切换到
// 备用模型，同时后台周期性探活主模型，恢复后自动切回。
//
//   - 判定「失败」：一次 Complete 返回错误（*LLMClient 内部已对 5xx 做过退避重试，
//     所以这里看到的就是重试后仍失败的调用）。
//   - 熔断阈值：breakerThreshold 次连续失败（ErrContextOverflow/取消类错误不计入——
//     那是请求内容问题，不是模型不可用）。
//   - 探活：每 probeInterval 做一次极小的补全请求；成功即切回主模型。
//   - 正常成功一次即清零失败计数。
//
// 满足 Completer，可直接作为 Config.LLM 使用。
type FailoverClient struct {
	primary  Completer // 主模型
	fallback Completer // 备用模型（nil = 无备用，行为等同 primary）

	breakerThreshold int           // 连续失败几次熔断，默认 3
	probeInterval    time.Duration // 熔断后探活主模型的间隔，默认 3 分钟

	mu            sync.Mutex
	usingFallback bool // 当前是否在用备用模型
	failures      int  // 主模型连续失败计数

	stopOnce sync.Once
	stop     chan struct{}
}

// FailoverOption 配置 FailoverClient。
type FailoverOption func(*FailoverClient)

// WithBreakerThreshold 设置熔断阈值（连续失败次数，<=0 用默认 3）。
func WithBreakerThreshold(n int) FailoverOption {
	return func(f *FailoverClient) {
		if n > 0 {
			f.breakerThreshold = n
		}
	}
}

// WithProbeInterval 设置熔断后的主模型探活间隔（<=0 用默认 3 分钟）。
func WithProbeInterval(d time.Duration) FailoverOption {
	return func(f *FailoverClient) {
		if d > 0 {
			f.probeInterval = d
		}
	}
}

// NewFailover 创建主备客户端并启动探活循环（探测本身极便宜：
// 只在熔断期间进行，每次一条几个 token 的补全）。
func NewFailover(primary, fallback Completer, opts ...FailoverOption) *FailoverClient {
	f := &FailoverClient{
		primary:          primary,
		fallback:         fallback,
		breakerThreshold: 3,
		probeInterval:    3 * time.Minute,
		stop:             make(chan struct{}),
	}
	for _, opt := range opts {
		opt(f)
	}
	go f.probeLoop()
	return f
}

// Close 停止探活循环。
func (f *FailoverClient) Close() {
	f.stopOnce.Do(func() { close(f.stop) })
}

// UsingFallback 返回当前是否在用备用模型。
func (f *FailoverClient) UsingFallback() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.usingFallback
}

// Complete 走当前活跃模型；主模型连续失败达阈值时熔断到备用（本次调用即用备用补答）。
func (f *FailoverClient) Complete(ctx context.Context, system string, msgs []Message, tools []map[string]any) (*LLMResult, error) {
	f.mu.Lock()
	useFallback := f.usingFallback
	f.mu.Unlock()

	if !useFallback {
		result, err := f.primary.Complete(ctx, system, msgs, tools)
		if err == nil {
			f.mu.Lock()
			f.failures = 0
			f.mu.Unlock()
			return result, nil
		}
		if isErrContentProblem(err) {
			return nil, err // 内容/取消类问题：换模型也一样，直接返回
		}
		f.mu.Lock()
		f.failures++
		tripped := f.fallback != nil && f.failures >= f.breakerThreshold
		if tripped {
			f.usingFallback = true
		}
		f.mu.Unlock()
		if !tripped {
			return nil, err
		}
		log.Printf("[llm] 主模型连续失败 %d 次，熔断切换到备用模型", f.failures)
		useFallback = true
	}

	if f.fallback == nil {
		return f.primary.Complete(ctx, system, msgs, tools)
	}
	result, err := f.fallback.Complete(ctx, system, msgs, tools)
	if err != nil {
		// 备用也失败：下次调用先再试一次主模型（可能刚好恢复了），避免双双卡死。
		f.mu.Lock()
		f.usingFallback = false
		f.failures = f.breakerThreshold - 1 // 再挂一次立刻回备用，来回震荡有探活兜底
		f.mu.Unlock()
		return nil, err
	}
	return result, nil
}

// probeLoop 熔断期间周期性探活主模型，恢复即切回。
func (f *FailoverClient) probeLoop() {
	ticker := time.NewTicker(f.probeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-f.stop:
			return
		case <-ticker.C:
		}
		if !f.UsingFallback() {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := f.primary.Complete(ctx, "你是健康探测器。", []Message{NewUser("ping，只回复: pong")}, nil)
		cancel()
		if err != nil {
			log.Printf("[llm] 主模型探活仍失败: %v", err)
			continue
		}
		f.mu.Lock()
		f.usingFallback = false
		f.failures = 0
		f.mu.Unlock()
		log.Printf("[llm] 主模型已恢复，切回主模型")
	}
}

// isErrContentProblem 判断错误是否与模型可用性无关（内容超长/空回复：换模型也一样，
// 不计入熔断，直接返回）。取消/超时走 %w 包装，用 errors.Is 识别。
func isErrContentProblem(err error) bool {
	return errors.Is(err, ErrContextOverflow) || errors.Is(err, ErrEmptyContent)
}
