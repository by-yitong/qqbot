package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SessionMaxHistory 是单用户历史的硬上限（条），防止失控增长（压缩后也会被截到该上限内）。
var SessionMaxHistory = 200

// Session 是单个用户的对话历史（内存视图 + 可选 JSONL 落盘）。
//
// 非并发安全：同一 Session 的访问由其内嵌的 sync.Mutex 串行化
// （SessionStore.Get 返回的 *Session，调用方在 agent loop 期间加锁）。
type Session struct {
	sync.Mutex
	userID  string
	file    string // 落盘路径；为空则纯内存
	history []Message
}

// History 返回历史消息（只读视图，调用方不应修改）。
func (s *Session) History() []Message { return s.history }

// Append 追加一条消息（受 SessionMaxHistory 上限）。
func (s *Session) Append(m Message) {
	s.history = append(s.history, m)
	if len(s.history) > SessionMaxHistory {
		s.history = trimLeadingOrphans(s.history[len(s.history)-SessionMaxHistory:])
	}
}

// ReplaceHistory 用给定消息覆盖历史（压缩后使用），同样受上限。
func (s *Session) ReplaceHistory(msgs []Message) {
	if len(msgs) > SessionMaxHistory {
		msgs = trimLeadingOrphans(msgs[len(msgs)-SessionMaxHistory:])
	}
	s.history = msgs
}

// trimLeadingOrphans 丢弃开头的孤立 tool 消息，确保历史不以「引用已缺失 assistant 的
// tool 结果」开头。历史被截断到 SessionMaxHistory 时，可能正好从一批 tool 结果中间切开，
// 留下没有对应 tool_call 的孤儿 tool 消息——部分 OpenAI 兼容 API 会因此报错。
func trimLeadingOrphans(msgs []Message) []Message {
	for len(msgs) > 0 && msgs[0].Role == RoleTool {
		msgs = msgs[1:]
	}
	return msgs
}

// TrimByAge 会话时间衰减：丢弃 Ts 不晚于 cutoff 的旧消息，只保留近期窗口。
// Ts 为零值（升级前的存量数据）一律视为陈旧。被丢弃的消息并不丢失——长期记忆库
// 有全量记录，模型需要旧事时可经 search_history 检索；这里只决定「不占上下文」。
// 切点若落在 tool 批中间，保留部分开头的孤儿 tool 消息一并丢弃（同 trimLeadingOrphans）。
func TrimByAge(msgs []Message, cutoff time.Time) []Message {
	cut := 0
	for cut < len(msgs) && !msgs[cut].Ts.After(cutoff) {
		cut++
	}
	if cut == 0 {
		return msgs
	}
	return trimLeadingOrphans(msgs[cut:])
}

// load 从 JSONL 文件读取历史（文件不存在视为空历史）。坏行跳过。
func (s *Session) load() error {
	if s.file == "" {
		return nil
	}
	data, err := os.ReadFile(s.file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var m Message
		if err := json.Unmarshal(line, &m); err != nil {
			continue // 跳过坏行，保证可用
		}
		s.history = append(s.history, m)
	}
	s.history = trimLeadingOrphans(s.history)
	if len(s.history) > SessionMaxHistory {
		s.history = trimLeadingOrphans(s.history[len(s.history)-SessionMaxHistory:])
	}
	return nil
}

// save 原子写盘：每条消息一行 JSON（JSONL）。
func (s *Session) save() error {
	if s.file == "" {
		return nil
	}
	tmp := s.file + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, m := range s.history {
		if err := enc.Encode(m); err != nil {
			f.Close()
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.file)
}

// SessionStore 管理每用户的会话历史。
//
// 两层锁：s.mu 保护 sessions map；每个 *Session 自带锁串行化同一用户的 agent loop。
type SessionStore struct {
	dir      string // 落盘目录；为空则纯内存
	mu       sync.Mutex
	sessions map[string]*Session
}

// NewSessionStore 创建会话存储。dir 非空时自动创建目录。
func NewSessionStore(dir string) (*SessionStore, error) {
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	return &SessionStore{dir: dir, sessions: map[string]*Session{}}, nil
}

// Get 取（或懒加载并加载磁盘）某用户的会话。
func (s *SessionStore) Get(userID string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[userID]
	if !ok {
		file := ""
		if s.dir != "" {
			file = filepath.Join(s.dir, safeFilename(userID)+".jsonl")
		}
		sess = &Session{userID: userID, file: file}
		_ = sess.load() // 加载失败不致命：用空历史继续
		s.sessions[userID] = sess
	}
	return sess
}

// safeFilename 把任意 userID 规整成安全的文件名（仅保留 [A-Za-z0-9_-]，其余换 _）。
func safeFilename(id string) string {
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "anonymous"
	}
	return b.String()
}
