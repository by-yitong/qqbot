package plugins

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// newDouyinFixture 起一个覆盖 短链跳转/ttwid 签发/detail 三端点的测试服务。
// details 按 aweme_id 返回预置 JSON；403First 让 detail 首次请求回 403（验证重试）。
func newDouyinFixture(t *testing.T, details map[string]string, forbiddenFirst int32) (*DouyinClient, *httptest.Server) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/s/"):
			http.Redirect(w, r, "https://www.iesdouyin.com/share/video/1234567890/?mid=1", http.StatusFound)
		case r.URL.Path == "/ttwid/":
			w.Header().Set("Set-Cookie", "ttwid=fake-ttwid; Path=/")
			fmt.Fprintf(w, `{"message":"success"}`)
		case strings.HasPrefix(r.URL.Path, "/aweme/v1/web/aweme/detail/"):
			if atomic.AddInt32(&hits, 1) <= forbiddenFirst {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, "Blocked by ArgusSecurityPlugin")
				return
			}
			if r.Header.Get("Cookie") != "ttwid=fake-ttwid" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			id := r.URL.Query().Get("aweme_id")
			body, ok := details[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := NewDouyinClient()
	c.WebBase = srv.URL
	c.TtwidReg = srv.URL + "/ttwid/"
	return c, srv
}

const dyVideoDetail = `{"status_code":0,"aweme_detail":{
	"desc":"我答应过一位先生 #硬核动漫","media_type":4,"duration":396853,
	"author":{"nickname":"拿来吧你零食屋"},
	"statistics":{"digg_count":27432,"comment_count":72,"share_count":5203},
	"video":{"play_addr":{"uri":"v1e00fg","url_list":["https://v.douyinvod.example/v.mp4"],"data_size":42314184},
	"ratio":"default","bit_rate":[{"gear_name":"normal_1080_0","play_addr":{"data_size":42314184}}]},
	"images":[]}}`

const dyNoteDetail = `{"status_code":0,"aweme_detail":{
	"desc":"Zcode又又发鸡蛋了","media_type":2,"duration":0,
	"author":{"nickname":"🌈BY易仝"},
	"statistics":{"digg_count":26,"comment_count":12,"share_count":5},
	"video":{"play_addr":{"uri":"m","url_list":["https://music.example/bg.mp3"],"data_size":0}},
	"images":[{"url_list":["https://p.example/1.webp"]},{"url_list":["https://p.example/2.webp"]}]}}`

func TestDouyinParseVideo(t *testing.T) {
	c, srv := newDouyinFixture(t, map[string]string{"1234567890": dyVideoDetail}, 0)
	res, err := c.Parse(context.Background(), srv.URL+"/s/abc/")
	if err != nil {
		t.Fatal(err)
	}
	if res.VideoURL != "https://v.douyinvod.example/v.mp4" {
		t.Errorf("VideoURL: %s", res.VideoURL)
	}
	if res.Quality != "1080p" {
		t.Errorf("Quality: %s", res.Quality)
	}
	if res.Duration < 396.8 || res.Duration > 396.9 {
		t.Errorf("Duration(ms→s): %f", res.Duration)
	}
	if res.SizeStr != "42.3MB" {
		t.Errorf("SizeStr: %s", res.SizeStr)
	}
	if res.Author != "拿来吧你零食屋" || res.Like != 27432 || res.Share != 5203 {
		t.Errorf("元数据不对: %+v", res)
	}
}

func TestDouyinParseNote(t *testing.T) {
	c, srv := newDouyinFixture(t, map[string]string{"1234567890": dyNoteDetail}, 0)
	res, err := c.Parse(context.Background(), srv.URL+"/s/abc/")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Images) != 2 || res.Images[0] != "https://p.example/1.webp" {
		t.Errorf("图集解析不对: %+v", res.Images)
	}
	// 图文的 play_addr 是背景音乐，绝不能当视频发
	if res.VideoURL != "" {
		t.Errorf("图文不应有视频直链: %s", res.VideoURL)
	}
	if !strings.Contains(res.Summary(), "图集 2 张") {
		t.Errorf("简介应含图集张数: %s", res.Summary())
	}
}

func TestDouyinDeletedItem(t *testing.T) {
	c, srv := newDouyinFixture(t, map[string]string{"1234567890": `{"status_code":0,"aweme_detail":null,
		"filter_detail":{"detail_msg":"因作品权限或已被删除，无法观看"}}`}, 0)
	_, err := c.Parse(context.Background(), srv.URL+"/s/abc/")
	if err == nil || !strings.Contains(err.Error(), "已被删除") {
		t.Errorf("期望删除/私密错误, got %v", err)
	}
}

func TestDouyinRetryOn403(t *testing.T) {
	c, srv := newDouyinFixture(t, map[string]string{"1234567890": dyVideoDetail}, 1)
	res, err := c.Parse(context.Background(), srv.URL+"/s/abc/")
	if err != nil {
		t.Fatalf("403 后应换 ttwid 重试成功: %v", err)
	}
	if res.VideoURL == "" {
		t.Error("重试后应拿到结果")
	}
}

func TestDouyinBadShortLink(t *testing.T) {
	c, _ := newDouyinFixture(t, nil, 0)
	// 跳转目标没有作品 ID
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/user/abc", http.StatusFound)
	}))
	defer bad.Close()
	if _, err := c.Parse(context.Background(), bad.URL+"/x/"); err == nil || !strings.Contains(err.Error(), "未取到作品 ID") {
		t.Errorf("期望短链异常错误, got %v", err)
	}
}

// 直连失败时 parseDouyin 应退星之阁；两边都挂时错误信息带两边原因。
func TestParseDouyinFallback(t *testing.T) {
	xzg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"code":0,"data":{"author":{"name":"兜底作者"},"item":{"title":"兜底","url":"https://cdn/x.mp4"}}}`)
	}))
	defer xzg.Close()

	dead, _ := newDouyinFixture(t, map[string]string{"1234567890": `{"status_code":8}`}, 0)
	p := &VideoPlugin{apiBase: xzg.URL, dy: dead}
	res, err := p.parseDouyin(context.Background(), dead.WebBase+"/s/abc/")
	if err != nil {
		t.Fatal(err)
	}
	if res.Author != "兜底作者" {
		t.Errorf("应走星之阁兜底: %+v", res)
	}

	// 两边都失败
	p.apiBase = "http://127.0.0.1:1"
	_, err = p.parseDouyin(context.Background(), dead.WebBase+"/s/abc/")
	if err == nil || !strings.Contains(err.Error(), "抖音直连") || !strings.Contains(err.Error(), "星之阁") {
		t.Errorf("错误应含两边原因: %v", err)
	}
}
