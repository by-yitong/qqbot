// 短视频解析源抽象与链式故障转移。
// 解析源历史上已换过 4 代（直连→星之阁→zsz→luoying），单源一旦失效整个功能就挂；
// 抽象出统一 Parser 接口后，VideoPlugin 只认 ParserChain：按优先级逐个试，
// 单源连续失败自动冷却、健康源全挂时冷却源兜底，以后加源 = 加一个文件 + 装配处一行。
package plugins

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// Parser 一个短视频解析源。实现方自带配置，按链接域名自行分流平台；
// 不认识的平台返回 ErrUnsupportedPlatform（链会跳到下一个源）。
type Parser interface {
	Name() string // 源名（日志/聚合报错/env 排序用），如 "luoying"
	Parse(ctx context.Context, link string) (*VideoResult, error)
}

// ErrUnsupportedPlatform 表示该源不认识/不解析此链接。与「源故障」区分开：
// 不支持不算失败、不进错误聚合，只是把机会让给后面的源。
var ErrUnsupportedPlatform = errors.New("不支持的平台")

// ParserChain 按优先级组织的解析源链，带单源失败计数与冷却，并发安全。
type ParserChain struct {
	parsers []Parser

	// 可调参数（默认值面向生产；测试嫌慢可同包直接改小，避免真等 10 分钟）。
	timeout       time.Duration // 单源解析超时，超时算该源失败
	failThreshold int           // 连续失败多少次进入冷却
	cooldown      time.Duration // 冷却时长

	mu    sync.Mutex
	fails map[string]int       // 源名 -> 当前连续失败次数（成功清零）
	until map[string]time.Time // 源名 -> 冷却截止时间（过期即视为健康）
}

// NewParserChain 创建解析链。传入顺序即优先级顺序；nil 源会被跳过
// （NewXxxFromEnv 未配置时返回 nil，装配处无需判空）。
func NewParserChain(parsers ...Parser) *ParserChain {
	kept := make([]Parser, 0, len(parsers))
	for _, p := range parsers {
		if p != nil {
			kept = append(kept, p)
		}
	}
	return &ParserChain{
		parsers:       kept,
		timeout:       60 * time.Second,
		failThreshold: 3,
		cooldown:      10 * time.Minute,
		fails:         make(map[string]int),
		until:         make(map[string]time.Time),
	}
}

// Parse 解析链接：先按优先级试健康源，全失败后再试冷却中的源兜底（可能已恢复）。
// 全部失败时聚合各源错误返回；所有源都不认识该平台时返回「没有解析源支持该链接」。
func (ch *ParserChain) Parse(ctx context.Context, link string) (*VideoResult, error) {
	if len(ch.parsers) == 0 {
		return nil, errors.New("未配置任何视频解析源（如 LUOYING_API_KEY）")
	}
	var parts []string
	for _, p := range ch.ordered() {
		if err := ctx.Err(); err != nil { // 外层已取消/超时，别再拖下一个源
			return nil, err
		}
		res, err := ch.tryOne(ctx, p, link)
		if err == nil {
			return res, nil
		}
		if errors.Is(err, ErrUnsupportedPlatform) {
			continue // 该源不认识此链接：不算失败、不进聚合，让给下一源
		}
		parts = append(parts, p.Name()+": "+err.Error())
	}
	if len(parts) == 0 { // 走到这说明所有源都返回了 Unsupported
		return nil, errors.New("没有解析源支持该链接")
	}
	return nil, errors.New(strings.Join(parts, "；"))
}

// ordered 计算本轮尝试顺序：健康源按优先级序在前，冷却中的源殿后兜底。
func (ch *ParserChain) ordered() []Parser {
	now := time.Now()
	ch.mu.Lock()
	defer ch.mu.Unlock()
	var healthy, cooling []Parser
	for _, p := range ch.parsers {
		if now.Before(ch.until[p.Name()]) {
			cooling = append(cooling, p)
		} else {
			healthy = append(healthy, p)
		}
	}
	return append(healthy, cooling...)
}

// tryOne 带单源超时跑一个源，并把结果记入失败计数/冷却状态。
func (ch *ParserChain) tryOne(ctx context.Context, p Parser, link string) (*VideoResult, error) {
	ctx, cancel := context.WithTimeout(ctx, ch.timeout)
	defer cancel()
	res, err := p.Parse(ctx, link)
	if err == nil {
		ch.reset(p.Name())
		return res, nil
	}
	if errors.Is(err, ErrUnsupportedPlatform) {
		return nil, err // 不支持 ≠ 失败，状态不动
	}
	ch.recordFail(p.Name())
	return nil, err
}

// recordFail 记一次失败；连续失败达阈值 → 进冷却并清零计数
// （冷却期结束后从零重新累计，避免一次冷却判过期后立刻又触发）。
func (ch *ParserChain) recordFail(name string) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.fails[name]++
	if ch.fails[name] >= ch.failThreshold {
		ch.until[name] = time.Now().Add(ch.cooldown)
		ch.fails[name] = 0
	}
}

// reset 源成功：清零失败计数并解除冷却（兜底尝试成功说明源已恢复）。
func (ch *ParserChain) reset(name string) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	delete(ch.fails, name)
	delete(ch.until, name)
}

// orderParsers 按 VIDEO_PARSERS 的值挑选/重排解析源：
// 空 spec → 过滤 nil 后按原序全量返回；非空（逗号分隔源名）→ 按 spec 顺序
// 选出对应源，用于临时下线某源或手动调优先级（spec 里未知名忽略，未列出的源丢弃）。
func orderParsers(all []Parser, spec string) []Parser {
	var pool []Parser
	for _, p := range all {
		if p != nil { // 未配置（如缺 API key）的源在这里统一剔除
			pool = append(pool, p)
		}
	}
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return pool
	}
	byName := make(map[string]Parser, len(pool))
	for _, p := range pool {
		byName[p.Name()] = p
	}
	var out []Parser
	for _, name := range strings.Split(spec, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if p, ok := byName[name]; ok {
			out = append(out, p)
			delete(byName, name) // spec 重复名字只装一次
		}
	}
	return out
}
