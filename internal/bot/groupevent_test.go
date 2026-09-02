package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tencent-connect/botgo/dto"
)

// 官方文档示例（截断）改写的事件 payload。
const memberAddPayload = `{"op":0,"id":"EV1","t":"GROUP_MEMBER_ADD","d":{` +
	`"timestamp":1784276757,"group_openid":"G1","member_openid":"M1","user_openid":"U1"}}`

const joinReqPayload = `{"op":0,"id":"EV2","t":"GROUP_JOIN_REQUEST","d":{` +
	`"group_openid":"G1","join_request_id":"JR1","member_openid":"M2","username":"小明",` +
	`"apply_at":"2026-08-05T16:21:40+08:00","apply_source":"self_apply",` +
	`"verify_info":{"method":"verify_message","verify_message":"就快乐了"}}}`

func TestOnGroupMemberAddDispatch(t *testing.T) {
	b := New(Config{})
	var got []*GroupMemberAddEvent
	done := make(chan struct{}, 1)
	b.OnGroupMemberAdd(func(e *GroupMemberAddEvent) {
		got = append(got, e)
		done <- struct{}{}
	})
	if err := b.onGroupMemberAdd(nil, []byte(memberAddPayload)); err != nil {
		t.Fatalf("onGroupMemberAdd: %v", err)
	}
	<-done
	if len(got) != 1 {
		t.Fatalf("应触发 1 次，实际 %d", len(got))
	}
	e := got[0]
	if e.GroupOpenID != "G1" || e.MemberOpenID != "M1" || e.Timestamp != 1784276757 {
		t.Errorf("解析错误: %+v", e)
	}
}

func TestOnGroupJoinRequestDispatch(t *testing.T) {
	b := New(Config{})
	var got []*GroupJoinRequestEvent
	done := make(chan struct{}, 1)
	b.OnGroupJoinRequest(func(e *GroupJoinRequestEvent) {
		got = append(got, e)
		done <- struct{}{}
	})
	if err := b.onGroupJoinRequest(nil, []byte(joinReqPayload)); err != nil {
		t.Fatalf("onGroupJoinRequest: %v", err)
	}
	<-done
	if len(got) != 1 {
		t.Fatalf("应触发 1 次，实际 %d", len(got))
	}
	e := got[0]
	if e.JoinRequestID != "JR1" || e.Username != "小明" || e.ApplySource != "self_apply" {
		t.Errorf("解析错误: %+v", e)
	}
	if ans := e.Answers(); len(ans) != 1 || ans[0] != "就快乐了" {
		t.Errorf("Answers()=%v", ans)
	}
}

func TestJoinRequestAnswers(t *testing.T) {
	// 问答模式：每题一条答案
	e := GroupJoinRequestEvent{VerifyInfo: &JoinRequestVerifyInfo{
		Method: "admin_review_qa",
		ReviewQAList: []JoinRequestReviewQA{
			{Question: "1+1", Answer: "2"},
			{Question: "群规在哪", Answer: "看置顶"},
		},
	}}
	if ans := e.Answers(); len(ans) != 2 || ans[0] != "2" || ans[1] != "看置顶" {
		t.Errorf("Answers()=%v", ans)
	}
	// 被邀请（无验证信息）→ 无答案
	if ans := (&GroupJoinRequestEvent{ApplySource: "invited"}).Answers(); ans != nil {
		t.Errorf("被邀请不应有答案，得到 %v", ans)
	}
	// 未知方式 → 无答案
	if ans := (&GroupJoinRequestEvent{VerifyInfo: &JoinRequestVerifyInfo{Method: "other"}}).Answers(); ans != nil {
		t.Errorf("未知方式不应有答案，得到 %v", ans)
	}
}

