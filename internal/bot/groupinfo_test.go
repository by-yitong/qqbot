package bot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

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
		t.Fatalf("应聚合 2 条策略，实际 %d", len(list))
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
