package audio

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"testing"

	"github.com/rolandhe/silk-go/pkg/silksdk"
)

// synthPCM 生成 secs 秒 440Hz 正弦（测试输入）。
func synthPCM(rate, channels, secs int) []int16 {
	n := rate * secs * channels
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		t := float64(i/channels) / float64(rate)
		out[i] = int16(8000 * math.Sin(2*math.Pi*440*t))
	}
	return out
}

// buildWAV 把交错采样打包成 16 位 PCM RIFF/WAVE 字节。
func buildWAV(samples []int16, rate, channels int) []byte {
	data := make([]byte, len(samples)*2)
	for i, v := range samples {
		binary.LittleEndian.PutUint16(data[2*i:], uint16(v))
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&b, binary.LittleEndian, uint16(channels))
	binary.Write(&b, binary.LittleEndian, uint32(rate))
	binary.Write(&b, binary.LittleEndian, uint32(rate*channels*2))
	binary.Write(&b, binary.LittleEndian, uint16(channels*2))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	return b.Bytes()
}

// decodeSilkBack 用 silksdk 解回 PCM（自动剥 0x02 Tencent 头），返回样本数。
func decodeSilkBack(t *testing.T, silk []byte) int {
	t.Helper()
	var pcm bytes.Buffer
	m := &silksdk.DecodeMetrics{}
	if err := silksdk.Decode(silksdk.DecodeOptions{
		Input:      bytes.NewReader(silk),
		Output:     &pcm,
		SampleRate: silkSampleRate,
		Metrics:    m,
	}); err != nil {
		t.Fatalf("silk 解码失败: %v", err)
	}
	return int(m.SamplesOut)
}

func TestToSilkWAVRoundTrip(t *testing.T) {
	// 44.1kHz 立体声 → 混 mono + 重采样 24kHz。
	wav := buildWAV(synthPCM(44100, 2, 1), 44100, 2)
	silk, err := ToSilk(wav, "wav")
	if err != nil {
		t.Fatalf("ToSilk: %v", err)
	}
	// QQ 头：0x02 开头，且不应有标准 #!SILK_V3 魔数。
	if len(silk) < 2 || silk[0] != 0x02 {
		t.Fatalf("silk 应以 0x02 开头, got % X", silk[:min(4, len(silk))])
	}
	if bytes.HasPrefix(silk, []byte("#!SILK_V3")) {
		t.Fatal("Tencent 模式不应带 #!SILK_V3 魔数")
	}
	got := decodeSilkBack(t, silk)
	// 1s 音频 → 24000 样本；允许 20ms 包对齐造成的少量偏差。
	if math.Abs(float64(got-24000)) > 960 {
		t.Errorf("解码样本数 %d, 期望约 24000", got)
	}
}

func TestToSilkWAVMono16k(t *testing.T) {
	wav := buildWAV(synthPCM(16000, 1, 1), 16000, 1)
	silk, err := ToSilk(wav, ".wav") // 扩展名带点也能认
	if err != nil {
		t.Fatalf("ToSilk: %v", err)
	}
	if silk[0] != 0x02 {
		t.Fatal("应以 0x02 开头")
	}
	if got := decodeSilkBack(t, silk); math.Abs(float64(got-24000)) > 960 {
		t.Errorf("解码样本数 %d, 期望约 24000（上采样 16k→24k）", got)
	}
}

func TestToSilkSniffUnknownExt(t *testing.T) {
	// 后缀缺失/未知 → 按内容嗅探为 wav。
	wav := buildWAV(synthPCM(24000, 1, 1), 24000, 1)
	if _, err := ToSilk(wav, ""); err != nil {
		t.Fatalf("嗅探 wav 失败: %v", err)
	}
	if _, err := ToSilk(wav, "bin"); err != nil {
		t.Fatalf("嗅探 wav（bin 后缀）失败: %v", err)
	}
}

func TestToSilkPassthroughSilk(t *testing.T) {
	raw := []byte{0x02, 0x01, 0x02, 0x03}
	out, err := ToSilk(raw, "silk")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, raw) {
		t.Fatal("silk 应原样透传")
	}
}

func TestToSilkUnsupported(t *testing.T) {
	if _, err := ToSilk([]byte{1, 2, 3, 4}, "flac"); err == nil {
		t.Fatal("flac 应报不支持")
	}
}

func TestToSilkWAVBadFormat(t *testing.T) {
	// 32 位 float wav（format=3）应明确报错。
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(50))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(3)) // IEEE float
	b.Write(make([]byte, 14))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(4))
	b.Write([]byte{0, 0, 0, 0})
	if _, err := ToSilk(b.Bytes(), "wav"); err == nil {
		t.Fatal("非 PCM wav 应报错")
	}
}

func TestToSilkMP3(t *testing.T) {
	data, err := os.ReadFile("testdata/sample.mp3")
	if err != nil {
		t.Skip("无 mp3 fixture")
	}
	silk, err := ToSilk(data, "mp3")
	if err != nil {
		t.Fatalf("mp3 转 silk: %v", err)
	}
	if len(silk) < 2 || silk[0] != 0x02 {
		t.Fatalf("silk 应以 0x02 开头, got % X", silk[:min(4, len(silk))])
	}
	n := decodeSilkBack(t, silk)
	if n < 8000 { // 32KB @128kbps 立体声 ≈ 2s；解码出至少 0.3s
		t.Errorf("解码样本过少: %d", n)
	}
	t.Logf("fixture %d 字节 → silk %d 字节, %.1fs", len(data), len(silk), float64(n)/24000)
}

func TestResampleLinearLength(t *testing.T) {
	out := resampleLinear(make([]int16, 44100), 44100, 24000)
	if len(out) != 24000 {
		t.Errorf("44100→24000 应得 24000 样本, got %d", len(out))
	}
	// 相同采样率原样返回。
	same := make([]int16, 100)
	same[42] = 7
	out = resampleLinear(same, 24000, 24000)
	if len(out) != 100 || out[42] != 7 {
		t.Error("同采样率应原样返回")
	}
}

func TestSniff(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{string(buildWAV(nil, 24000, 1)), "wav"},
		{"ID3\x03\x00\x00\x00\x00", "mp3"},
		{string([]byte{0xFF, 0xFB, 0x90, 0x00}), "mp3"},
		{"\x00\x01\x02\x03", ""},
	}
	for i, c := range cases {
		if got := sniff([]byte(c.in)); got != c.want {
			t.Errorf("case %d: sniff=%q want %q", i, got, c.want)
		}
	}
}

func TestDownMixMono(t *testing.T) {
	out := downMixMono([]int16{100, 200, -30000, 30000})
	want := []int16{150, 0}
	if len(out) != len(want) {
		t.Fatalf("len=%d want %d", len(out), len(want))
	}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("out[%d]=%d want %d", i, out[i], want[i])
		}
	}
}