func TestWantGroupMemberEvent(t *testing.T) {
	b := New(Config{})
	if b.wantGroupMemberEvent() {
		t.Error("未注册监听器时不应申请群成员事件 intent")
	}
	b.OnGroupMemberAdd(func(*GroupMemberAddEvent) {})
	if !b.wantGroupMemberEvent() {
		t.Error("注册监听器后应申请群成员事件 intent")
	}
	if IntentGroupMemberEvent != dto.Intent(1<<24) {
		t.Errorf("IntentGroupMemberEvent 应为 1<<24，实际 %d", int(IntentGroupMemberEvent))
	}
}

// joinSrv 假 QQ API：审批 + 申请列表端点。
type joinSrv struct {
	mu      sync.Mutex
	calls   []string
	bodies  []string
	cursor1 bool // 列表是否翻页
}

func (s *joinSrv) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, r.Method+" "+r.URL.Path)
	buf := make([]byte, 4096)
	n, _ := r.Body.Read(buf)
	s.bodies = append(s.bodies, string(buf[:n]))
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/approval_join_request/M9"):
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/join_request_list"):
		if !s.cursor1 && r.URL.Query().Get("cursor") == "" {
			s.cursor1 = true
			_, _ = w.Write([]byte(`{"list":[{"join_request_id":"JR1","member_openid":"M2","username":"小明",` +
				`"apply_source":"self_apply","verify_info":{"method":"verify_message","verify_message":"答案A"}}],` +
				`"next_cursor":"CUR2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"list":[{"join_request_id":"JR2","member_openid":"M3","username":"小红","apply_source":"invited"}],"next_cursor":""}`))
	default:
		http.NotFound(w, r)
	}
}

func TestGroupJoinRequestAPIs(t *testing.T) {
	s := &joinSrv{}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(srv.Close)
	old := qqAPIBase
	qqAPIBase = srv.URL
	t.Cleanup(func() { qqAPIBase = old })
	b := &Bot{tokenSource: &fakeTS{tok: "tok"}, appID: "APP"}

	// 审批：approve 带 join_request_id；decline 带理由
	if err := b.ApproveGroupJoinRequest(context.Background(), "G1", "M9", "JR1", JoinReviewOpApprove, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := b.ApproveGroupJoinRequest(context.Background(), "G1", "M9", "JR2", JoinReviewOpDecline, "答案不对"); err != nil {
		t.Fatalf("decline: %v", err)
	}
	if !strings.Contains(s.bodies[0], `"op":"approve"`) || !strings.Contains(s.bodies[0], `"join_request_id":"JR1"`) {
		t.Errorf("approve body: %s", s.bodies[0])
	}
	if !strings.Contains(s.bodies[1], `"op":"decline"`) || !strings.Contains(s.bodies[1], `"reject_reason":"答案不对"`) {
		t.Errorf("decline body: %s", s.bodies[1])
	}
	// 非法动作
	if err := b.ApproveGroupJoinRequest(context.Background(), "G1", "M9", "", "kill", ""); err == nil {
		t.Error("非法 op 应报错")
	}

	// 列表：自动翻页聚合 2 条
	items, err := b.ListGroupJoinRequests(context.Background(), "G1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 2 || items[0].Username != "小明" || items[1].ApplySource != "invited" {
		t.Errorf("items=%+v", items)
	}
	if items[0].Answers()[0] != "答案A" {
		t.Errorf("Answers: %v", items[0].Answers())
	}
}

// 校验事件 JSON 能被 encoding/json 正常反序列化（结构体 tag 正确性）。
func TestJoinRequestJSONTags(t *testing.T) {
	raw := `{"group_openid":"G1","join_request_id":"JR1","risk_tips":"","union_openid":"U1",
	"member_openid":"M2","username":"小明","apply_at":"t","apply_source":"invited","invited_by":"M8",
	"bot":false,"verify_info":null,"auto_approved":{"strategy_id":"st_1"}}`
	var e GroupJoinRequestEvent
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if e.InvitedBy != "M8" || e.AutoApproved == nil || e.AutoApproved.StrategyID != "st_1" || e.VerifyInfo != nil {
		t.Errorf("解析: %+v", e)
	}
}
