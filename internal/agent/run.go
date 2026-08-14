package agent

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrInterrupted 表示 Run 被主动取消（.stop 或父 ctx 取消）且没有可返回的中间回复。
// 调用方（插件）应静默处理，不作为故障上报。
var ErrInterrupted = errors.New("agent: 已被停止")

// runHandle 记录一次进行中的 Run。
//
//   - cancel 供 Interrupt 取消整个 Run（与 LoopTimeout 走同一条 ctx 取消路径）；
//   - steer 是运行中「补充」的消息（Steer() 入队，Run 在 turn 边界 drain 注入）；
//   - closed 表示 Run 已决定退出、不再接收补充（与 Steer 同锁互斥，
//     保证「已确认入队的消息必然被处理」——Steer 返回 true 就绝不丢失）。
type runHandle struct {
	cancel context.CancelFunc
	done   chan struct{}
	start  time.Time

	mu     sync.Mutex
	steer  []string
	closed bool
}

// drainSteer 取走全部待注入的补充消息（无则返回 nil）。
func (h *runHandle) drainSteer() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.steer) == 0 {
		return nil
	}
	out := h.steer
	h.steer = nil
	return out
}

// pendingSteer 返回当前排队中的补充条数。
func (h *runHandle) pendingSteer() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.steer)
}

// closeIfIdle 尝试关闭 handle：若还有未注入的补充则不关闭、返回 false
// （Run 会继续循环消费它们）；否则原子地置 closed 并返回 true。
// 与 Steer() 在同一把锁下互斥，消灭「刚检查完队列空、退出前又入队了一条」的丢失窗口：
// 只要 Steer 成功入队，Run 这里必然看得到。
func (h *runHandle) closeIfIdle() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || len(h.steer) > 0 {
		return false
	}
	h.closed = true
	return true
}

// close 强制关闭（Run 退出路径统一调用，防泄漏）。
func (h *runHandle) close() {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
}

// Steer 向该会话正在进行的 Run 补充一条消息：不打断在飞的工具，
// 在 turn 边界（一批工具跑完、下次调 LLM 前）作为 user 消息注入。
// 返回 false 表示当前没有可接收的 Run（调用方应走正常 Run 路径排队处理）。
func (a *Agent) Steer(userID, text string) bool {
	h := a.lookupRun(userID)
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	h.steer = append(h.steer, text)
	return true
}

// Interrupt 取消该会话正在进行的 Run（若有）。返回是否有 Run 被取消。
// 不等待 Run 退出（Run 会尽快收尾并 persist 部分历史）；需要确认结束用 WaitIdle。
func (a *Agent) Interrupt(userID string) bool {
	h := a.lookupRun(userID)
	if h == nil {
		return false
	}
	h.cancel()
	return true
}

// WaitIdle 等待该会话的 Run 结束（无 Run 立即返回 true）。超时返回 false。
func (a *Agent) WaitIdle(userID string, timeout time.Duration) bool {
	h := a.lookupRun(userID)
	if h == nil {
		return true
	}
	select {
	case <-h.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// RunInfo 描述一个会话当前的运行状态（.status 命令的数据源）。
type RunInfo struct {
	Running bool      // 是否有进行中的 Run
	Since   time.Time // Run 开始时间（Running 时有意义）
	Pending int       // 排队中的补充消息条数
}

// Status 返回该会话的运行状态。
func (a *Agent) Status(userID string) RunInfo {
	h := a.lookupRun(userID)
	if h == nil {
		return RunInfo{}
	}
	return RunInfo{Running: true, Since: h.start, Pending: h.pendingSteer()}
}

// ResetSession 清空该会话的短期历史（session 落盘一并清空）。
// 只影响 session，不动长期记忆库。若该会话有进行中的 Run，调用方应先 Interrupt+WaitIdle，
// 否则本调用会阻塞到 Run 结束（session 锁）——行为正确但可能久等。
func (a *Agent) ResetSession(userID string) error {
	if a.cfg.Sessions == nil {
		return nil
	}
	sess := a.cfg.Sessions.Get(userID)
	sess.Lock()
	defer sess.Unlock()
	sess.history = nil
	return sess.save()
}

// --- 注册表内部实现 ---

func (a *Agent) lookupRun(userID string) *runHandle {
	a.runsMu.Lock()
	defer a.runsMu.Unlock()
	return a.runs[userID]
}

// startRun 注册一次 Run 的 handle。
func (a *Agent) startRun(userID string, cancel context.CancelFunc) *runHandle {
	h := &runHandle{cancel: cancel, done: make(chan struct{}), start: time.Now()}
	a.runsMu.Lock()
	defer a.runsMu.Unlock()
	a.runs[userID] = h
	return h
}

// endRun 注销 Run：关闭 handle（之后的 Steer 返回 false）、从注册表移除、关闭 done。
func (a *Agent) endRun(userID string, h *runHandle) {
	h.close()
	a.runsMu.Lock()
	if a.runs[userID] == h {
		delete(a.runs, userID)
	}
	a.runsMu.Unlock()
	close(h.done)
}
