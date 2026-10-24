package plugins

// progressHint：agent 长任务的「任务进行中」提示消息生命周期。
//
// Run 超过 delay 仍未结束 → 发一条提示（被动回复锚点，不占主动消息额度）；
// Run 结束 → 撤回提示。官方撤回窗口只有 2 分钟，长任务（循环上限默认 240s）会超窗，
// 所以每隔 refresh 滚动换新：先发新提示、再撤旧提示，保证手里那条始终在窗口内可撤回。
// 发送/撤回失败只记日志、不影响任务本身。

import (
	"log"
	"sync"
	"time"
)

// progressHint 描述一次 Run 的提示生命周期；send/recall 由调用方注入（生产是
// c.ReplyText + Bot.DeleteGroupMessage/DeleteC2CMessage，测试注入假实现）。
type progressHint struct {
	send    func() (string, error)   // 发提示，返回消息 id
	recall  func(msgID string) error // 撤回指定消息
	delay   time.Duration            // 开始提示前等待（<=0 禁用）
	refresh time.Duration            // 滚动换新间隔（须 < 2 分钟撤回窗口；<=0 不滚动）
}

// start 启动生命周期，返回 finish——Run 结束（含出错/被打断）时必须调用，负责撤回
// 当前的提示。finish 幂等（重复调用安全）。
func (h *progressHint) start() (finish func()) {
	if h == nil || h.delay <= 0 || h.send == nil || h.recall == nil {
		return func() {}
	}
	done := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	current := "" // 当前在屏提示的消息 id

	finish = func() {
		once.Do(func() {
			close(done)
			mu.Lock()
			id := current
			mu.Unlock()
			if id != "" {
				h.recallLogged(id)
			}
		})
	}

	go func() {
		timer := time.NewTimer(h.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-done:
			return // Run 在 delay 内结束：没发过提示，无需撤回
		}
		if id := h.sendLogged(); id != "" {
			mu.Lock()
			current = id
			mu.Unlock()
			// 竞态兜底：send 期间 Run 可能刚好结束且 finish 读到的还是空 id，
			// 这里再查一次 done，由本 goroutine 撤回刚发出的提示。
			select {
			case <-done:
				mu.Lock()
				current = ""
				mu.Unlock()
				h.recallLogged(id)
				return
			default:
			}
		} else {
			return // 提示发送失败：不再滚动（recall 没意义），等 finish 自然 no-op
		}

		// 滚动换新：先发新、再撤旧，间隔内始终有一条提示在屏。
		if h.refresh <= 0 {
			<-done
			return
		}
		tick := time.NewTicker(h.refresh)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				newID := h.sendLogged()
				if newID == "" {
					continue // 新的没发出去：保留旧的（可能已超窗撤不掉，日志可见）
				}
				mu.Lock()
				old := current
				current = newID
				mu.Unlock()
				if old != "" {
					h.recallLogged(old)
				}
			}
		}
	}()
	return finish
}

// sendLogged 发提示并记日志，返回消息 id（失败返回空）。
func (h *progressHint) sendLogged() string {
	id, err := h.send()
	if err != nil {
		log.Printf("[agent] 发送任务进行中提示失败: %v", err)
		return ""
	}
	return id
}

// recallLogged 撤回并记日志（失败不影响任何流程）。
func (h *progressHint) recallLogged(msgID string) {
	if err := h.recall(msgID); err != nil {
		log.Printf("[agent] 撤回任务进行中提示失败（超 2 分钟窗口或已撤回）: %v", err)
	}
}
