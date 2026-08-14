package scheduler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// storeFile 是 tasks.json 的整体结构。NextID 自增保证 ID 不复用（删了 t3 再建是 t4，
// 避免旧 ID 与残留 session 文件等错位）。
type storeFile struct {
	NextID int    `json:"next_id"`
	Tasks  []Task `json:"tasks"`
}

// store 负责 tasks.json 的读写（调用方——Scheduler——自行持锁串行化）。
type store struct {
	path string // 空 = 仅内存（不落盘，测试用）
}

// load 读取任务文件：不存在视为空；坏 JSON 时把原文件改名为 .bak 备份后从空开始
// （返回错误仅作告警，不阻塞启动）。
func (st *store) load() (storeFile, error) {
	f := storeFile{NextID: 1}
	if st.path == "" {
		return f, nil
	}
	data, err := os.ReadFile(st.path)
	if err != nil {
		if os.IsNotExist(err) {
			return f, nil
		}
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		_ = os.Rename(st.path, st.path+".bak")
		return storeFile{NextID: 1}, fmt.Errorf("任务文件损坏（已备份为 %s.bak，从空开始）: %w", st.path, err)
	}
	if f.NextID < 1 {
		f.NextID = 1
	}
	return f, nil
}

// save 原子写盘（tmp + rename，仿 session.save）。
func (st *store) save(f storeFile) error {
	if st.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(st.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, st.path)
}
