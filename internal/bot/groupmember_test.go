package bot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// memberSrv 假 QQ API：按请求特征回包并记录调用。
type memberSrv struct {
	mu     sync.Mutex
	calls  []string // "METHOD path?query"
	bodies []string
}

func (s *memberSrv) record(r *http.Request, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, r.Method+" "+r.URL.RequestURI())
	s.bodies = append(s.bodies, body)
}

// TestListGroupMembersPagination 成员列表自动翻页：cursor 透传、末页（next_cursor 空）停止。
func TestListGroupMembersPagination(t *testing.T) {
	s := &memberSrv{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.record(r, string(body))
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(`{"members":[{"member_openid":"M1","username":"小明","member_role":"owner","bot":false,` +
				`"joined_at":"2025-08-20T09:15:00+08:00","union_openid":"U1"}],"next_cursor":"CUR2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"members":[{"member_openid":"M2","username":"小红","member_role":"member","bot":true}],"next_cursor":""}`))
	}))
	t.Cleanup(srv.Close)
	old := qqAPIBase
	qqAPIBase = srv.URL
	t.Cleanup(func() { qqAPIBase = old })
	b := &Bot{tokenSource: &fakeTS{tok: "tok"}, appID: "APP"}

	ms, err := b.ListGroupMembers(context.Background(), "G1")
	if err != nil {
		t.Fatalf("ListGroupMembers: %v", err)
	}
	if len(s.calls) != 2 {
		t.Fatalf("应翻页 2 次，实际 %d 次: %v", len(s.calls), s.calls)
	}
	if s.calls[0] != "GET /v2/groups/G1/members" {
		t.Errorf("首页应不带 cursor: %s", s.calls[0])
	}
	if !strings.Contains(s.calls[1], "cursor=CUR2") {
		t.Errorf("第二页应带 cursor: %s", s.calls[1])
	}
	if len(ms) != 2 || ms[0].MemberOpenID != "M1" || ms[0].Username != "小明" ||
		ms[0].MemberRole != MemberRoleOwner || !strings.Contains(ms[0].JoinedAt, "2025-08-20") || ms[0].UnionOpenID != "U1" {
		t.Errorf("第一页解析: %+v", ms[0])
	}
	if ms[1].MemberOpenID != "M2" || ms[1].MemberRole != MemberRoleMember || !ms[1].Bot {
		t.Errorf("第二页解析: %+v", ms[1])
	}
}

