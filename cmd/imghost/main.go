// 图床服务：接收图片上传，返回公网 URL，供 QQ 机器人按 URL 模式发图。
//
// 为什么需要它：QQ 群/私聊的 /files 富媒体上传，简单 multipart 直传已废（QQ 报 850026
// 「富媒体文件下载失败」），官方只支持「URL 模式」（给 url，QQ 自行下载）和分片上传。
// 本服务提供一个公网可达的图片 URL，让 QQ 按 URL 模式下载。
//
// 部署：放在有公网 IP 的服务器上。建议套 nginx/caddy 走 HTTPS（QQ 下载图片更稳，
// 部分情况下 HTTP 会被拒）。构建后直接跑：
//
//	go build -o imghost ./cmd/imghost
//	./imghost
//
// 配置（env，复制进 systemd unit 或 .env）：
//
//	IMGHOST_LISTEN=:8088                    # 监听地址
//	IMGHOST_BASE_URL=https://img.example.com # 公网基础 URL（构造返回的图片 URL，不要带末尾 /）
//	IMGHOST_TOKEN=changeme                  # 上传鉴权 token（仅 bot 上传用；GET 公开不鉴权）
//	IMGHOST_DIR=./data/imghost              # 存储目录
//	IMGHOST_MAX_BYTES=20971520              # 单文件上限（默认 20MB）
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

var (
	listen   string
	baseURL  string
	token    string
	dir      string
	maxBytes int64
)

func main() {
	listen = envOr("IMGHOST_LISTEN", ":8088")
	baseURL = strings.TrimRight(envOr("IMGHOST_BASE_URL", "http://localhost:8088"), "/")
	token = os.Getenv("IMGHOST_TOKEN")
	dir = envOr("IMGHOST_DIR", "./data/imghost")
	maxBytes = envInt("IMGHOST_MAX_BYTES", 20*1024*1024)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatalf("创建存储目录失败: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/upload", handleUpload)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	// 兜底：按路径后缀分发，容忍反向代理不去前缀（如 nginx location /file 不带尾斜杠时，
	// 请求到服务的路径是 /file/upload、/file/<id>.png——按后缀识别上传，其余按文件名取图）。
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/upload") {
			handleUpload(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/health") {
			_, _ = w.Write([]byte("ok"))
			return
		}
		handleServe(w, r)
	})

	log.Printf("imghost: 监听 %s，公网 base=%s，存储=%s，鉴权=%v，上限=%dB",
		listen, baseURL, dir, token != "", maxBytes)
	srv := &http.Server{Addr: listen, Handler: mux, MaxHeaderBytes: 1 << 20}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// handleUpload 接收原始图片字节，存盘，返回公网 URL。需 Bearer token（若配置了 token）。
func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil {
		http.Error(w, "读取失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(data) == 0 {
		http.Error(w, "空 body", http.StatusBadRequest)
		return
	}
	if int64(len(data)) > maxBytes {
		http.Error(w, fmt.Sprintf("文件超限（>%dB）", maxBytes), http.StatusRequestEntityTooLarge)
		return
	}
	ext := normalizeExt(r.URL.Query().Get("ext"))
	id := randID()
	name := id + "." + ext
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		http.Error(w, "写盘失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	url := baseURL + "/" + name
	log.Printf("[imghost] 上传 %s (%d 字节) → %s", name, len(data), url)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"url": url, "id": id})
}

// handleServe 公开提供图片（QQ 下载用）。GET /<id>.<ext>。防路径穿越。
func handleServe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := filepath.Base(r.URL.Path) // 去掉任何目录成分
	if name == "" || name == "." || name == "/" || !strings.Contains(name, ".") {
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(dir, name)
	// 二次校验：Clean 后必须仍在 dir 内。
	if !isWithin(full, dir) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentTypeForExt(filepath.Ext(name)))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, full)
}

func isWithin(path, base string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func randID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func normalizeExt(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	ext = strings.TrimPrefix(ext, ".")
	switch ext {
	case "png", "jpg", "jpeg", "gif", "webp", "bmp":
		return ext
	default:
		return "png"
	}
}

func contentTypeForExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	default:
		return "application/octet-stream"
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		var n int64
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}
