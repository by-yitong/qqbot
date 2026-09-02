// Package audio 把常见音频格式（mp3/wav）转成 QQ 语音消息可用的 silk 字节。
//
// QQ 官方机器人语音要求 silk（QQ 客户端原生格式）：0x02 头、无 EOF 标记，
// 24kHz 单声道已覆盖人声频段。编码用纯 Go 的 silk-go（GPLv3，进程内、无外部依赖，
// Tencent 选项即输出 QQ/微信头），mp3 解码用 go-mp3（其输出恒为 16bit 小端双声道交错）。
package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"

	mp3dec "github.com/hajimehoshi/go-mp3"
	"github.com/rolandhe/silk-go/pkg/silksdk"
)

// QQ 语音目标参数。silk 内部采样率上限即 24kHz；25kbps 对人声够用且体积小。
const (
	silkSampleRate = 24000
	silkBitRate    = 25000
)

// ToSilk 把音频字节转成 QQ 可播放的 silk（0x02 头、24kHz 单声道）。
// ext 可带点；mp3/wav 转码，silk 原样返回；后缀不是这三种时按内容嗅探兜底。
func ToSilk(data []byte, ext string) ([]byte, error) {
	kind := normalizeExt(ext)
	switch kind {
	case "silk":
		return data, nil
	case "wav", "wave":
		pcm, rate, ch, err := decodeWAV(data)
		if err != nil {
			return nil, err
		}
		return encodeSilk(pcm, rate, ch)
	case "mp3":
		return mp3ToSilk(data)
	}
	// 后缀未知/缺失 → 嗅探内容（RIFF→wav，ID3/MPEG 帧同步→mp3）。
	switch sniff(data) {
	case "wav":
		pcm, rate, ch, err := decodeWAV(data)
		if err != nil {
			return nil, err
		}
		return encodeSilk(pcm, rate, ch)
	case "mp3":
		return mp3ToSilk(data)
	}
	return nil, fmt.Errorf("不支持的音频格式 %q（支持 mp3/wav/silk）", ext)
}

// mp3ToSilk 解码 mp3 → PCM（双声道）→ 编 silk。
func mp3ToSilk(data []byte) ([]byte, error) {
	dec, err := mp3dec.NewDecoder(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("mp3 解码失败: %w", err)
	}
	pcm, err := io.ReadAll(dec)
	if err != nil {
		return nil, fmt.Errorf("mp3 解码失败: %w", err)
	}
	if len(pcm) == 0 {
		return nil, fmt.Errorf("mp3 无音频数据")
	}
	return encodeSilk(pcm, dec.SampleRate(), 2)
}

// decodeWAV 解析 RIFF/WAVE（仅 16 位 PCM），返回 pcm 字节、采样率、声道数。
// 遍历块结构以兼容带 LIST 等附加块、data 在 fmt 之后的文件。
func decodeWAV(b []byte) ([]byte, int, int, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, 0, 0, fmt.Errorf("不是合法的 wav 文件")
	}
	var rate, channels, bits int
	var pcm []byte
	off := 12
	for off+8 <= len(b) {
		id := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		body := b[min(off+8, len(b)):min(off+8+size, len(b))]
		switch id {
		case "fmt ":
			if len(body) < 16 {
				return nil, 0, 0, fmt.Errorf("wav 的 fmt 块过短")
			}
			format := binary.LittleEndian.Uint16(body[0:2])
			channels = int(binary.LittleEndian.Uint16(body[2:4]))
			rate = int(binary.LittleEndian.Uint32(body[4:8]))
			bits = int(binary.LittleEndian.Uint16(body[14:16]))
			if format != 1 {
				return nil, 0, 0, fmt.Errorf("仅支持 PCM 编码的 wav（format=%d）", format)
			}
			if bits != 16 {
				return nil, 0, 0, fmt.Errorf("仅支持 16 位 wav（bits=%d）", bits)
			}
		case "data":
			pcm = body
		}
		off += 8 + size
		if size%2 == 1 {
			off++ // RIFF 块按 2 字节对齐
		}
	}
	if pcm == nil || channels == 0 || rate == 0 {
		return nil, 0, 0, fmt.Errorf("wav 缺少 fmt/data 块")
	}
	return pcm, rate, channels, nil
}

// encodeSilk 把 16bit 小端交错 pcm 编成 QQ silk：混单声道 → 重采样 24kHz → Tencent 头输出。
func encodeSilk(pcm []byte, rate, channels int) ([]byte, error) {
	if rate <= 0 || channels < 1 || channels > 2 {
		return nil, fmt.Errorf("不支持的采样参数: rate=%d channels=%d", rate, channels)
	}
	samples := bytesToInt16(pcm)
	if channels == 2 {
		samples = downMixMono(samples)
	}
	samples = resampleLinear(samples, rate, silkSampleRate)
	if len(samples) == 0 {
		return nil, fmt.Errorf("音频数据为空")
	}
	var out bytes.Buffer
	if err := silksdk.Encode(silksdk.EncodeOptions{
		Input:      bytes.NewReader(int16ToBytes(samples)),
		Output:     &out,
		SampleRate: silkSampleRate,
		BitRate:    silkBitRate,
		Tencent:    true, // QQ/微信语音格式：0x02 头、无 0xFF 0xFF EOF 标记
	}); err != nil {
		return nil, fmt.Errorf("silk 编码失败: %w", err)
	}
	return out.Bytes(), nil
}

// downMixMono 把双声道交错采样平均成单声道（int32 中转不会溢出）。
func downMixMono(s []int16) []int16 {
	n := len(s) / 2
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		out[i] = int16((int32(s[2*i]) + int32(s[2*i+1])) / 2)
	}
	return out
}

// resampleLinear 线性插值重采样（语音场景够用；rate 相同则原样返回）。
func resampleLinear(s []int16, from, to int) []int16 {
	if from == to || len(s) == 0 {
		return s
	}
	n := int(float64(len(s)) * float64(to) / float64(from))
	if n == 0 {
		n = 1
	}
	out := make([]int16, n)
	last := int16(0)
	if len(s) > 0 {
		last = s[len(s)-1]
	}
	for i := 0; i < n; i++ {
		pos := float64(i) * float64(from) / float64(to)
		j := int(pos)
		if j >= len(s)-1 {
			out[i] = last
			continue
		}
		frac := pos - float64(j)
		out[i] = int16(float64(s[j])*(1-frac) + float64(s[j+1])*frac)
	}
	return out
}

// sniff 按文件头嗅探音频类型，返回 "wav"/"mp3"/""（无法识别）。
func sniff(b []byte) string {
	switch {
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WAVE":
		return "wav"
	case len(b) >= 3 && string(b[0:3]) == "ID3":
		return "mp3"
	case len(b) >= 2 && b[0] == 0xFF && b[1]&0xE0 == 0xE0:
		return "mp3" // MPEG 帧同步字
	}
	return ""
}

func bytesToInt16(b []byte) []int16 {
	n := len(b) / 2
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i : 2*i+2]))
	}
	return out
}

func int16ToBytes(s []int16) []byte {
	out := make([]byte, len(s)*2)
	for i, v := range s {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(v))
	}
	return out
}

func normalizeExt(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	return strings.TrimPrefix(ext, ".")
}