// TestGetGroupMember 成员详情：路径拼 member_openid，字段解析。
func TestGetGroupMember(t *testing.T) {
	s := &memberSrv{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.record(r, string(body))
		if r.URL.Path != "/v2/groups/G1/members/M9" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"member_openid":"M9","username":"小明","member_role":"admin","bot":false,` +
			`"joined_at":"2025-08-20T09:15:00+08:00","union_openid":"B4C6"}`))
	}))
	t.Cleanup(srv.Close)
	old := qqAPIBase
	qqAPIBase = srv.URL
	t.Cleanup(func() { qqAPIBase = old })
	b := &Bot{tokenSource: &fakeTS{tok: "tok"}, appID: "APP"}

	m, err := b.GetGroupMember(context.Background(), "G1", "M9")
	if err != nil {
		t.Fatalf("GetGroupMember: %v", err)
	}
	if s.calls[0] != "GET /v2/groups/G1/members/M9" {
		t.Errorf("call: %s", s.calls[0])
	}
	if m.MemberRole != MemberRoleAdmin || m.Username != "小明" || m.UnionOpenID != "B4C6" {
		t.Errorf("member: %+v", m)
	}
}

// TestRemoveGroupMembers 批量移除：请求体、拉黑开关、响应解析与本地校验。
func TestRemoveGroupMembers(t *testing.T) {
	s := &memberSrv{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.record(r, string(body))
		_, _ = w.Write([]byte(`{"remove_members_result":"success","add_to_member_blacklist_fail_openids":["M2"]}`))
	}))
	t.Cleanup(srv.Close)
	old := qqAPIBase
	qqAPIBase = srv.URL
	t.Cleanup(func() { qqAPIBase = old })
	b := &Bot{tokenSource: &fakeTS{tok: "tok"}, appID: "APP"}

	res, err := b.RemoveGroupMembers(context.Background(), "G1", []string{"M1", "M2"}, true)
	if err != nil {
		t.Fatalf("RemoveGroupMembers: %v", err)
	}
	if s.calls[0] != "POST /v2/groups/G1/batch_remove_members" {
		t.Errorf("call: %s", s.calls[0])
	}
	if body := s.bodies[0]; !strings.Contains(body, `"member_openids":["M1","M2"]`) || !strings.Contains(body, `"add_to_member_blacklist":true`) {
		t.Errorf("body: %s", body)
	}
	if res.RemoveMembersResult != "success" || len(res.AddToMemberBlacklistFailOpenIDs) != 1 || res.AddToMemberBlacklistFailOpenIDs[0] != "M2" {
		t.Errorf("res: %+v", res)
	}

	// 不拉黑：add_to_member_blacklist 省略（omitempty）
	if _, err := b.RemoveGroupMembers(context.Background(), "G1", []string{"M1"}, false); err != nil {
		t.Fatalf("RemoveGroupMembers(false): %v", err)
	}
	if body := s.bodies[1]; strings.Contains(body, "add_to_member_blacklist") {
		t.Errorf("false 应省略拉黑字段: %s", body)
	}

	// 本地校验（不发请求）：空列表 / 超 20 人
	callsBefore := len(s.calls)
	if _, err := b.RemoveGroupMembers(context.Background(), "G1", nil, false); err == nil {
		t.Error("空列表应报错")
	}
	var tooMany []string
	for i := 0; i < 21; i++ {
		tooMany = append(tooMany, "M")
	}
	if _, err := b.RemoveGroupMembers(context.Background(), "G1", tooMany, false); err == nil {
		t.Error("超 20 人应报错")
	}
	if len(s.calls) != callsBefore {
		t.Errorf("本地校验失败不应发请求，calls=%d", len(s.calls))
	}
}

// TestListGroupBlacklist 黑名单查询：limit=100、cursor 翻页。
func TestListGroupBlacklist(t *testing.T) {
	s := &memberSrv{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.record(r, string(body))
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(`{"users":[{"union_openid":"U1","member_openid":"M1","username":"阳光少年",` +
				`"banned_at":"2025-07-01T10:30:00+08:00","bot":false}],"next_cursor":"C2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"users":[],"next_cursor":""}`))
	}))
	t.Cleanup(srv.Close)
	old := qqAPIBase
	qqAPIBase = srv.URL
	t.Cleanup(func() { qqAPIBase = old })
	b := &Bot{tokenSource: &fakeTS{tok: "tok"}, appID: "APP"}

	users, err := b.ListGroupBlacklist(context.Background(), "G1")
	if err != nil {
		t.Fatalf("ListGroupBlacklist: %v", err)
	}
	if !strings.Contains(s.calls[0], "limit=100") {
		t.Errorf("应带 limit=100: %s", s.calls[0])
	}
	if !strings.Contains(s.calls[1], "cursor=C2") {
		t.Errorf("第二页应带 cursor: %s", s.calls[1])
	}
	if len(users) != 1 || users[0].MemberOpenID != "M1" || users[0].Username != "阳光少年" || users[0].BannedAt == "" {
		t.Errorf("users: %+v", users)
	}
}

// TestSetGroupBlacklist 黑名单操作：请求体、fail_openids 解析、本地校验。
func TestSetGroupBlacklist(t *testing.T) {
	s := &memberSrv{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.record(r, string(body))
		_, _ = w.Write([]byte(`{"fail_openids":["M3"]}`))
	}))
	t.Cleanup(srv.Close)
	old := qqAPIBase
	qqAPIBase = srv.URL
	t.Cleanup(func() { qqAPIBase = old })
	b := &Bot{tokenSource: &fakeTS{tok: "tok"}, appID: "APP"}

	fail, err := b.SetGroupBlacklist(context.Background(), "G1", BlacklistOpAdd, []string{"M1", "M3"})
	if err != nil {
		t.Fatalf("SetGroupBlacklist: %v", err)
	}
	if s.calls[0] != "POST /v2/groups/G1/member_blacklist" {
		t.Errorf("call: %s", s.calls[0])
	}
	if body := s.bodies[0]; !strings.Contains(body, `"op":"add"`) || !strings.Contains(body, `"member_openids":["M1","M3"]`) {
		t.Errorf("body: %s", body)
	}
	if len(fail) != 1 || fail[0] != "M3" {
		t.Errorf("fail: %v", fail)
	}

	// 本地校验（不发请求）：非法 op / 空 / 超 20
	callsBefore := len(s.calls)
	if _, err := b.SetGroupBlacklist(context.Background(), "G1", "drop", []string{"M1"}); err == nil {
		t.Error("非法 op 应报错")
	}
	if _, err := b.SetGroupBlacklist(context.Background(), "G1", BlacklistOpDel, nil); err == nil {
		t.Error("空列表应报错")
	}
	var tooMany []string
	for i := 0; i < 21; i++ {
		tooMany = append(tooMany, "M")
	}
	if _, err := b.SetGroupBlacklist(context.Background(), "G1", BlacklistOpDel, tooMany); err == nil {
		t.Error("超 20 人应报错")
	}
	if len(s.calls) != callsBefore {
		t.Errorf("本地校验失败不应发请求，calls=%d", len(s.calls))
	}
}
