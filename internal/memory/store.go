// Package memory 提供聊天记录的长期存储（SQLite/GORM）与检索。
//
// 分工：agent 的 session 是「bot 参与的短期记忆」（压缩式）；本包是「全量消息的长期记忆」——
// 群里所有消息（含未 @bot 的）与私聊消息全部落库，供两处消费：
//  1. search_history 工具：agent 按需深查（关键词/说话人/时间范围），群隔离。
//  2. 近期注入：每次 @bot 进 Run 时，把最近一段时间的群聊动态拼进 system prompt（不落 session）。
//
// 驱动用 glebarez/sqlite（纯 Go，无 cgo）。写入走异步队列（带缓冲 channel + 单写 goroutine），
// 消息洪峰不挡主流程；MsgID 唯一约束去重（QQ 重连可能重投同一条消息）。
package memory

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Message 是一条落库的聊天消息。
type Message struct {
	ID       uint      `gorm:"primaryKey"`
	MsgID    string    `gorm:"uniqueIndex;size:128"` // QQ 消息 id（去重）；bot 回复可用合成 id
	Ts       time.Time `gorm:"index"`                // 消息时间
	Source   string    `gorm:"size:16"`              // group / c2c
	GroupID  string    `gorm:"size:64;index:idx_g_ts,priority:1;index:idx_g_user,priority:1"`
	UserID   string    `gorm:"size:64;index:idx_g_user,priority:2"`
	Username string    `gorm:"size:128"` // 发送者当时昵称（快照，改名不追溯）
	Content  string
	AtBot    bool // 是否 @bot / 进过 agent
}

// TableName 表名。
func (Message) TableName() string { return "messages" }

// LLMUsage 是一次 LLM 调用的 token 用量（.usage 统计的数据源）。
type LLMUsage struct {
	ID               uint      `gorm:"primaryKey"`
	Ts               time.Time `gorm:"index"`
	Day              string    `gorm:"size:10;index"` // 本地日期 YYYY-MM-DD（按天聚合）
	Model            string    `gorm:"size:64"`
	PromptTokens     int64     // 输入 token
	CompletionTokens int64     // 输出 token
	TotalTokens      int64
	CachedTokens     int64 // 缓存命中的输入 token（CacheSupported=false 时无意义）
	CacheSupported   bool  // 网关是否报告缓存命中
	DurationMs       int64 // 调用耗时
}

// TableName 表名。
func (LLMUsage) TableName() string { return "llm_usages" }

// Scope 是查询范围（群隔离的硬约束）：群聊按群，私聊按用户。
type Scope struct {
	Source  string // "group" / "c2c"
	GroupID string // 群 openid（Source=group 时必填）
	UserID  string // 用户 openid（Source=c2c 时必填）
}

// Store 是聊天记录存储：GORM/SQLite + 异步写入。
type Store struct {
	db      *gorm.DB
	ch      chan Message
	usageCh chan LLMUsage
	wg      sync.WaitGroup
	mu      sync.Mutex // 保护 closed
	cl      bool
}

// Open 打开（或创建）数据库并迁移表。dsn 是 sqlite 文件路径，如 ./data/memory.db。
func Open(dsn string) (*Store, error) {
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormLogger,
	})
	if err != nil {
		return nil, fmt.Errorf("打开记忆库失败: %w", err)
	}
	// sqlite 并发写会锁库：单连接 + WAL，配合异步单写 goroutine。
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("设置 WAL 失败: %w", err)
	}
	if err := db.AutoMigrate(&Message{}, &LLMUsage{}); err != nil {
		return nil, fmt.Errorf("迁移记忆表失败: %w", err)
	}
	s := &Store{db: db, ch: make(chan Message, 1024), usageCh: make(chan LLMUsage, 256)}
	s.wg.Add(1)
	go s.writeLoop()
	return s, nil
}

// Record 异步落一条消息（缓冲满则丢弃并记日志，绝不阻塞调用方）。
// MsgID 重复时静默忽略（去重）。
func (s *Store) Record(m Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cl {
		return
	}
	select {
	case s.ch <- m:
	default:
		// 队列满：丢这条（消息洪峰时保主流程），可接受的自用取舍。
	}
}

// RecordUsage 异步落一条 LLM 用量记录（非阻塞，缓冲满则丢弃）。
func (s *Store) RecordUsage(u LLMUsage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cl {
		return
	}
	select {
	case s.usageCh <- u:
	default:
	}
}

