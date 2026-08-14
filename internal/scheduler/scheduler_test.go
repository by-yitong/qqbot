package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newTestSched(t *testing.T) *Scheduler {
	t.Helper()
	return New(filepath.Join(t.TempDir(), "tasks.json"), loc, 2, 10)
}

func TestAddRejectsPastOnce(t *testing.T) {
	s := newTestSched(t)
	_, err := s.Add(Task{
		Name: "过期", Handler: HandlerAgent, Target: &Target{Source: "c2c", ID: "U1"},
		Schedule: Schedule{Type: ScheduleOnce, RunAt: time.Now().Add(-time.Hour).Format(time.RFC3339)},
	})
	if err == nil {
		t.Fatal("过去的 once 任务应被拒绝")
	}
}

func TestAddCaps(t *testing.T) {
	s := newTestSched(t) // maxPerChat=2, maxTotal=10
	mk := func() Task {
		return Task{
			Name: "n", Handler: HandlerAgent, Target: &Target{Source: "c2c", ID: "U1"},
			Schedule: Schedule{Type: ScheduleDaily, At: "08:00"},
		}
	}
	if _, err := s.Add(mk()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(mk()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(mk()); err == nil {
		t.Fatal("单会话第 3 个任务应超上限")
	}
	// 插件任务不计上限
	s.Ensure(Task{Name: "插件任务", Handler: "x.y", Schedule: Schedule{Type: ScheduleDaily, At: "08:00"}})
	if _, err := s.Add(Task{
		Name: "换个聊天", Handler: HandlerAgent, Target: &Target{Source: "c2c", ID: "U2"},
		Schedule: Schedule{Type: ScheduleDaily, At: "08:00"},
	}); err != nil {
		t.Fatalf("另一个聊天不应受限: %v", err)
	}
}

func TestTickFiresAndReschedules(t *testing.T) {
	s := newTestSched(t)
	fired := make(chan Task, 4)
	s.Handle(HandlerAgent, func(ctx context.Context, task Task) error {
		fired <- task
		return nil
	})
	created, err := s.Add(Task{
		Name: "每日", Handler: HandlerAgent, Target: &Target{Source: "c2c", ID: "U1"},
		Schedule: Schedule{Type: ScheduleDaily, At: "08:00"}, Payload: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 把 NextRun 拨到过去，模拟到点
	s.mu.Lock()
	s.tasks[created.ID].NextRun = time.Now().Add(-time.Minute)
	s.mu.Unlock()
	s.tick(time.Now())

	select {
	case task := <-fired:
		if task.ID != created.ID || task.Payload != "hi" {
			t.Errorf("收到任务字段不对: %+v", task)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("到点未触发")
	}

	// daily 触发后应排到明天
	tickTime := time.Now().Add(-time.Minute)
	s.mu.Lock()
	next := s.tasks[created.ID].NextRun
	s.mu.Unlock()
	if !next.After(tickTime) {
		t.Errorf("触发后应已排下次: %v", next)
	}

	// 等收尾完成，校验统计
	waitFor(t, func() bool {
		for _, ts := range s.List() {
			return ts.RunCount == 1 && ts.LastError == "" && !ts.LastRun.IsZero()
		}
		return false
	})
}

func TestTickIntervalSlotBased(t *testing.T) {
	s := newTestSched(t)
	created, err := s.Add(Task{
		Name: "间隔", Handler: "h", Schedule: Schedule{Type: ScheduleInterval, EveryMins: 30},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 首次 = now+30m；模拟停机 70 分钟后恢复：应滚到「原槽位+30 的整数倍且 >now」
	now := time.Now().Add(70 * time.Minute)
	s.mu.Lock()
	s.tasks[created.ID].NextRun = time.Now() // 原 NextRun 就是创建时刻附近
	s.mu.Unlock()
	s.mu.Lock()
	next, err := s.nextAfterFire(s.tasks[created.ID], now)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if diff := next.Sub(now); diff <= 0 || diff > 30*time.Minute {
		t.Errorf("槽位累加结果应在 (now, now+30m] 内: %v (diff=%v)", next, diff)
	}
}

func TestOnceTaskDoneAfterRun(t *testing.T) {
	s := newTestSched(t)
	s.Handle("h", func(ctx context.Context, task Task) error { return nil })
	created, err := s.Add(Task{
		Name: "单次", Handler: "h",
		Schedule: Schedule{Type: ScheduleOnce, RunAt: time.Now().Add(time.Minute).Format(time.RFC3339)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RunNow(created.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		for _, ts := range s.List() {
			return ts.RunCount == 1
		}
		return false
	})
	for _, ts := range s.List() {
		if ts.Done {
			t.Error("试跑不应把 once 任务置 Done")
		}
	}

	// 正式触发路径：置 Done 后 tick 不再触发
	s.mu.Lock()
	s.tasks[created.ID].NextRun = time.Now().Add(-time.Second)
	s.mu.Unlock()
	s.tick(time.Now())
	waitFor(t, func() bool {
		for _, ts := range s.List() {
			return ts.Done
		}
		return false
	})
	s.tick(time.Now()) // Done 后再 tick：无 handler 调用（若有会 panic 于已满 channel？不会，直接验证不炸）
}

func TestRecoverMissedSkipsOld(t *testing.T) {
	s := newTestSched(t)
	created, err := s.Add(Task{
		Name: "每日", Handler: "h", Schedule: Schedule{Type: ScheduleDaily, At: "08:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// NextRun 拨到 1 小时前（超过 5 分钟宽限）→ recoverMissed 应滚到未来
	s.mu.Lock()
	s.tasks[created.ID].NextRun = time.Now().Add(-time.Hour)
	s.mu.Unlock()
	s.recoverMissed()
	for _, ts := range s.List() {
		if ts.ID != created.ID {
			continue
		}
		if !ts.NextRun.After(time.Now()) {
			t.Errorf("错过的每日任务应滚到未来: %v", ts.NextRun)
		}
	}
}

func TestDeleteFiresHooks(t *testing.T) {
	s := newTestSched(t)
	created, _ := s.Add(Task{Name: "删我", Handler: "h", Schedule: Schedule{Type: ScheduleDaily, At: "08:00"}})
	hooked := make(chan Task, 1)
	s.OnDelete(func(task Task) { hooked <- task })
	if _, ok := s.Delete(created.ID); !ok {
		t.Fatal("删除失败")
	}
	select {
	case task := <-hooked:
		if task.ID != created.ID {
			t.Errorf("钩子收到错任务: %v", task.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("OnDelete 未触发")
	}
	if _, ok := s.Delete("t999"); ok {
		t.Error("删除不存在的任务应返回 false")
	}
}

func TestHandlerErrorRecorded(t *testing.T) {
	s := newTestSched(t)
	boom := errors.New("发送失败")
	s.Handle("h", func(ctx context.Context, task Task) error { return boom })
	created, _ := s.Add(Task{Name: "会失败", Handler: "h", Schedule: Schedule{Type: ScheduleDaily, At: "08:00"}})
	if err := s.RunNow(created.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		for _, ts := range s.List() {
			return ts.LastError == boom.Error()
		}
		return false
	})
}

func TestPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.json")
	s1 := New(path, loc, 5, 50)
	s1.Handle("h", func(ctx context.Context, task Task) error { return nil })
	created, err := s1.Add(Task{
		Name: "重启存活", Handler: "h",
		Schedule: Schedule{Type: ScheduleWeekly, At: "09:30", Days: []int{1, 2, 3, 4, 5}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s2 := New(path, loc, 5, 50) // 模拟重启：从盘上加载
	found := false
	for _, ts := range s2.List() {
		if ts.ID == created.ID && ts.Name == "重启存活" && ts.Enabled {
			found = true
		}
	}
	if !found {
		t.Error("重启后任务应恢复")
	}
	// ID 自增不复用
	next, err := s2.Add(Task{Name: "又一个", Handler: "h", Schedule: Schedule{Type: ScheduleDaily, At: "08:00"}})
	if err != nil {
		t.Fatal(err)
	}
	if next.ID == created.ID {
		t.Errorf("ID 不应复用: %s", next.ID)
	}
}

func TestChatInfoCtx(t *testing.T) {
	ctx := context.Background()
	if _, ok := ChatInfoFrom(ctx); ok {
		t.Error("未注入应返回 false")
	}
	info := ChatInfo{Target: Target{Source: "group", ID: "G1"}, UserID: "U1"}
	got, ok := ChatInfoFrom(WithChatInfo(ctx, info))
	if !ok || got.Target.ID != "G1" || got.UserID != "U1" {
		t.Errorf("注入后取回不一致: %+v, %v", got, ok)
	}
}

// waitFor 轮询等待条件成立（finish 在独立 goroutine 收尾，测试需要等它落定）。
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}
