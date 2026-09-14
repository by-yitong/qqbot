package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"qqbot/internal/bot"
	"qqbot/internal/imgstore"
)

func TestExtractPayload(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    webhookPayload
		wantErr string
	}{
		{name: "飞书格式", in: `{"msg_type":"text","content":{"text":"飞书格式"}}`,
			want: webhookPayload{msgType: "text", text: "飞书格式"}},
		{name: "简写格式", in: `{"text":"简写格式"}`,
			want: webhookPayload{msgType: "text", text: "简写格式"}},
		{name: "纯文本", in: `纯文本 body`,
			want: webhookPayload{msgType: "text", text: "纯文本 body"}},
		{name: "空 body", in: `  `, wantErr: "empty text"},
		{name: "at 全体被删", in: `{"msg_type":"text","content":{"text":"测试<at userid='all'>所有人</at>"}}`,
			want: webhookPayload{msgType: "text", text: "测试"}},
		{name: "at 某人转文本", in: `{"text":"<at user_id=\"ou_123\">张三</at> 看下"}`,
			want: webhookPayload{msgType: "text", text: "@张三 看下"}},
		{name: "图片", in: `{"msg_type":"image","content":{"url":"https://x/a.png"}}`,
			want: webhookPayload{msgType: "image", url: "https://x/a.png"}},
		{name: "视频顶层 url", in: `{"msg_type":"video","url":"https://x/a.mp4"}`,
			want: webhookPayload{msgType: "video", url: "https://x/a.mp4"}},
		{name: "文件带说明文字", in: `{"msg_type":"file","content":{"url":"https://x/r.zip","text":"报表"}}`,
			want: webhookPayload{msgType: "file", text: "报表", url: "https://x/r.zip"}},
		{name: "markdown", in: `{"msg_type":"markdown","content":{"text":"# 标题\n**加粗**"}}`,
			want: webhookPayload{msgType: "markdown", text: "# 标题\n**加粗**"}},
		{name: "md 别名", in: `{"msg_type":"md","text":"- 列表"}`,
			want: webhookPayload{msgType: "markdown", text: "- 列表"}},
		{name: "markdown 空", in: `{"msg_type":"markdown","content":{}}`, wantErr: "empty text"},
		{name: "富媒体缺 url", in: `{"msg_type":"image","content":{}}`,
			wantErr: "需要 url"},
		{name: "JSON 无文本无类型", in: `{"msg_type":"text","content":{}}`, wantErr: "empty text"},
		{name: "不支持的类型", in: `{"msg_type":"post","content":{}}`,
			wantErr: "不支持的 msg_type"},
	}
	for _, c := range cases {
		got, err := extractPayload([]byte(c.in))
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: extractPayload(%q) 错误 = %v, 应含 %q", c.name, c.in, err, c.wantErr)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: extractPayload(%q) = %+v, %v; want %+v", c.name, c.in, got, err, c.want)
		}
	}
}

func TestMediaKindByExt(t *testing.T) {
	cases := map[string]string{
		"png": "image", "JPG": "image", "jpeg": "image",
		"mp4": "video", "mov": "video",
		"silk": "voice", "mp3": "voice", "wav": "voice",
		"zip": "file", "pdf": "file", "": "file",
	}
	for ext, want := range cases {
		if got := mediaKindByExt(strings.ToLower(ext)); got != want {
			t.Errorf("mediaKindByExt(%q) = %q, want %q", ext, got, want)
		}
	}
}

func TestURLExtOf(t *testing.T) {
	if got := urlExtOf("https://x.com/a/b.PNG?sig=1"); got != "png" {
		t.Errorf("urlExtOf 带查询参数 = %q, want png", got)
	}
	if got := urlExtOf("https://x.com/noext"); got != "" {
		t.Errorf("urlExtOf 无扩展 = %q, want 空", got)
	}
}

