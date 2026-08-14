package tools

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qqbot/internal/imgstore"
)

func newImageGen(t *testing.T, srv *httptest.Server) *ImageGen {
	t.Helper()
	return &ImageGen{
		APIKey:  "testkey",
		BaseURL: srv.URL,
		Model:   "gpt-image-test",
		HTTP:    srv.Client(),
	}
}

func TestGenerateB64ReturnsGenimgRef(t *testing.T) {
	// 网关回 b64_json：工具应解码存图床，返回 genimg: 引用。
	b64 := base64.StdEncoding.EncodeToString([]byte("PNGDATA"))
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Write([]byte(`{"data":[{"b64_json":"` + b64 + `","revised_prompt":"a red circle"}],"output_format":"png"}`))
	}))
	defer srv.Close()

	g := newImageGen(t, srv)
	ref, revised, err := g.Generate(context.Background(), "circle", "1024x1024")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.HasPrefix(ref, imgstore.Scheme) {
		t.Errorf("b64 响应应返回 genimg: 引用, got %q", ref)
	}
	if revised != "a red circle" {
		t.Errorf("revised=%q", revised)
	}
	if gotAuth != "Bearer testkey" {
		t.Errorf("Authorization=%q", gotAuth)
	}
	if !strings.Contains(gotBody, `"model":"gpt-image-test"`) || !strings.Contains(gotBody, `"prompt":"circle"`) {
		t.Errorf("请求体缺 model/prompt: %q", gotBody)
	}
	// 引用经图床能取回原字节
	data, _, _, ok := imgstore.Default().Resolve(ref)
	if !ok || string(data) != "PNGDATA" {
		t.Errorf("图床取回应为 PNGDATA, ok=%v data=%q", ok, string(data))
	}
}

func TestGenerateB64WithHostReturnsURL(t *testing.T) {
	// 配了图床：b64 → 解码 → 上传图床 → 返回公网 URL（不是 genimg: 引用）。
	b64 := base64.StdEncoding.EncodeToString([]byte("PNGDATA"))
	genSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[{"b64_json":"` + b64 + `"}],"output_format":"png"}`))
	}))
	defer genSrv.Close()

	hostSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/upload" {
			t.Errorf("图床应被 POST /upload, got %q", r.URL.Path)
		}
		w.Write([]byte(`{"url":"https://img.example.com/xyz.png","id":"xyz"}`))
	}))
	defer hostSrv.Close()

	g := newImageGen(t, genSrv)
	g.ImageHost = &imgstore.Host{BaseURL: hostSrv.URL, Token: "tok"}
	ref, _, err := g.Generate(context.Background(), "cat", "1024x1024")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if ref != "https://img.example.com/xyz.png" {
		t.Errorf("配图床时应返回公网 URL, got %q", ref)
	}
}

func TestGenerateURLReturned(t *testing.T) {
	// 网关回 url：工具应直接返回该 url（不存图床）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"url":"https://cdn/x.png"}]}`))
	}))
	defer srv.Close()

	g := newImageGen(t, srv)
	ref, _, err := g.Generate(context.Background(), "cat", "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if ref != "https://cdn/x.png" {
		t.Errorf("url 响应应原样返回, got %q", ref)
	}
}

func TestGenerateAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"error":{"message":"invalid model"}}`))
	}))
	defer srv.Close()

	g := newImageGen(t, srv)
	if _, _, err := g.Generate(context.Background(), "x", ""); err == nil {
		t.Fatal("error 响应应报错")
	} else if !strings.Contains(err.Error(), "invalid model") {
		t.Errorf("错误应含 API 信息, got %v", err)
	}
}

func TestGenerateHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()

	g := newImageGen(t, srv)
	if _, _, err := g.Generate(context.Background(), "x", ""); err == nil {
		t.Fatal("HTTP 401 应报错")
	}
}

func TestEditMultipart(t *testing.T) {
	// 图生图：验证 multipart 含 image 文件字段、prompt、model。
	var gotCT, gotPrompt, gotModel string
	var imageBytes []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotPrompt = r.FormValue("prompt")
		gotModel = r.FormValue("model")
		f, _, err := r.FormFile("image")
		if err == nil {
			imageBytes, _ = io.ReadAll(f)
			f.Close()
		}
		b64 := base64.StdEncoding.EncodeToString([]byte("RESULT"))
		w.Write([]byte(`{"data":[{"b64_json":"` + b64 + `"}]}`))
	}))
	defer srv.Close()

	g := newImageGen(t, srv)
	// 先把输入图存进图床，用引用作 image_url
	inRef := imgstore.Default().Put([]byte("INPUTIMG"), "png")
	ref, _, err := g.Edit(context.Background(), "make it blue", "1024x1024", inRef)
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if !strings.HasPrefix(ref, imgstore.Scheme) {
		t.Errorf("应返回 genimg: 引用, got %q", ref)
	}
	if !strings.HasPrefix(gotCT, "multipart/form-data") {
		t.Errorf("Content-Type 应为 multipart: %q", gotCT)
	}
	if gotPrompt != "make it blue" || gotModel != "gpt-image-test" {
		t.Errorf("prompt=%q model=%q", gotPrompt, gotModel)
	}
	if string(imageBytes) != "INPUTIMG" {
		t.Errorf("image 字段字节=%q want INPUTIMG", string(imageBytes))
	}
}

func TestGenerateImageTool(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("IMG"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"b64_json":"` + b64 + `"}]}`))
	}))
	defer srv.Close()

	tool := &GenerateImage{Owner: newImageGen(t, srv)}
	// 文生图
	out, err := tool.Execute(context.Background(), `{"prompt":"a cat","size":"1024x1024"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "已生成图片") || !strings.Contains(out, imgstore.Scheme) {
		t.Errorf("文生图输出应含引用: %q", out)
	}
	if !strings.Contains(out, `send_image(url="`) {
		t.Errorf("应提示用 send_image 发送: %q", out)
	}
	// 缺 prompt
	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Fatal("缺 prompt 应报错")
	}
}
