package bot

// 群管理 API 测试（对应 group.go）：群信息/机器人状态/审批策略共用 infoSrv，
// 成员与黑名单用 memberSrv，禁言用 muteSrv——均为 httptest 假 QQ API，
// 验证请求路径/请求体/自动翻页/本地校验。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- 群信息 / 机器人状态 / 自动审批策略 ---

// infoSrv 假 QQ API：群信息 / 机器人状态 / 自动审批策略列表（含翻页）。
type infoSrv struct {
	mu     sync.Mutex
	calls  []string
	cursor bool // 策略列表是否已翻过第一页
}

func (s *infoSrv) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, r.Method+" "+r.URL.Path)
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/info"):
		_, _ = w.Write([]byte(`{"group_openid":"G1","group_name":"读书分享会",` +
			`"group_finger_memo":"每周共读一本好书","group_class_text":"文化",` +
			`"group_tags":["阅读","文学"],"group_member_num":256}`))
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/bot_state"):
		_, _ = w.Write([]byte(`{"member_openid":"BOT1","joined_at":"2025-06-15T14:30:00+08:00",` +
			`"allow_proactive_msg":false,"recv_msg_setting":"only_mention","member_role":"admin"}`))
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/join_approval_strategy"):
		if !s.cursor && r.URL.Query().Get("cursor") == "" {
			s.cursor = true
			_, _ = w.Write([]byte(`{"strategies":[{"strategy_id":"ST1","group_openids":["G1"],` +
				`"whitelist_user_count":120,"is_enable":"on","expire_at":"2027-01-01T00:00:00+08:00",` +
				`"created_at":"2026-08-01T00:00:00+08:00","updated_at":"2026-08-20T00:00:00+08:00","remark":"老群员白名单"}],` +
				`"next_cursor":"CUR2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"strategies":[{"strategy_id":"ST2","group_ids":["123456"],` +
			`"is_enable":"off"}],"next_cursor":""}`))
	default:
		http.NotFound(w, r)
	}
}

func TestGroupInfoAndBotStateAPIs(t *testing.T) {
	s := &infoSrv{}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(srv.Close)
	b := testBot(t, srv)

	gi, err := b.GetGroupInfo(context.Background(), "G1")
	if err != nil {
		t.Fatalf("GetGroupInfo: %v", err)
	}
	if gi.GroupOpenID != "G1" || gi.GroupName != "读书分享会" || gi.GroupMemberNum != 256 ||
		len(gi.GroupTags) != 2 || gi.GroupClassText != "文化" || gi.GroupFingerMemo != "每周共读一本好书" {
		t.Errorf("GroupInfo 解析: %+v", gi)
	}

	st, err := b.GetGroupBotState(context.Background(), "G1")
	if err != nil {
		t.Fatalf("GetGroupBotState: %v", err)
	}
	if st.MemberOpenID != "BOT1" || st.MemberRole != "admin" || st.RecvMsgSetting != "only_mention" || st.AllowProactiveMsg {
		t.Errorf("GroupBotState 解析: %+v", st)
	}
	if !strings.HasPrefix(st.JoinedAt, "2025-06-15") {
		t.Errorf("JoinedAt: %q", st.JoinedAt)
	}
}

func TestListGroupJoinApprovalStrategies(t *testing.T) {
	s := &infoSrv{}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(srv.Close)
	b := testBot(t, srv)

	list, err := b.ListGroupJoinApprovalStrategies(context.Background())
	if err != nil {
		t.Fatalf("ListGroupJoinApprovalStrategies: %v", err)
	}
	// 自动翻页聚合 2 条；路径不带群 id
	if len(list) != 2 {
		t.Fatalf("应聚合 2 条策略，实际 %d 条", len(list))
	}
	if list[0].StrategyID != "ST1" || list[0].IsEnable != "on" || list[0].WhitelistUserCount != 120 ||
		len(list[0].GroupOpenIDs) != 1 || list[0].Remark != "老群员白名单" {
		t.Errorf("策略1 解析: %+v", list[0])
	}
	if list[1].StrategyID != "ST2" || list[1].IsEnable != "off" || len(list[1].GroupIDs) != 1 {
		t.Errorf("策略2 解析: %+v", list[1])
	}
	for _, c := range s.calls {
		if !strings.HasPrefix(c, "GET /v2/groups/join_approval_strategy") {
			t.Errorf("策略列表路径应不带群 id: %s", c)
		}
	}
}

// --- 成员与黑名单 ---

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
		w.Header().Set("Content-Type", "application/json")
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
	b := testBot(t, srv)

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
		w.Header().Set("Content-Type", "application/json")
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
	b := testBot(t, srv)

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
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		s.record(r, string(body))
		_, _ = w.Write([]byte(`{"remove_members_result":"success","add_to_member_blacklist_fail_openids":["M2"]}`))
	}))
	t.Cleanup(srv.Close)
	b := testBot(t, srv)

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
		w.Header().Set("Content-Type", "application/json")
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
	b := testBot(t, srv)

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
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		s.record(r, string(body))
		_, _ = w.Write([]byte(`{"fail_openids":["M3"]}`))
	}))
	t.Cleanup(srv.Close)
	b := testBot(t, srv)

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