// multipartReq 构造一个 multipart/form-data 请求（fields 里 k=v；fileField 非空时
// 追加一个文件字段）。
func multipartReq(t *testing.T, fields map[string]string, fileField, filename string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if fileField != "" {
		fw, err := mw.CreateFormFile(fileField, filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/webhook/g/s", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestParseMultipart(t *testing.T) {
	dir := t.TempDir()
	host := &imgstore.Host{BaseURL: "https://img.example.com", Dir: dir}
	p := NewWebhook(t.TempDir()+"/webhook.json", "", "", host)

	// png 直传（有图床）：嗅探为 image，字节落盘图床目录换成公网 URL
	pl, err := p.parseMultipart(httptest.NewRecorder(), multipartReq(t, map[string]string{"text": "看图"}, "file", "a.png", []byte("pngdata")))
	if err != nil {
		t.Fatalf("png 直传: %v", err)
	}
	if pl.msgType != "image" || pl.text != "看图" || !strings.HasPrefix(pl.url, host.BaseURL+"/") || pl.fileData != nil {
		t.Errorf("png 直传 payload = %+v", pl)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 || !strings.HasSuffix(ents[0].Name(), ".png") {
		t.Errorf("图床目录应落盘 1 个 .png: %v", ents)
	}

	// 未知扩展：按通用文件，字节直传分片（不落图床）
	pl, err = p.parseMultipart(httptest.NewRecorder(), multipartReq(t, nil, "file", "报表.xlsx", []byte("XLSX")))
	if err != nil || pl.msgType != "file" || pl.fileData == nil || string(pl.fileData) != "XLSX" || pl.fileName != "报表.xlsx" || pl.url != "" {
		t.Errorf("xlsx 应字节直传: %+v, %v", pl, err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("file 直传不应落图床: %v", ents)
	}

	// 显式 type=file 覆盖嗅探：同样字节直传
	pl, err = p.parseMultipart(httptest.NewRecorder(), multipartReq(t, map[string]string{"type": "file"}, "file", "a.png", []byte("x")))
	if err != nil || pl.msgType != "file" || pl.fileData == nil {
		t.Errorf("显式 file 应覆盖嗅探: %+v, %v", pl, err)
	}

	// url 字段模式（multipart 里直接给地址，不落图床）；缺省按扩展嗅探
	pl, err = p.parseMultipart(httptest.NewRecorder(), multipartReq(t, map[string]string{"url": "https://x/a.mp4"}, "", "", nil))
	if err != nil || pl.msgType != "video" || pl.url != "https://x/a.mp4" {
		t.Errorf("url 字段模式: %+v, %v", pl, err)
	}

	// 既无 file 也无 url
	if _, err = p.parseMultipart(httptest.NewRecorder(), multipartReq(t, nil, "", "", nil)); err == nil || !strings.Contains(err.Error(), "file") {
		t.Errorf("缺 file/url 应报错: %v", err)
	}

	// 不支持的 type
	if _, err = p.parseMultipart(httptest.NewRecorder(), multipartReq(t, map[string]string{"type": "audio"}, "file", "a.png", []byte("x"))); err == nil || !strings.Contains(err.Error(), "不支持的 type") {
		t.Errorf("未知 type 应报错: %v", err)
	}

	// 未配图床：image 兜底字节直传（不再 503）
	p2 := NewWebhook(t.TempDir()+"/webhook.json", "", "", nil)
	pl, err = p2.parseMultipart(httptest.NewRecorder(), multipartReq(t, nil, "file", "a.png", []byte("px")))
	if err != nil || pl.msgType != "image" || pl.fileData == nil || string(pl.fileData) != "px" {
		t.Errorf("无图床 image 应兜底直传: %+v, %v", pl, err)
	}

	// 未配图床：voice 无转码能力，仍 503
	_, err = p2.parseMultipart(httptest.NewRecorder(), multipartReq(t, nil, "file", "a.mp3", []byte("x")))
	var se *statusErr
	if err == nil || !errors.As(err, &se) || se.status != http.StatusServiceUnavailable {
		t.Errorf("无图床语音应 503: %v", err)
	}

	// 超过大小上限：413
	old := webhookMaxFileBytes
	webhookMaxFileBytes = 8
	defer func() { webhookMaxFileBytes = old }()
	_, err = p.parseMultipart(httptest.NewRecorder(), multipartReq(t, nil, "file", "big.png", []byte("123456789")))
	if err == nil || !errors.As(err, &se) || se.status != http.StatusRequestEntityTooLarge {
		t.Errorf("超限应 413: %v", err)
	}
}

// TestWebhookHandle 富媒体推送全链路：JSON url 模式与 multipart 直传都应走到
// 「发送到 QQ」这一步（未连接 api → ErrNotConnected → 502），图床落盘可验证。
func TestWebhookHandle(t *testing.T) {
	b := bot.New(bot.Config{})
	dir := t.TempDir()
	p := NewWebhook(t.TempDir()+"/webhook.json", "", "",
		&imgstore.Host{BaseURL: "https://img.example.com", Dir: dir})
	p.Install(b)
	p.mu.Lock()
	p.conf.Groups["g1"] = "s1"
	p.mu.Unlock()
	srv := httptest.NewServer(http.HandlerFunc(p.handle))
	defer srv.Close()

	post := func(contentType, body string) *http.Response {
		t.Helper()
		resp, err := http.Post(srv.URL+"/webhook/g1/s1", contentType, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	// JSON 富媒体（file 类型）：路由到发送步，因未连接 api 而 502
	if resp := post("application/json", `{"msg_type":"file","content":{"url":"https://x/r.zip","text":"报表"}}`); resp.StatusCode != http.StatusBadGateway {
		t.Errorf("JSON file 推送应 502（发送失败）, got %d", resp.StatusCode)
	}
	// JSON markdown：路由到 SendGroupMarkdown（未连接 api → 502，证明走 md 分支）
	if resp := post("application/json", `{"msg_type":"markdown","content":{"text":"# hi **md**"}}`); resp.StatusCode != http.StatusBadGateway {
		t.Errorf("JSON markdown 推送应 502（发送失败）, got %d", resp.StatusCode)
	}
	// 纯文本行为不变
	if resp := post("text/plain", "hello"); resp.StatusCode != http.StatusBadGateway {
		t.Errorf("纯文本推送应 502（发送失败）, got %d", resp.StatusCode)
	}
	// 错误负载：400
	if resp := post("application/json", `{"msg_type":"image","content":{}}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("缺 url 应 400, got %d", resp.StatusCode)
	}

	// multipart 直传：图床落盘后同样走到发送步（502）
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "a.png")
	_, _ = fw.Write([]byte("png"))
	_ = mw.Close()
	resp, err := http.Post(srv.URL+"/webhook/g1/s1", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("multipart 推送应 502（发送失败）, got %d", resp.StatusCode)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("multipart 应落盘图床 1 个文件: %v", ents)
	}

	// 错误 sign → 403；GET → 405
	resp2, _ := http.Post(srv.URL+"/webhook/g1/wrong", "text/plain", strings.NewReader("x"))
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Errorf("错误 sign 应 403, got %d", resp2.StatusCode)
	}
	get, _ := http.Get(srv.URL + "/webhook/g1/s1")
	get.Body.Close()
	if get.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET 应 405, got %d", get.StatusCode)
	}
}

func TestAllowedIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/webhook/g/s", nil)
	req.RemoteAddr = "1.2.3.4:5678"
	if !allowedIP(nil, req) {
		t.Error("空白名单应放行")
	}
	if allowedIP([]string{"10.0.0.1"}, req) {
		t.Error("不在白名单应拒绝")
	}
	if !allowedIP([]string{"1.2.3.4"}, req) {
		t.Error("单 IP 精确匹配应放行")
	}
	if !allowedIP([]string{"1.2.0.0/16"}, req) {
		t.Error("CIDR 匹配应放行")
	}
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.9")
	if !allowedIP([]string{"1.2.3.4"}, req) {
		t.Error("应取 X-Forwarded-For 首个 IP")
	}
}

// TestWebhookInstallOwnerOnly 验证 /webhook 注册为 OwnerOnly：非主人视角的 help
// 与自动菜单/面板里都看不到它（命令本体在 internal/bot 的 owner 测试里覆盖）。
func TestWebhookInstallOwnerOnly(t *testing.T) {
	b := bot.New(bot.Config{}, bot.WithPrefixes("/"), bot.WithOwners("boss"))
	NewWebhook(t.TempDir()+"/webhook.json", "", "", nil).Install(b)

	stranger := bot.NewEventCtx(b, "c2c", "help", "u9", "", "", "m", "", false)
	if help := b.HelpFor(stranger); strings.Contains(help, "webhook") || strings.Contains(help, "推送地址") {
		t.Errorf("非主人视角的 help 不应包含 webhook: %s", help)
	}
	owner := bot.NewEventCtx(b, "c2c", "help", "boss", "", "", "m", "", false)
	if help := b.HelpFor(owner); !strings.Contains(help, "webhook") {
		t.Errorf("主人视角的 help 应包含 webhook: %s", help)
	}
	for _, it := range b.AutoMenuConfig().Menu.Items {
		if it.SendMessage == "webhook" {
			t.Errorf("自动菜单不应包含 OwnerOnly 命令: %+v", it)
		}
	}
}

// TestWebhookGenRouting 验证生成/作废的目标群解析与状态流转：群里触发=本群、
// 私聊带参=参数群、私聊免参=最近记忆、无记忆不动状态；off 作废签名并清记忆。
// 回复与私聊发送在测试里因未连接 api 而是 no-op，这里只验证状态侧。
func TestWebhookGenRouting(t *testing.T) {
	b := bot.New(bot.Config{}, bot.WithPrefixes("/"))
	p := NewWebhook(t.TempDir()+"/webhook.json", "", "", nil)
	p.Install(b)

	run := func(source, groupID, userID string, args ...string) {
		c := bot.NewEventCtx(b, source, "webhook", userID, "", groupID, "m", "", false)
		c.Args = bot.Args{Args: args}
		p.cmd(c)
	}

	// 群里触发生成：g1 有签名，且记为 u1 的最近目标群
	run("group", "g1", "u1")
	p.mu.Lock()
	sign1, last := p.conf.Groups["g1"], p.lastGroup["u1"]
	p.mu.Unlock()
	if len(sign1) != 32 { // 16 字节 hex
		t.Errorf("群里触发应为 g1 生成签名, got %q", sign1)
	}
	if last != "g1" {
		t.Errorf("lastGroup[u1] = %q, want g1", last)
	}

	// 私聊免参：用记忆的 g1 重新生成（签名应变化）
	run("c2c", "", "u1")
	p.mu.Lock()
	sign2 := p.conf.Groups["g1"]
	p.mu.Unlock()
	if sign2 == "" || sign2 == sign1 {
		t.Errorf("私聊免参应基于记忆重新生成 g1 签名: %q -> %q", sign1, sign2)
	}

	// 私聊裸带群 id（私聊兜底路径提示的形式）：为 g2 生成
	run("c2c", "", "u1", "g2")
	p.mu.Lock()
	signG2 := p.conf.Groups["g2"]
	p.mu.Unlock()
	if len(signG2) != 32 {
		t.Errorf("私聊 /webhook g2 应为 g2 生成签名, got %q", signG2)
	}

	// 无记忆的用户私聊免参：无目标群，不产生任何状态
	run("c2c", "", "u9")
	p.mu.Lock()
	_, emptyGroup := p.conf.Groups[""]
	u9last := p.lastGroup["u9"]
	p.mu.Unlock()
	if emptyGroup || u9last != "" {
		t.Errorf("无目标群时不应写状态: groups[\"\"]=%v lastGroup[u9]=%q", emptyGroup, u9last)
	}

	// off g1：作废 g1 签名；记忆指向 g2，不应被误清
	run("group", "g1", "u1", "off")
	p.mu.Lock()
	_, g1Exists := p.conf.Groups["g1"]
	last = p.lastGroup["u1"]
	p.mu.Unlock()
	if g1Exists {
		t.Error("off 应作废 g1 的签名")
	}
	if last != "g2" {
		t.Errorf("off g1 不应清掉指向 g2 的记忆, got %q", last)
	}

	// off g2：作废签名，记忆随之清空
	run("group", "g2", "u1", "off")
	p.mu.Lock()
	_, g2Exists := p.conf.Groups["g2"]
	last = p.lastGroup["u1"]
	p.mu.Unlock()
	if g2Exists {
		t.Error("off 应作废 g2 的签名")
	}
	if last != "" {
		t.Errorf("off g2 后 lastGroup[u1] 应清空, got %q", last)
	}
}

// TestWebhookConfSaveRoundTrip 落盘字段（groups/whitelist）不因新增 imgHost 而变。
func TestWebhookConfSaveRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "webhook.json")
	p := NewWebhook(file, "", "", nil)
	p.conf = webhookConf{Groups: map[string]string{}} // 正常经 Install→load 初始化，这里直连
	p.mu.Lock()
	p.conf.Groups["g"] = "s"
	p.conf.Whitelist = []string{"1.2.3.4"}
	p.save()
	p.mu.Unlock()

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var c webhookConf
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	if c.Groups["g"] != "s" || len(c.Whitelist) != 1 {
		t.Errorf("落盘内容异常: %s", data)
	}

	p2 := NewWebhook(file, "", "", nil)
	if err := p2.load(); err != nil {
		t.Fatal(err)
	}
	p2.mu.Lock()
	ok := p2.conf.Groups["g"] == "s"
	p2.mu.Unlock()
	if !ok {
		t.Error("重载后应恢复 groups")
	}
}
