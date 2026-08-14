// Package imgstore 提供进程内的图片暂存（「图床」）。
//
// 用途：生图 API（如 gpt-image-2）常只回 base64，几十万字节不能塞进 LLM 上下文。
// 解决：把生成的图片字节存进本 Store，拿到一个短引用串 "genimg:<id>.<ext>"；
// AI 把这个引用交给 send_image，send_image 经 bot.downloadImage 用 Resolve 取回字节上传腾讯。
//
// 关键：send_image 的 URL 只需被「本进程」能取——QQ 不直接抓 URL（bot 下载字节再上传），
// 所以无需公网暴露/OSS，进程内 map 即可。带 TTL + 容量淘汰，避免内存无限增长。
package imgstore

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Scheme 是引用串前缀。引用形如 "genimg:<id>.<ext>"。
const Scheme = "genimg:"

const (
	defaultTTL = 30 * time.Minute // 单张默认保留时长
	defaultMax = 200              // 默认最多缓存条目，超过淘汰最旧
)

type item struct {
	data        []byte
	contentType string
	filename    string
	createdAt   time.Time
}

// Store 是进程内图片暂存，并发安全。
// SaveDir 非空时，Put 会额外把图片写一份到磁盘（归档用，便于事后查看生成的图）。
type Store struct {
	mu      sync.Mutex
	items   map[string]item
	ttl     time.Duration
	max     int
	SaveDir string // 可选：落盘目录；为空则只在内存
}

var (
	once        sync.Once
	single      *Store
	defaultSave string // Default() 创建时套用的 SaveDir（由 SetSaveDir 设置）
)

// SetSaveDir 设置默认 Store 的落盘目录（须在首次 Default() 前调用）。
// main.go 启动时从 env 读入配置调用。设为空字符串则不落盘。
func SetSaveDir(dir string) { defaultSave = dir }

// Default 返回进程级默认 Store（惰性初始化，带后台 TTL 清理）。
// generate_image（agent 工具）与 downloadImage（bot）都通过它共享同一份缓存。
func Default() *Store {
	once.Do(func() {
		single = New(defaultTTL, defaultMax)
		single.SaveDir = defaultSave
		single.startEvictor()
	})
	return single
}

// New 创建一个 Store（不自启后台清理）。
func New(ttl time.Duration, max int) *Store {
	if ttl <= 0 {
		ttl = defaultTTL
	}
	if max <= 0 {
		max = defaultMax
	}
	return &Store{items: map[string]item{}, ttl: ttl, max: max}
}

// Put 存入图片字节，返回引用串 "genimg:<id>.<ext>"。
// ext 为空默认 png（可带或不带点）。
func (s *Store) Put(data []byte, ext string) string {
	ext = normalizeExt(ext)
	id := randID()
	s.mu.Lock()
	defer s.mu.Unlock()
	// 容量淘汰：达到上限时删最旧一条。
	if len(s.items) >= s.max {
		s.evictOldestLocked()
	}
	s.items[id] = item{
		data:        data,
		contentType: contentTypeForExt(ext),
		filename:    id + "." + ext,
		createdAt:   time.Now(),
	}
	// 可选落盘：归档一份到 SaveDir（best-effort，失败仅记日志，不影响内存存取）。
	if s.SaveDir != "" {
		s.saveToDisk(id+"."+ext, data)
	}
	return Scheme + id + "." + ext
}

// saveToDisk 把图片字节写到 SaveDir/filename（目录不存在则创建）。best-effort。
func (s *Store) saveToDisk(filename string, data []byte) {
	dir := s.SaveDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[imgstore] 创建落盘目录失败 %s: %v", dir, err)
		return
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Printf("[imgstore] 落盘失败 %s: %v", path, err)
	}
}

// Resolve 把引用串解析回字节。非 genimg: 引用、或已过期/不存在，返回 ok=false。
// 返回的 filename 带 id 与扩展名，可直接用于上传。
func (s *Store) Resolve(ref string) (data []byte, contentType, filename string, ok bool) {
	if !strings.HasPrefix(ref, Scheme) {
		return nil, "", "", false
	}
	name := strings.TrimPrefix(ref, Scheme)
	id := name
	if i := strings.IndexByte(name, '.'); i >= 0 {
		id = name[:i]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it, exists := s.items[id]
	if !exists {
		return nil, "", "", false
	}
	return it.data, it.contentType, it.filename, true
}

// Len 返回当前缓存条目数（测试/观察用）。
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

// startEvictor 启动后台 goroutine，周期性清理过期条目。
func (s *Store) startEvictor() {
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for range t.C {
			s.evictExpired()
		}
	}()
}

func (s *Store) evictExpired() {
	cutoff := time.Now().Add(-s.ttl)
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, it := range s.items {
		if it.createdAt.Before(cutoff) {
			delete(s.items, id)
		}
	}
}

// evictOldestLocked 删除最早创建的条目（调用方持锁）。
func (s *Store) evictOldestLocked() {
	var oldestID string
	var oldestT time.Time
	for id, it := range s.items {
		if oldestID == "" || it.createdAt.Before(oldestT) {
			oldestID = id
			oldestT = it.createdAt
		}
	}
	if oldestID != "" {
		delete(s.items, oldestID)
	}
}

// randID 生成 16 个十六进制字符的随机 id。
func randID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// normalizeExt 规范化扩展名：去点、转小写、空则 png。
func normalizeExt(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	ext = strings.TrimPrefix(ext, ".")
	if ext == "" {
		ext = "png"
	}
	return ext
}

// contentTypeForExt 由扩展名推断 MIME，默认 image/png。
func contentTypeForExt(ext string) string {
	switch ext {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "bmp":
		return "image/bmp"
	default:
		return "image/png"
	}
}