// --- 禁言 ---

// muteSrv 假 QQ API：记录 restrict_chat_setting 调用。
type muteSrv struct {
	mu     sync.Mutex
	calls  []string
	bodies []string
}

func (s *muteSrv) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, r.Method+" "+r.URL.Path)
	buf := make([]byte, 8192)
	n, _ := r.Body.Read(buf)
	s.bodies = append(s.bodies, string(buf[:n]))
	_, _ = w.Write([]byte(`{}`))
}

func TestGroupMuteAPIs(t *testing.T) {
	s := &muteSrv{}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(srv.Close)
	b := testBot(t, srv)

	// 禁言：op=add + RFC3339 到期时间
	expire := time.Now().Add(10 * time.Minute).Truncate(time.Second)
	if err := b.MuteGroupMember(context.Background(), "G1", "M1", expire); err != nil {
		t.Fatalf("mute: %v", err)
	}
	if s.calls[0] != "POST /v2/groups/G1/restrict_chat_setting" {
		t.Errorf("call: %s", s.calls[0])
	}
	body := s.bodies[0]
	if !strings.Contains(body, `"op":"add"`) || !strings.Contains(body, `"member_openid":"M1"`) {
		t.Errorf("mute body: %s", body)
	}
	if !strings.Contains(body, `"mute_expire_at":"`+expire.Format(time.RFC3339)+`"`) {
		t.Errorf("mute expire 应为 RFC3339：%s", body)
	}

	// 解禁：op=del，mute_expire_at 省略（官方：del 时可传空串，省略同样合法）
	if err := b.UnmuteGroupMember(context.Background(), "G1", "M1"); err != nil {
		t.Fatalf("unmute: %v", err)
	}
	if !strings.Contains(s.bodies[1], `"op":"del"`) || strings.Contains(s.bodies[1], `"mute_expire_at"`) {
		t.Errorf("unmute body: %s", s.bodies[1])
	}

	// 本地校验（不发请求）：过去时间 / 超 30 天 / 空列表 / 超 10 人
	callsBefore := len(s.calls)
	if err := b.MuteGroupMember(context.Background(), "G1", "M1", time.Now().Add(-time.Minute)); err == nil {
		t.Error("过去时间应报错")
	}
	if err := b.MuteGroupMember(context.Background(), "G1", "M1", time.Now().Add(31*24*time.Hour)); err == nil {
		t.Error("超 30 天应报错")
	}
	if err := b.SetGroupMemberMutes(context.Background(), "G1", nil); err == nil {
		t.Error("空列表应报错")
	}
	if err := b.MuteGroupMember(context.Background(), "G1", "M1", time.Now().Add(30*24*time.Hour)); err != nil {
		t.Errorf("整 30 天应放行: %v", err)
	}
	var tooMany []MemberMuteState
	for i := 0; i < 11; i++ {
		tooMany = append(tooMany, MemberMuteState{Op: MuteOpAdd, MemberOpenID: "M"})
	}
	if err := b.SetGroupMemberMutes(context.Background(), "G1", tooMany); err == nil {
		t.Error("超 10 人应报错")
	}
	if len(s.calls) != callsBefore+1 { // 只有整 30 天那次真正发出
		t.Errorf("本地校验失败不应发请求，calls=%d", len(s.calls))
	}
}

// TestGetGroupMuteState 查询群禁言状态：全员禁言规则 + 禁言中成员名单。
func TestGetGroupMuteState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/restrict_chat_setting") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"global_rule":{"mode":"schedule",` +
			`"schedule_rules":[{"task_id":"T1","start_at":"2026-09-03T13:00:00+08:00","end_at":"2026-09-03T14:00:00+08:00","enabled":false}],` +
			`"recurring_rules":[{"task_id":"T2","weekdays":[1,2,3,4,5],"start_time":"09:00","end_time":"18:00","enabled":true}]},` +
			`"members":[{"member_openid":"M1","mute_expire_at":"2026-09-03T12:00:00+08:00","username":"小明","union_openid":"U1"}]}`))
	}))
	t.Cleanup(srv.Close)
	b := testBot(t, srv)

	st, err := b.GetGroupMuteState(context.Background(), "G1")
	if err != nil {
		t.Fatalf("GetGroupMuteState: %v", err)
	}
	if st.GlobalRule.Mode != MuteModeSchedule {
		t.Errorf("mode=%s", st.GlobalRule.Mode)
	}
	if len(st.GlobalRule.ScheduleRules) != 1 || st.GlobalRule.ScheduleRules[0].TaskID != "T1" || st.GlobalRule.ScheduleRules[0].Enabled {
		t.Errorf("schedule_rules: %+v", st.GlobalRule.ScheduleRules)
	}
	rr := st.GlobalRule.RecurringRules
	if len(rr) != 1 || rr[0].TaskID != "T2" || len(rr[0].Weekdays) != 5 || rr[0].StartTime != "09:00" || !rr[0].Enabled {
		t.Errorf("recurring_rules: %+v", rr)
	}
	if len(st.Members) != 1 || st.Members[0].MemberOpenID != "M1" || st.Members[0].Username != "小明" || st.Members[0].UnionOpenID != "U1" {
		t.Errorf("members: %+v", st.Members)
	}
}
