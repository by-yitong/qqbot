package bot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"qqbot/pkg/botgo/dto"
)

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

func TestExtractMentionsAndMentionsAccessor(t *testing.T) {
	b := New(Config{})
	m := &dto.Message{
		Content: "禁言 <@M1> 10分钟",
		Author:  &dto.User{ID: "U1", Username: "管理员"},
		Mentions: []*dto.User{
			{ID: "M1", Username: "小明"},
			{ID: "", Username: "空id"}, // 跳过
			{ID: "B1", Username: "机器人自己"},
		},
	}
	c := b.fromMessage(context.Background(), "group", m, true)

	// api 未就绪 → selfOpenID 为空 → 不排除任何 mention
	ms := c.Mentions()
	if len(ms) != 2 || ms[0].ID != "M1" || ms[0].Username != "小明" || ms[1].ID != "B1" {
		t.Fatalf("Mentions()=%+v", ms)
	}

	// 自身 openid 已知（模拟 Me() 已缓存）→ 排除 B1
	b.selfMu.Lock()
	b.selfID = "B1"
	b.selfMu.Unlock()
	ms = c.Mentions()
	if len(ms) != 1 || ms[0].ID != "M1" {
		t.Fatalf("排除自身后 Mentions()=%+v", ms)
	}

	// 无 mentions → nil
	c2 := b.fromMessage(context.Background(), "group", &dto.Message{Author: &dto.User{ID: "U1"}}, false)
	if got := c2.Mentions(); len(got) != 0 {
		t.Errorf("无 mentions 应为空，得到 %+v", got)
	}
}
