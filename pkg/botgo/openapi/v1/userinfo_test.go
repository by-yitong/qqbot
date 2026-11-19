package v1

// 本地 fork 补充的用户基本信息接口测试（官方 2026-11-16 新增）：
// 经真实 openAPI 实例打到 httptest 假 server（覆盖 constant.APIDomain 指向本机）。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qqbot/pkg/botgo/dto"
)

func TestGetUserInfo(t *testing.T) {
	var gotPath, gotMethod, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"union_openid":"UOID","username":"小明","avatar":"https://avatar.example.com/a.png","allow_proactive_msg":true}`)
	}))
	t.Cleanup(srv.Close)

	api := testAPI(t, srv)
	info, err := api.GetUserInfo(context.Background(), "U1")
	if err != nil {
		t.Fatalf("GetUserInfo: %v", err)
	}
	if !strings.HasSuffix(gotPath, "/v2/users/U1/info") {
		t.Errorf("path=%q want /v2/users/U1/info", gotPath)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method=%q want GET", gotMethod)
	}
	if gotAuth != "QQBot tok" {
		t.Errorf("Authorization=%q want QQBot tok", gotAuth)
	}
	want := dto.UserInfo{
		UnionOpenID:       "UOID",
		Username:          "小明",
		Avatar:            "https://avatar.example.com/a.png",
		AllowProactiveMsg: true,
	}
	if *info != want {
		t.Errorf("UserInfo=%+v want %+v", *info, want)
	}
}

func TestGetUserInfoHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":40184001,"message":"与机器人无好友关系"}`))
	}))
	t.Cleanup(srv.Close)

	api := testAPI(t, srv)
	if _, err := api.GetUserInfo(context.Background(), "U1"); err == nil {
		t.Fatal("HTTP 400（40184001 无好友关系）应报错")
	}
}