// writeLoop 单 goroutine 批量写库（串行化 sqlite 写，避免锁库）。
// 两类写入共用一个循环；channel 关闭后置 nil（select 永久阻塞该分支），
// 两个都关了才退出。
func (s *Store) writeLoop() {
	defer s.wg.Done()
	msgs := make([]Message, 0, 64)
	usages := make([]LLMUsage, 0, 32)
	flush := func() {
		if len(msgs) > 0 {
			if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&msgs).Error; err != nil {
				// 记日志即可：落库失败不致命（丢一段记录，不影响主流程）。
				logPrintf("记忆落库失败: %v", err)
			}
			msgs = msgs[:0]
		}
		if len(usages) > 0 {
			if err := s.db.Create(&usages).Error; err != nil {
				logPrintf("用量落库失败: %v", err)
			}
			usages = usages[:0]
		}
	}
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for s.ch != nil || s.usageCh != nil {
		select {
		case m, ok := <-s.ch:
			if !ok {
				s.ch = nil
				continue
			}
			msgs = append(msgs, m)
			if len(msgs) >= 64 {
				flush()
			}
		case u, ok := <-s.usageCh:
			if !ok {
				s.usageCh = nil
				continue
			}
			usages = append(usages, u)
			if len(usages) >= 64 {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
	flush()
}

// Close 停止写入并落盘剩余消息。
func (s *Store) Close() {
	s.mu.Lock()
	if s.cl {
		s.mu.Unlock()
		return
	}
	s.cl = true
	close(s.ch)
	close(s.usageCh)
	s.mu.Unlock()
	s.wg.Wait()
}

// SearchQuery 是历史检索条件。
type SearchQuery struct {
	Query string        // 关键词（子串匹配；空=不按内容过滤）
	User  string        // 说话人（昵称或 openid 子串；空=不过滤）
	Since time.Duration // 时间范围（0=不限）
	Limit int           // 返回条数上限（默认 20，上限 50）
}

// Search 按 scope（群/私聊隔离）检索历史，时间正序返回。
func (s *Store) Search(sc Scope, q SearchQuery) ([]Message, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	db := s.scopeDB(sc)
	if kw := strings.TrimSpace(q.Query); kw != "" {
		db = db.Where("content LIKE ?", "%"+kw+"%")
	}
	if u := strings.TrimSpace(q.User); u != "" {
		db = db.Where("username LIKE ? OR user_id LIKE ?", "%"+u+"%", "%"+u+"%")
	}
	if q.Since > 0 {
		db = db.Where("ts >= ?", time.Now().Add(-q.Since))
	}
	var msgs []Message
	err := db.Order("ts ASC").Limit(limit).Find(&msgs).Error
	return msgs, err
}

// Recent 取 scope 内最近 window 时长（最多 max 条）的消息，时间正序。
func (s *Store) Recent(sc Scope, window time.Duration, max int) ([]Message, error) {
	if max <= 0 {
		max = 30
	}
	var msgs []Message
	err := s.scopeDB(sc).
		Where("ts >= ?", time.Now().Add(-window)).
		Order("ts ASC").Limit(max).Find(&msgs).Error
	return msgs, err
}

// RecentAfter 取 scope 内 ts 严格大于 since 的消息（时间正序，最多 max 条）。
// 供「增量注入」：上一轮注入点之后的新消息（空结果 = 期间没人说话，可不注入）。
func (s *Store) RecentAfter(sc Scope, since time.Time, max int) ([]Message, error) {
	if max <= 0 {
		max = 30
	}
	var msgs []Message
	err := s.scopeDB(sc).
		Where("ts > ?", since).
		Order("ts ASC").Limit(max).Find(&msgs).Error
	return msgs, err
}

// scopeDB 按 Scope 加隔离过滤（硬约束：群查群、私聊查该用户，跨会话不可查）。
func (s *Store) scopeDB(sc Scope) *gorm.DB {
	switch sc.Source {
	case "group":
		return s.db.Where("source = ? AND group_id = ?", "group", sc.GroupID)
	default: // c2c
		return s.db.Where("source = ? AND user_id = ?", "c2c", sc.UserID)
	}
}

// --- ctx 注入（search_history 工具经它拿到当前会话的查询范围）---

type scopeKey struct{}

// WithScope 把查询范围绑定进 ctx（agent.Run 的调用方注入）。
func WithScope(ctx context.Context, sc Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, sc)
}

// ScopeFrom 从 ctx 取查询范围（未绑定返回 ok=false）。
func ScopeFrom(ctx context.Context) (Scope, bool) {
	sc, ok := ctx.Value(scopeKey{}).(Scope)
	return sc, ok
}

// FormatMessages 把消息格式化成给 LLM 看的文本行（[08-14 12:00] 昵称: 内容）。
func FormatMessages(msgs []Message) string {
	var b strings.Builder
	for _, m := range msgs {
		name := m.Username
		if name == "" {
			name = m.UserID
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n", m.Ts.Format("01-02 15:04"), name, strings.ReplaceAll(m.Content, "\n", " "))
	}
	return strings.TrimRight(b.String(), "\n")
}
