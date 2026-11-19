package bot

// 图文卡片（msg_type=8）发送测试：经 testBot 打到 httptest 假 server，
// 校验组包（msg_type/card/锚点/seq）与客户端侧校验。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/by-yitong/botgo/dto"
)

func validCard() dto.CardContent {
	return dto.CardContent{
		Title:       "标题",
		Description: "描述",
		URL:         "https://example.com/a",
		PicURL:      "https://example.com/p.jpg",
	}
}

// cardSrv 记录 messages 端点收到的请求体。
func cardSrv(t *testing.T) (*httptest.Server, *dto.MessageToCreate) {
	t.Helper()
	var got dto.MessageToCreate
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("messages 请求体非 JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"ROBOT1.0_CARD","timestamp":"2026-11-04T10:00:00+08:00"}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestSendGroupCard(t *testing.T) {
	srv, got := cardSrv(t)
	b := testBot(t, srv)
	id, err := b.SendGroupCard(context.Background(), "G", "ANCHOR", validCard())
	if err != nil {
		t.Fatal(err)
	}
	if id != "ROBOT1.0_CARD" {
		t.Errorf("id=%q", id)
	}
	if got.MsgType != dto.CardMsg || got.Card == nil || got.Card.Type != "tuwen" {
		t.Errorf("组包错: msg_type=%v card=%+v", got.MsgType, got.Card)
	}
	if got.Card.Content.Title != "标题" || got.Card.Content.PicURL != "https://example.com/p.jpg" {
		t.Errorf("card content 错: %+v", got.Card.Content)
	}
	if got.MsgID != "ANCHOR" || got.MsgSeq == 0 {
		t.Errorf("锚点/序号错: msg_id=%q seq=%d", got.MsgID, got.MsgSeq)
	}
}

func TestSendC2CCard(t *testing.T) {
	srv, got := cardSrv(t)
	b := testBot(t, srv)
	if _, err := b.SendC2CCard(context.Background(), "U", "", validCard()); err != nil {
		t.Fatal(err)
	}
	if got.MsgID != "" || got.Card == nil {
		t.Errorf("主动消息应无锚点: %+v", got)
	}
}

func TestValidateCard(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*dto.CardContent)
	}{
		{"标题空", func(c *dto.CardContent) { c.Title = "" }},
		{"标题51字", func(c *dto.CardContent) { c.Title = strings.Repeat("字", 51) }},
		{"描述201字", func(c *dto.CardContent) { c.Description = strings.Repeat("字", 201) }},
		{"url非https", func(c *dto.CardContent) { c.URL = "http://example.com" }},
		{"pic_url超长", func(c *dto.CardContent) { c.PicURL = "https://" + strings.Repeat("a", 1100) }},
	}
	for _, tc := range cases {
		c := validCard()
		tc.mut(&c)
		if err := validateCard(c); err == nil {
			t.Errorf("%s: 应报错", tc.name)
		}
	}
	if err := validateCard(validCard()); err != nil {
		t.Errorf("合法卡片不应报错: %v", err)
	}
	// 边界值正好通过：50 字标题、200 字描述、1024 字符 url。
	c := validCard()
	c.Title = strings.Repeat("字", 50)
	c.Description = strings.Repeat("字", 200)
	c.URL = "https://" + strings.Repeat("a", 1024-8)
	if err := validateCard(c); err != nil {
		t.Errorf("边界值不应报错: %v", err)
	}
}

// 校验失败不应发出 HTTP 请求。
func TestSendGroupCardValidationNoCall(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++ }))
	t.Cleanup(srv.Close)
	b := testBot(t, srv)
	c := validCard()
	c.URL = "http://x"
	if _, err := b.SendGroupCard(context.Background(), "G", "", c); err == nil {
		t.Fatal("应报错")
	}
	if calls != 0 {
		t.Errorf("校验失败不应发请求, got %d 次", calls)
	}
}
