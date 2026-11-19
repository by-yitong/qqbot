package bot

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/by-yitong/botgo/dto"
)

// c2cStreamThrottle 是相邻两次实际推送的最小间隔（打字机节流；测试可临时调小）。
var c2cStreamThrottle = 500 * time.Millisecond

const (
	streamStateGenerating = 1  // 正文生成中
	streamStateDone       = 10 // 正文生成结束
	streamInputReplace    = "replace"
	streamContentTypeMD   = "markdown"
	streamPushTimeout     = 10 * time.Second
)

// C2CStreamWriter 把「逐段累积的全文」推成 QQ 单聊流式消息（打字机效果）。
// 鸭子类型满足 agent.StreamSink（OnDelta/OnSegmentBreak）；单聊专用，群聊不用。
type C2CStreamWriter struct {
	b           *Bot
	userOpenID  string
	anchorMsgID string // 被动回复锚点（触发消息 id）

	mu          sync.Mutex
	streamMsgID string // 当前段流式消息 id（空=该段未开流）
	lastIndex   int32  // 已成功推送的最大分片序号（首片为 0）
	msgSeq      int32  // 当前段已用的去重序号（从 1 递增）
	pushed      string // 最近一次成功推送的全文（分段收尾内容用）
	lastPush    time.Time
	delivered   bool // 至少一个分片成功送达
	failed      bool // 首片失败：此后全部 no-op，上层改走静态发送
	finished    bool // Finish 成功：此后全部 no-op
}

// NewC2CStreamWriter 创建单聊流式写入器（anchorMsgID 为触发消息 id，被动回复锚点）。
func NewC2CStreamWriter(b *Bot, userOpenID, anchorMsgID string) *C2CStreamWriter {
	return &C2CStreamWriter{b: b, userOpenID: userOpenID, anchorMsgID: anchorMsgID}
}

// OnDelta 节流推送当前段全量文本（懒开流：首次调用才发首片）。
// 被节流跳过的内容不丢：下轮 replace 全量会带上，Finish 必推最终全文。
func (w *C2CStreamWriter) OnDelta(accum string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failed || w.finished || time.Since(w.lastPush) < c2cStreamThrottle {
		return
	}
	if w.streamMsgID == "" {
		rsp, err := w.push(&dto.StreamMessageReq{
			InputMode:   streamInputReplace,
			InputState:  streamStateGenerating,
			ContentType: streamContentTypeMD,
			ContentRaw:  accum,
			MsgID:       w.anchorMsgID,
			MsgSeq:      1,
		})
		if err != nil {
			log.Printf("bot: 流式消息首片发送失败（降级为静态发送）: %v", err)
			w.failed = true
			return
		}
		if rsp != nil {
			w.streamMsgID = rsp.ID
		}
		w.lastIndex, w.msgSeq = 0, 1
	} else {
		idx, seq := w.lastIndex+1, w.msgSeq+1
		if _, err := w.push(&dto.StreamMessageReq{
			InputMode:   streamInputReplace,
			InputState:  streamStateGenerating,
			Index:       idx,
			ContentType: streamContentTypeMD,
			ContentRaw:  accum,
			StreamMsgID: w.streamMsgID,
			MsgSeq:      seq,
		}); err != nil {
			log.Printf("bot: 流式消息续片推送失败（index=%d，继续尝试）: %v", idx, err)
			return
		}
		w.lastIndex, w.msgSeq = idx, seq
	}
	w.pushed = accum
	w.lastPush = time.Now()
	w.delivered = true
}

// OnSegmentBreak 收尾当前流（state=10），下一段开新消息；当前无流时 no-op。
func (w *C2CStreamWriter) OnSegmentBreak() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closeSegment()
}

// Delivered 报告是否至少一个分片成功送达（false=上层应改走静态发送）。
func (w *C2CStreamWriter) Delivered() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.delivered
}

// Finish 以 state=10 收尾并推送最终全文；failed 或从未开流时返回 nil。
func (w *C2CStreamWriter) Finish(finalText string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failed || w.finished || w.streamMsgID == "" {
		return nil
	}
	idx, seq := w.lastIndex+1, w.msgSeq+1
	if _, err := w.push(&dto.StreamMessageReq{
		InputMode:   streamInputReplace,
		InputState:  streamStateDone,
		Index:       idx,
		ContentType: streamContentTypeMD,
		ContentRaw:  finalText,
		StreamMsgID: w.streamMsgID,
		MsgSeq:      seq,
	}); err != nil {
		log.Printf("bot: 流式消息收尾失败: %v", err)
		return err
	}
	w.lastIndex, w.msgSeq = idx, seq
	w.pushed = finalText
	w.lastPush = time.Now()
	w.delivered = true
	w.finished = true
	return nil
}

// Abort 异常路径尽力收尾（等价 OnSegmentBreak，吞错）。
func (w *C2CStreamWriter) Abort() { w.OnSegmentBreak() }

// closeSegment 发分段收尾片（ContentRaw=当前已推送文本）并重置段状态；
// 收尾失败只记日志：分段边界照常生效，下一段开新消息。调用方持锁。
func (w *C2CStreamWriter) closeSegment() {
	if w.failed || w.finished || w.streamMsgID == "" {
		return
	}
	idx, seq := w.lastIndex+1, w.msgSeq+1
	if _, err := w.push(&dto.StreamMessageReq{
		InputMode:   streamInputReplace,
		InputState:  streamStateDone,
		Index:       idx,
		ContentType: streamContentTypeMD,
		ContentRaw:  w.pushed,
		StreamMsgID: w.streamMsgID,
		MsgSeq:      seq,
	}); err != nil {
		log.Printf("bot: 流式消息分段收尾失败（忽略，下一段开新消息）: %v", err)
	}
	w.streamMsgID, w.pushed = "", ""
	w.lastIndex, w.msgSeq = 0, 0
	w.lastPush = time.Now()
}

// push 经 b.openapi() 发一次流式推送；独立 context（流式回调可能在
// agent ctx 即将取消时触发，不能随之失效）。
func (w *C2CStreamWriter) push(req *dto.StreamMessageReq) (*dto.StreamMessageRsp, error) {
	api, err := w.b.openapi()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), streamPushTimeout)
	defer cancel()
	return api.PostStreamC2CMessage(ctx, w.userOpenID, req)
}
