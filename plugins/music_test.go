package plugins

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"qqbot/internal/bot"
)

// newTestMusic 构造指向 httptest 的插件实例（偏好落盘到临时目录）。
func newTestMusic(t *testing.T, base string) *Music {
	t.Helper()
	p := &Music{
		apiBase:      base,
		kuwoBR:       2,
		neteaseLevel: "exhigh",
		prefFn:       filepath.Join(t.TempDir(), "music.json"),
		pending:      map[string]*musicPending{},
	}
	p.sources = p.buildSources()
	p.def = "5sign"
	return p
}

// musicSrv 按路径分发固定响应的 itapi 模拟服务。
func musicSrv(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.URL.Path {
		case "/5sign":
			if r.URL.Query().Get("n") != "" {
				fmt.Fprint(w, `{"data":{"code":200,"title":"天下","singer":"小千",
					"cover":"https://wsingbssdl.kugou.com/c.jpg","link":"https://5sing.kugou.com/fc/5906676.html",
					"url":"https://wsaudiobssdlbig.kugou.com/x.mp3"}}`)
				return
			}
			fmt.Fprint(w, `{"data":[
				{"ID":1,"title":"天下","singer":"小千","songtype":"翻唱","originSinger":"张杰","songid":5906676},
				{"ID":2,"title":"天下","singer":"羊皮为裘","songtype":"原创","originSinger":"羊皮为裘","songid":3274660},
				{"ID":3,"title":"","singer":"空标题应跳过"}]}`)
		case "/qqmisic":
			if r.URL.Query().Get("n") != "" {
				fmt.Fprint(w, `{"code":200,"data":{"name":"天下","singer":"张杰/DJ Wave","duration":"4:07",
					"bitRate":320000,"extName":"mp3","quality":"higher",
					"cover_url":"https://y.gtimg.cn/c.jpg","play_url":"http://aqqmusic.tc.qq.com/M800.mp3?vkey=x","lyric":"[ti:天下]"}}`)
				return
			}
			var list []string
			for i := 1; i <= 9; i++ {
				pay := "免费"
				if i == 1 {
					pay = "付费"
				}
				list = append(list, fmt.Sprintf(`{"id":%d,"name":"天下%d","artist":"张杰%d","pay":%d,"paytext":"%s","duration":"3:4%d"}`,
					i, i, i, i%2, pay, i%10))
			}
			fmt.Fprintf(w, `{"code":200,"data":{"total":9,"list":[%s]}}`, strings.Join(list, ","))
		case "/kw":
			if r.URL.Query().Get("b") != "" {
				fmt.Fprint(w, `json:{"app":"com.tencent.structmsg","meta":{"music":{"title":"天下","desc":"张杰",
					"musicUrl":"https://kw-bj.kuwo.cn/x.mp3","preview":"http://img1.kwcdn.kuwo.cn/p.jpg","tag":"酷我音乐"}}}`)
				return
			}
			fmt.Fprint(w, "1：天下——张杰\n2：天下 (DJ Wave版)——张杰&DJ Wave\n3：提示行没有分隔符不应匹配\n搜索到与天下相关的歌曲共3条\n")
		case "/Kuwo":
			if r.URL.Query().Get("n") != "" {
				fmt.Fprint(w, `{"code":1,"data":{"song":"天下","singer":"张杰","album":"听！我们的歌",
					"format":"flac","level":"ff","picture":"https://kwimg1.kuwo.cn/c.jpg",
					"time":"221","url":"http://kw-er.kuwo.cn/y.flac?sign=x"}}`)
				return
			}
			fmt.Fprint(w, `{"code":1,"message":"1. 天下 —— 张杰","data":[
				{"song":"天下","singer":"张杰","time":"221","rid":"MUSIC_1","picture":"https://kwimg1.kuwo.cn/c.jpg"},
				{"song":"天下 (DJ Wave版)","singer":"张杰&DJ Wave","time":"247","rid":"MUSIC_2"}]}`)
		case "/Qsyy":
			if r.URL.Query().Get("n") != "" {
				if r.URL.Query().Get("n") == "1" {
					fmt.Fprint(w, `{"msg":"免费歌曲,已获取完整音频","pay":"free","title":"天下","singer":"张杰",
						"music":"https://v5-luna.douyinvod.com/a/?mime_type=audio_mp4","cover":"https://p3-luna.douyinpic.com/c.jpg",
						"link":"https://music.douyin.com/qishui/share/track?track_id=761"}`)
				} else {
					fmt.Fprint(w, `{"msg":"付费歌曲,仅试听30秒","pay":"paid","title":"晴天","singer":"周杰伦"}`)
				}
				return
			}
			fmt.Fprint(w, `{"code":200,"data":[{"n":1,"title":"天下","singer":"张杰"},{"n":2,"title":"天下（DJ降速版）","singer":"DJ万言"}]}`)
		case "/wangyiyun":
			fmt.Fprint(w, ``) // 2026-09 实测：上游空响应
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestMusicSearchPick(t *testing.T) {
	srv := musicSrv(t)
	defer srv.Close()
	p := newTestMusic(t, srv.URL)
	ctx := context.Background()
	src := func(key string) *musicSource {
		for _, s := range p.sources {
			if s.Key == key {
				return s
			}
		}
		t.Fatalf("音源 %s 未注册", key)
		return nil
	}

	// 5sign：N 取接口 ID，翻唱标记进 Extra，空标题跳过。
	es, err := src("5sign").search(ctx, "天下")
	if err != nil || len(es) != 2 {
		t.Fatalf("5sign search: %v, %d 条", err, len(es))
	}
	if es[0].N != 1 || es[0].Title != "天下" || es[0].Extra != "翻唱" {
		t.Errorf("5sign 第一条不对: %+v", es[0])
	}
	tr, err := src("5sign").pick(ctx, "天下", 1)
	if err != nil || tr.AudioURL == "" || tr.Format != "mp3" || tr.PageURL == "" {
		t.Fatalf("5sign pick: %v %+v", err, tr)
	}

	// qq：超 6 条截断；付费标记保留、免费不显示。
	es, err = src("qq").search(ctx, "天下")
	if err != nil || len(es) != musicMaxList {
		t.Fatalf("qq search: %v, %d 条（应截到 %d）", err, len(es), musicMaxList)
	}
	if es[0].Extra != "3:41·付费" || es[1].Extra != "3:42" {
		t.Errorf("qq Extra 不对: %+v / %+v", es[0], es[1])
	}
	tr, err = src("qq").pick(ctx, "天下", 2)
	if err != nil || tr.Format != "mp3" || tr.Quality != "higher" || tr.Duration != "4:07" {
		t.Fatalf("qq pick: %v %+v", err, tr)
	}

	// kuwo：纯文本列表解析；无分隔线的行忽略。
	es, err = src("kuwo").search(ctx, "天下")
	if err != nil || len(es) != 2 {
		t.Fatalf("kuwo search: %v, %d 条", err, len(es))
	}
	if es[1].Title != "天下 (DJ Wave版)" || es[1].Singer != "张杰&DJ Wave" {
		t.Errorf("kuwo 第二条不对: %+v", es[1])
	}
	tr, err = src("kuwo").pick(ctx, "天下", 1)
	if err != nil || tr.AudioURL != "https://kw-bj.kuwo.cn/x.mp3" || tr.Cover == "" {
		t.Fatalf("kuwo pick: %v %+v", err, tr)
	}

	// kuwohq：秒数时长归一化；无损档名。
	es, err = src("kuwohq").search(ctx, "天下")
	if err != nil || len(es) != 2 {
		t.Fatalf("kuwohq search: %v, %d 条", err, len(es))
	}
	if es[0].Extra != "3:41" {
		t.Errorf("kuwohq 时长未归一化: %+v", es[0])
	}
	tr, err = src("kuwohq").pick(ctx, "天下", 1)
	if err != nil || tr.Format != "flac" || tr.Quality != "无损" || tr.Duration != "3:41" {
		t.Fatalf("kuwohq pick: %v %+v", err, tr)
	}

	// qishui：付费歌无 music → 错误带接口 msg。
	es, err = src("qishui").search(ctx, "天下")
	if err != nil || len(es) != 2 || es[1].N != 2 {
		t.Fatalf("qishui search: %v %+v", err, es)
	}
	if _, err = src("qishui").pick(ctx, "晴天", 1); err != nil || tr.AudioURL == "" {
		t.Fatalf("qishui pick(免费): %v", err)
	}
	if _, err = src("qishui").pick(ctx, "晴天", 2); err == nil || !strings.Contains(err.Error(), "付费") {
		t.Fatalf("qishui pick(付费) 应报含「付费」的错误, got %v", err)
	}

	// netease：上游空响应 → 引导换音源的错误。
	if _, err = src("netease").search(ctx, "天下"); err == nil || !strings.Contains(err.Error(), "无响应") {
		t.Fatalf("netease search 应报无响应, got %v", err)
	}
	if _, err = src("netease").pick(ctx, "天下", 1); err == nil || !strings.Contains(err.Error(), "无响应") {
		t.Fatalf("netease pick 应报无响应, got %v", err)
	}
}

func TestMusicKwLineRE(t *testing.T) {
	cases := []struct {
		in            string
		n             int
		title, singer string
	}{
		{"1：天下——张杰", 1, "天下", "张杰"},
		{"2：天下 (DJ Wave版)——张杰&DJ Wave", 2, "天下 (DJ Wave版)", "张杰&DJ Wave"},
		{"12. 天下    ——    张杰", 12, "天下", "张杰"},
		{"搜索到与天下相关的歌曲共9条", 0, "", ""},
	}
	for _, c := range cases {
		m := musicKwLineRE.FindStringSubmatch(c.in)
		if c.title == "" {
			if m != nil {
				t.Errorf("不应匹配 %q", c.in)
			}
			continue
		}
		if m == nil {
			t.Errorf("未匹配 %q", c.in)
			continue
		}
		got, _ := strconv.Atoi(m[1])
		if got != c.n || m[2] != c.title || m[3] != c.singer {
			t.Errorf("匹配 %q = %v", c.in, m)
		}
	}
}

func TestMusicListText(t *testing.T) {
	src := &musicSource{Key: "kuwo", Name: "酷我音乐"}
	entries := []musicEntry{
		{N: 1, Title: "天下", Singer: "张杰", Extra: "3:41"},
		{N: 2, Title: "天下 (DJ Wave版)", Singer: "张杰&DJ Wave"},
	}
	// 标签版：每行一个 qqbot-cmd-input，text/show 均 urlencode。
	tagged := musicListText(src, "天下", entries, true)
	if got := strings.Count(tagged, "<qqbot-cmd-input"); got != 2 {
		t.Fatalf("应每行 1 个标签共 2 个, got %d", got)
	}
	wantText := url.QueryEscape("选 1")
	wantShow := url.QueryEscape("1. 天下 - 张杰 [3:41]")
	if !strings.Contains(tagged, `text="`+wantText+`"`) || !strings.Contains(tagged, `show="`+wantShow+`"`) {
		t.Errorf("标签参数不对:\n%s", tagged)
	}
	if !strings.Contains(tagged, "点歌名标签选歌") {
		t.Errorf("缺标签版提示: %s", tagged)
	}
	// 纯文本版：无标签、有手动提示。
	plain := musicListText(src, "天下", entries, false)
	if strings.Contains(plain, "qqbot-cmd") {
		t.Errorf("纯文本版不应含标签: %s", plain)
	}
	for _, want := range []string{"1. 天下 - 张杰 [3:41]", "2. 天下 (DJ Wave版) - 张杰&DJ Wave", "发送「选 序号」"} {
		if !strings.Contains(plain, want) {
			t.Errorf("纯文本缺 %q: %s", want, plain)
		}
	}
}

func TestMusicPrefsPersist(t *testing.T) {
	srv := musicSrv(t)
	defer srv.Close()
	p := newTestMusic(t, srv.URL)
	c := &bot.Ctx{}

	// 别名解析。
	for in, want := range map[string]string{"酷我": "kuwo", "QQ音乐": "qq", "5sing": "5sign", "wy": "netease", "无损": "kuwohq", "不存在": ""} {
		if got := p.resolveKey(in); got != want {
			t.Errorf("resolveKey(%q) = %q, want %q", in, got, want)
		}
	}

	if got := p.sourceFor(c).Key; got != "5sign" {
		t.Fatalf("无覆盖时应为默认 5sign, got %s", got)
	}
	p.setChatSource(c, "kuwo")
	if got := p.sourceFor(c).Key; got != "kuwo" {
		t.Fatalf("切换后应为 kuwo, got %s", got)
	}

	// 另一实例从盘上读回同一偏好。
	p2 := newTestMusic(t, srv.URL)
	p2.prefFn = p.prefFn
	p2.loadPrefs()
	if got := p2.sourceFor(c).Key; got != "kuwo" {
		t.Fatalf("重启后偏好应保留 kuwo, got %s", got)
	}
	p.setChatSource(c, "")
	if got := p.sourceFor(c).Key; got != "5sign" {
		t.Fatalf("重置后应为 5sign, got %s", got)
	}
}

func TestMusicPendingTTL(t *testing.T) {
	p := newTestMusic(t, "http://x")
	c := &bot.Ctx{}
	if got := p.takePending(c); got != nil {
		t.Fatal("无待选时应返回 nil")
	}
	p.storePending(c, &musicPending{
		src: p.sources[0], keyword: "天下",
		entries: []musicEntry{{N: 1, Title: "天下", Singer: "张杰"}},
		expires: time.Now().Add(-time.Second),
	})
	if got := p.takePending(c); got != nil {
		t.Fatal("过期待选应返回 nil")
	}
	p.storePending(c, &musicPending{
		src: p.sources[0], keyword: "天下",
		entries: []musicEntry{{N: 1, Title: "天下", Singer: "张杰"}},
		expires: time.Now().Add(time.Minute),
	})
	if got := p.takePending(c); got == nil || got.entries[0].Title != "天下" {
		t.Fatal("有效待选应可取回")
	}
}

func TestMusicIntro(t *testing.T) {
	src := &musicSource{Key: "kuwo", Name: "酷我音乐"}
	tr := &musicTrack{
		Title: "天下", Singer: "张杰", Cover: "https://kwimg1.kuwo.cn/c.jpg",
		PageURL: "https://5sing.kugou.com/fc/1.html", Format: "mp3",
		Quality: "higher", Duration: "3:41",
	}
	md, plain := musicIntro(src, tr)
	if !strings.HasPrefix(md, "![封面](https://kwimg1.kuwo.cn/c.jpg)\n") {
		t.Errorf("md 应以封面图开头: %q", md)
	}
	for _, want := range []string{"🎵 天下 - 张杰", "酷我音乐 ｜ 3:41 ｜ mp3 ｜ higher", "🔗 https://5sing.kugou.com/fc/1.html", "歌曲发送中"} {
		if !strings.Contains(md, want) {
			t.Errorf("md 缺 %q: %q", want, md)
		}
	}
	if strings.Contains(plain, "![封面]") {
		t.Errorf("降级文本不应带封面: %q", plain)
	}
	if !strings.Contains(plain, "🎵 天下 - 张杰") || !strings.Contains(plain, "歌曲发送中") {
		t.Errorf("降级文本缺标题/发送中提示: %q", plain)
	}
	// 无封面：md 与 plain 一致。
	tr2 := &musicTrack{Title: "晴天", Singer: "周杰伦", Format: "m4a"}
	md2, plain2 := musicIntro(src, tr2)
	if md2 != plain2 || !strings.Contains(md2, "m4a") {
		t.Errorf("无封面时 md/plain 应一致且含格式: %q / %q", md2, plain2)
	}
}

func TestMusicHelpers(t *testing.T) {
	// 时长归一化。
	for in, want := range map[string]string{"221": "3:41", "59": "0:59", "3:41": "3:41", "": "", "abc": "abc"} {
		if got := musicFmtDur(in); got != want {
			t.Errorf("musicFmtDur(%q) = %q, want %q", in, got, want)
		}
	}
	// 文件名清洗（非法字符转空格并折叠连续空白）。
	if got := musicFileName("天/下: *好*?", "张\"杰\"", "mp3"); got != "天 下 好 - 张 杰.mp3" {
		t.Errorf("文件名清洗不对: %q", got)
	}
	if got := musicFileName("", "", "m4a"); got != "歌曲.m4a" {
		t.Errorf("空信息应兜底: %q", got)
	}
	// 格式推断：优先接口字段，缺省按 URL 猜（含 query）。
	if got := musicFormatOf(&musicTrack{Format: "FLAC"}); got != "flac" {
		t.Errorf("Format 大写应归一: %q", got)
	}
	if got := musicFormatOf(&musicTrack{AudioURL: "http://x/a/b.MP3?sign=1"}); got != "mp3" {
		t.Errorf("URL 猜格式不对: %q", got)
	}
	if got := musicFormatOf(&musicTrack{AudioURL: "http://x/a/noext"}); got != "" {
		t.Errorf("无扩展名应为空: %q", got)
	}
}
