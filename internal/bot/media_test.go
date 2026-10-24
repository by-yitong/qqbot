package bot

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qqbot/internal/imgstore"
)

// TestFetchFile 代下载：内容一致、文件名取 URL 末段、无扩展名兜底 file.bin。
func TestFetchFile(t *testing.T) {
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("APKBYTES"))
	}))
	defer src.Close()

	data, name, err := fetchFile(context.Background(), src.URL+"/GoStudio-v1.0.12.apk?token=1")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "APKBYTES" || name != "GoStudio-v1.0.12.apk" {
		t.Errorf("fetchFile: name=%q data=%q", name, data)
	}
	if _, name, err := fetchFile(context.Background(), src.URL+"/"); err != nil || name != "file.bin" {
		t.Errorf("无文件名应兜底 file.bin: %q %v", name, err)
	}
}

func TestSendGroupVideo(t *testing.T) {
	var gotFileType int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/files") {
			var req struct {
				FileType int    `json:"file_type"`
				URL      string `json:"url"`
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &req)
			gotFileType = req.FileType
			fmt.Fprint(w, `{"file_info":"VINFO"}`)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(srv.Close)

	b := testBot(t, srv)
	if _, err := b.SendGroupVideo(context.Background(), "G", "", "https://x/v.mp4"); err != nil {
		t.Fatalf("视频上传: %v", err)
	}
	if gotFileType != 2 {
		t.Errorf("视频 file_type 应为 2, got %d", gotFileType)
	}
}

func TestSendGroupVoice(t *testing.T) {
	var gotFileType int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/files") {
			var req struct {
				FileType int    `json:"file_type"`
				URL      string `json:"url"`
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &req)
			gotFileType = req.FileType
			fmt.Fprint(w, `{"file_info":"SINFO"}`)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(srv.Close)

	b := testBot(t, srv)
	if _, err := b.SendGroupVoice(context.Background(), "G", "", "https://x/a.silk"); err != nil {
		t.Fatalf("语音上传: %v", err)
	}
	if gotFileType != 3 {
		t.Errorf("语音 file_type 应为 3, got %d", gotFileType)
	}
}

func TestEnsureSilkURLTranscode(t *testing.T) {
	// 源音频：一个 httptest 服务器上的 wav。
	wav := buildTestWAV()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Type", "audio/wav")
		w.Write(wav)
	}))
	defer src.Close()

	// 图床：本地直存到临时目录，BaseURL 随便（本地模式不回源请求）。
	dir := t.TempDir()
	b := &Bot{mediaHost: &imgstore.Host{BaseURL: "http://cdn.example.com", Dir: dir}}

	url, err := b.ensureSilkURL(context.Background(), src.URL+"/voice.wav")
	if err != nil {
		t.Fatalf("ensureSilkURL: %v", err)
	}
	if !strings.HasPrefix(url, "http://cdn.example.com/") || !strings.HasSuffix(url, ".silk") {
		t.Fatalf("应返回图床 .silk URL, got %q", url)
	}
	// 直存文件应为 QQ 头（0x02 开头）的 silk。
	files, _ := filepath.Glob(filepath.Join(dir, "*.silk"))
	if len(files) != 1 {
		t.Fatalf("直存目录应有 1 个 silk, got %v", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 2 || data[0] != 0x02 {
		t.Fatalf("直存文件应以 0x02 开头, got % X", data[:min(4, len(data))])
	}
}

func TestEnsureSilkURLPassthroughAndErrors(t *testing.T) {
	b := &Bot{mediaHost: &imgstore.Host{BaseURL: "http://cdn.example.com", Dir: t.TempDir()}}

	// .silk 后缀（含大写、带查询串）直接透传，不发起下载。
	got, err := b.ensureSilkURL(context.Background(), "https://x/a.SILK?sign=1")
	if err != nil || got != "https://x/a.SILK?sign=1" {
		t.Fatalf("silk 透传: %q %v", got, err)
	}

	// 未配图床 → 明确报错。
	bare := &Bot{}
	if _, err := bare.ensureSilkURL(context.Background(), "https://x/a.mp3"); err == nil {
		t.Fatal("无图床应报错")
	}

	// 源不可达 → 下载失败错误。
	if _, err := b.ensureSilkURL(context.Background(), "http://127.0.0.1:1/a.mp3"); err == nil {
		t.Fatal("下载失败应报错")
	}
}

// buildTestWAV 生成 0.2s 22050Hz 单声道静音偏置正弦 wav（bot 层测试用）。
func buildTestWAV() []byte {
	const rate, secs = 22050, 1
	n := rate * secs
	data := make([]byte, n*2)
	for i := 0; i < n; i++ {
		v := int16(3000 * math.Sin(2*math.Pi*440*float64(i)/rate))
		binary.LittleEndian.PutUint16(data[2*i:], uint16(v))
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint16(1)) // mono
	binary.Write(&b, binary.LittleEndian, uint32(rate))
	binary.Write(&b, binary.LittleEndian, uint32(rate*2))
	binary.Write(&b, binary.LittleEndian, uint16(2))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	return b.Bytes()
}
