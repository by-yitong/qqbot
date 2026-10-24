package bot

// 协议已下沉 pkg/botgo，internal/bot 的协议测试经真实 openapi 实例打到 httptest
// 假 server（覆盖 constant.APIDomain 指向本机），验证委托链路与响应解析。

import (
	"net/http/httptest"
	"testing"

	"golang.org/x/oauth2"
	botgo "qqbot/pkg/botgo"
	"qqbot/pkg/botgo/constant"
)

// testBot 返回 api 指向 srv 的 Bot（静态 token；请求会带 X-Union-Appid: APP）。
// 测试结束自动还原 constant.APIDomain（全局变量，勿与 t.Parallel 混用）。
func testBot(t *testing.T, srv *httptest.Server) *Bot {
	t.Helper()
	old := constant.APIDomain
	constant.APIDomain = srv.URL
	t.Cleanup(func() { constant.APIDomain = old })
	b := New(Config{AppID: "APP"})
	b.api = botgo.NewOpenAPI("APP", oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: "tok", TokenType: "QQBot",
	}))
	return b
}
