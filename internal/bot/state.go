package bot

import "sync"

// stateStore 按 用户/群（openid 字符串）两个维度保存会话状态，用于多轮对话、计数、记忆等。
type stateStore struct {
	mu     sync.Mutex
	users  map[string]map[string]any
	groups map[string]map[string]any
}

func newStateStore() *stateStore {
	return &stateStore{
		users:  map[string]map[string]any{},
		groups: map[string]map[string]any{},
	}
}

func (s *stateStore) user(id string) *State  { return &State{store: s, scope: 'u', id: id} }
func (s *stateStore) group(id string) *State { return &State{store: s, scope: 'g', id: id} }

// UserState 返回某用户（openid）的会话状态视图。
func (b *Bot) UserState(userID string) *State { return b.state.user(userID) }

// GroupState 返回某群（群 openid）的会话状态视图。
func (b *Bot) GroupState(groupID string) *State { return b.state.group(groupID) }

// State 是绑定到某个 用户/群 命名空间的键值存储视图，线程安全。
type State struct {
	store *stateStore
	scope byte // 'u' = 用户, 'g' = 群
	id    string
}

// bucketLocked 必须在持锁时调用，返回（必要时创建）该命名空间的 map。
func (s *State) bucketLocked() map[string]any {
	if s.scope == 'u' {
		m := s.store.users[s.id]
		if m == nil {
			m = map[string]any{}
			s.store.users[s.id] = m
		}
		return m
	}
	m := s.store.groups[s.id]
	if m == nil {
		m = map[string]any{}
		s.store.groups[s.id] = m
	}
	return m
}

// Set 写入一个值。
func (s *State) Set(key string, val any) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	s.bucketLocked()[key] = val
}

// Get 读取一个值。
func (s *State) Get(key string) (any, bool) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	v, ok := s.bucketLocked()[key]
	return v, ok
}

// Delete 删除一个键。
func (s *State) Delete(key string) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	delete(s.bucketLocked(), key)
}

// Clear 清空该命名空间的全部状态。
func (s *State) Clear() {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if s.scope == 'u' {
		delete(s.store.users, s.id)
	} else {
		delete(s.store.groups, s.id)
	}
}

// GetString 读取字符串值（类型不符或不存在返回 ""）。
func (s *State) GetString(key string) string {
	v, _ := s.Get(key)
	if sv, ok := v.(string); ok {
		return sv
	}
	return ""
}

// GetInt 读取整数值（兼容 int/int64/float64）。
func (s *State) GetInt(key string) int {
	v, _ := s.Get(key)
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// GetBool 读取布尔值。
func (s *State) GetBool(key string) bool {
	v, _ := s.Get(key)
	if bv, ok := v.(bool); ok {
		return bv
	}
	return false
}
