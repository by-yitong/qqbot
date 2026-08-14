package scheduler

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"sort"
	"sync"
	"time"
)

// HandlerFunc 是任务到点后的处理器：发送消息、跑 agent 等副作用在这里发生。
// 返回的 error 会记进 Task.LastError（/task 列表可见），不影响后续调度。
type HandlerFunc func(ctx context.Context, t Task) error

const (
	tickEvery   = 30 * time.Second // 调度循环粒度（daily/weekly 的触发误差上限）
	runTimeout  = 15 * time.Minute // 单次执行上限（handler 应尊重 ctx，超时只是兜底）
	missedGrace = 5 * time.Minute  // 重启恢复宽限：错过不超过该时长则照常触发一次
)

// Scheduler 是定时任务核心：任务表（内存）+ JSON 落盘 + 到点回调。
//
// 并发模型：一把 mu 保护全部状态；处理器在独立 goroutine 里执行（带 panic 恢复与
// 超时），每任务同一时刻最多一个实例在跑（running 表），重叠的触发直接跳过。
type Scheduler struct {
	loc        *time.Location
	maxPerChat int
	maxTotal   int

	mu       sync.Mutex
	st       *store
	nextID   int
	tasks    map[string]*Task
	running  map[string]bool
	handlers map[string]HandlerFunc
	onDelete []func(Task)
	loopCtx  context.Context // Run 启动后非 nil：随进程停止取消在跑的 handler
}

// New 创建调度器并加载落盘任务（坏文件自动备份从空开始，不阻塞启动）。
// maxPerChat/maxTotal 是「用户任务」的上限（插件声明的任务不计），<=0 用默认值。
func New(storePath string, loc *time.Location, maxPerChat, maxTotal int) *Scheduler {
	if loc == nil {
		loc = time.Local
	}
	if maxPerChat <= 0 {
		maxPerChat = 10
	}
	if maxTotal <= 0 {
		maxTotal = 100
	}
	s := &Scheduler{
		loc:        loc,
		maxPerChat: maxPerChat,
		maxTotal:   maxTotal,
		st:         &store{path: storePath},
		tasks:      map[string]*Task{},
		running:    map[string]bool{},
		handlers:   map[string]HandlerFunc{},
	}
	f, err := s.st.load()
	if err != nil {
		log.Printf("[task] %v", err)
	}
	s.nextID = f.NextID
	for i := range f.Tasks {
		t := f.Tasks[i]
		s.tasks[t.ID] = &t
	}
	if len(s.tasks) > 0 {
		log.Printf("[task] 已加载 %d 个定时任务", len(s.tasks))
	}
	return s
}

// Loc 返回计划所用的时区（展示/格式化下次运行时间用）。
func (s *Scheduler) Loc() *time.Location { return s.loc }

// Handle 注册名为 name 的处理器（插件在 Install 里调用；任务表里 Handler 引用它）。
func (s *Scheduler) Handle(name string, fn HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[name] = fn
}

// OnDelete 注册任务删除后的回调（在锁外执行；如清任务专属的 agent session）。
func (s *Scheduler) OnDelete(fn func(Task)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onDelete = append(s.onDelete, fn)
}

// Add 新增任务：校验计划、计上限、算 NextRun、落盘。agent 工具与命令走这里。
func (s *Scheduler) Add(t Task) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addLocked(t, t.Author != AuthorPlugin)
}

// Ensure 幂等声明一个插件默认任务：同名且 Author=plugin 的已存在则不动（保留用户
// 对开关/计划的修改），否则创建（不占用户上限）。插件 Install 里调用。
func (s *Scheduler) Ensure(t Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.tasks {
		if ex.Author == AuthorPlugin && ex.Name == t.Name {
			return
		}
	}
	t.Author = AuthorPlugin
	created, err := s.addLocked(t, false)
	if err != nil {
		log.Printf("[task] 声明插件任务 %q 失败: %v", t.Name, err)
		return
	}
	log.Printf("[task] 已声明插件任务 %s「%s」：%s", created.ID, created.Name, created.Schedule.Humanize())
}

// addLocked 是 Add/Ensure 的公共落库路径（调用方持 mu）。enforceCaps 时计数量上限。
func (s *Scheduler) addLocked(t Task, enforceCaps bool) (Task, error) {
	if err := t.Schedule.Validate(); err != nil {
		return Task{}, err
	}
	if t.Handler == "" {
		return Task{}, fmt.Errorf("缺少处理器（handler）")
	}
	if t.Handler == HandlerAgent && t.Target == nil {
		return Task{}, fmt.Errorf("agent 任务需要发送目标（target）")
	}
	if enforceCaps {
		if n := s.countUserTasksLocked(); n >= s.maxTotal {
			return Task{}, fmt.Errorf("任务总数已达上限 %d", s.maxTotal)
		}
		if n := s.countByTargetLocked(t.Target); n >= s.maxPerChat {
			return Task{}, fmt.Errorf("该会话的任务数已达上限 %d", s.maxPerChat)
		}
	}
	now := time.Now()
	next, err := t.Schedule.Next(now, s.loc)
	if err != nil {
		return Task{}, err
	}
	if t.Schedule.Type == ScheduleOnce && !next.After(now) {
		return Task{}, fmt.Errorf("run_at 已是过去时间: %s", t.Schedule.RunAt)
	}
	id := ""
	for {
		cand := fmt.Sprintf("t%d", s.nextID)
		s.nextID++
		if _, exists := s.tasks[cand]; !exists {
			id = cand
			break
		}
	}
	t.ID = id
	t.CreatedAt = now
	t.NextRun = next
	t.Enabled = true
	t.Done = false
	s.tasks[id] = &t
	s.persistLocked()
	return t, nil
}

// Delete 删除任务，并触发 OnDelete 回调（锁外执行）。返回被删任务。
func (s *Scheduler) Delete(id string) (Task, bool) {
	s.mu.Lock()
	t, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return Task{}, false
	}
	delete(s.tasks, id)
	s.persistLocked()
	var hooks []func(Task)
	hooks = append(hooks, s.onDelete...)
	s.mu.Unlock()
	for _, fn := range hooks {
		fn(*t)
	}
	return *t, true
}

// Toggle 启用/停用任务；重新启用时重算 NextRun。
func (s *Scheduler) Toggle(id string, on bool) (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, false
	}
	t.Enabled = on
	if on && !t.Done {
		if next, err := t.Schedule.Next(time.Now(), s.loc); err == nil {
			t.NextRun = next
		}
	}
	s.persistLocked()
	return *t, true
}

// List 返回全部任务副本（按下次运行时间升序，已完成/无时间的排后）。
func (s *Scheduler) List() []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, *t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Done != b.Done {
			return b.Done
		}
		if a.NextRun.IsZero() || b.NextRun.IsZero() {
			return !a.NextRun.IsZero()
		}
		if !a.NextRun.Equal(b.NextRun) {
			return a.NextRun.Before(b.NextRun)
		}
		return a.ID < b.ID
	})
	return out
}

// RunNow 立即试跑一次（异步执行；不影响 NextRun，once 任务也不置 Done）。
func (s *Scheduler) RunNow(id string) error {
	s.mu.Lock()
	_, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("任务 %s 不存在", id)
	}
	if s.running[id] {
		s.mu.Unlock()
		return fmt.Errorf("任务 %s 正在执行中", id)
	}
	s.running[id] = true
	s.mu.Unlock()
	go s.runOne(id, true)
	return nil
}

// Run 启动调度循环（挂 bot.OnStart，阻塞到 ctx 取消）。启动先做错过恢复：
// NextRun 在 now 之前且超过宽限期的周期任务直接滚到下个计划点、once 标过期——
// 重启/热重载不补跑，防刷屏；宽限期内（<=5 分钟）的照常触发。
func (s *Scheduler) Run(ctx context.Context) {
	s.mu.Lock()
	s.loopCtx = ctx
	s.mu.Unlock()
	s.recoverMissed()
	ticker := time.NewTicker(tickEvery)
	defer ticker.Stop()
	log.Printf("[task] 调度循环已启动（tick %s）", tickEvery)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.tick(now)
		}
	}
}

// recoverMissed 启动时校正过期 NextRun（语义见 Run）。宽限内的不动：下个 tick 自然触发。
func (s *Scheduler) recoverMissed() {
	now := time.Now()
	s.mu.Lock()
	for _, t := range s.tasks {
		switch {
		case !t.Enabled || t.Done:
		case t.NextRun.IsZero():
			next, err := t.Schedule.Next(now, s.loc)
			if err != nil {
				t.LastError = err.Error()
				t.Enabled = false
				continue
			}
			t.NextRun = next
		case t.NextRun.After(now):
		case now.Sub(t.NextRun) <= missedGrace:
			// 宽限内：保留，下个 tick 触发
		default:
			s.skipMissedLocked(t, now)
		}
	}
	s.persistLocked()
	s.mu.Unlock()
}

// skipMissedLocked 把错过的周期任务滚到下个计划点（调用方持 mu）。
// interval 从原槽位累加（不追跑错过的次数）；once 标 Done；daily/weekly 直接重算。
func (s *Scheduler) skipMissedLocked(t *Task, now time.Time) {
	old := t.NextRun
	if t.Schedule.Type == ScheduleInterval {
		next := t.NextRun
		for !next.After(now) {
			next = next.Add(time.Duration(t.Schedule.EveryMins) * time.Minute)
		}
		t.NextRun = next
	} else if t.Schedule.Type == ScheduleOnce {
		t.Done = true
		log.Printf("[task] %s(%s) 错过单次执行时间 %s，标记过期", t.ID, t.Name, old.Format("01-02 15:04"))
		return
	} else {
		next, err := t.Schedule.Next(now, s.loc)
		if err != nil {
			t.LastError = err.Error()
			t.Enabled = false
			return
		}
		t.NextRun = next
	}
	log.Printf("[task] %s(%s) 停机期间错过 %s，滚到 %s（不补跑）",
		t.ID, t.Name, old.Format("01-02 15:04"), t.NextRun.Format("01-02 15:04"))
}

// tick 扫一遍到期任务：先排好下次时刻并落盘，再异步执行（执行慢/挂掉不影响后续调度）。
func (s *Scheduler) tick(now time.Time) {
	s.mu.Lock()
	var fire []string
	for _, t := range s.tasks {
		if !t.Enabled || t.Done || s.running[t.ID] {
			continue
		}
		if t.NextRun.IsZero() || t.NextRun.After(now) {
			continue
		}
		// once 任务 NextRun 不前移（跑完由 finish 置 Done 收口）；
		// interval 从原槽位累加防漂移；daily/weekly 从 now 重算。
		if t.Schedule.Type != ScheduleOnce {
			if next, err := s.nextAfterFire(t, now); err == nil {
				t.NextRun = next
			} else {
				t.LastError = err.Error()
				t.Enabled = false
				continue
			}
		}
		s.running[t.ID] = true
		fire = append(fire, t.ID)
	}
	s.persistLocked()
	s.mu.Unlock()
	for _, id := range fire {
		go s.runOne(id, false)
	}
}

// nextAfterFire 计算本次触发之后的下次时刻（调用方持 mu）。
func (s *Scheduler) nextAfterFire(t *Task, now time.Time) (time.Time, error) {
	if t.Schedule.Type == ScheduleInterval {
		next := t.NextRun
		for !next.After(now) {
			next = next.Add(time.Duration(t.Schedule.EveryMins) * time.Minute)
		}
		return next, nil
	}
	return t.Schedule.Next(now, s.loc)
}

// runOne 执行一个任务实例：panic 恢复 + 超时兜底，结束后 finish 收尾。
// manual=true 为试跑（RunNow）：不影响调度节奏，once 也不置 Done。
func (s *Scheduler) runOne(id string, manual bool) {
	defer func() {
		s.mu.Lock()
		delete(s.running, id)
		s.mu.Unlock()
	}()

	s.mu.Lock()
	tp, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	h, hasHandler := s.handlers[tp.Handler]
	ctx := s.loopCtx
	if ctx == nil {
		ctx = context.Background()
	}
	task := *tp
	s.mu.Unlock()

	var err error
	if !hasHandler {
		err = fmt.Errorf("未注册的处理器 %q", task.Handler)
	} else {
		ctx, cancel := context.WithTimeout(ctx, runTimeout)
		done := make(chan error, 1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[task] %s(%s) 执行 panic: %v\n%s", id, task.Name, r, debug.Stack())
					done <- fmt.Errorf("panic: %v", r)
				}
			}()
			done <- h(ctx, task)
		}()
		select {
		case err = <-done:
		case <-ctx.Done():
			err = fmt.Errorf("执行超时或被取消（上限 %s）", runTimeout)
		}
		cancel()
	}
	s.finish(id, err, manual)
}

// finish 收尾一次执行：更新 LastRun/RunCount/LastError、once 置 Done、落盘。
func (s *Scheduler) finish(id string, err error, manual bool) {
	s.mu.Lock()
	t, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	t.LastRun = time.Now()
	t.RunCount++
	if err != nil {
		t.LastError = err.Error()
		log.Printf("[task] %s(%s) 执行失败: %v", id, t.Name, err)
	} else {
		t.LastError = ""
		if manual {
			log.Printf("[task] %s(%s) 试跑完成", id, t.Name)
		} else {
			log.Printf("[task] %s(%s) 执行完成（第 %d 次）", id, t.Name, t.RunCount)
		}
	}
	if !manual && t.Schedule.Type == ScheduleOnce {
		t.Done = true
	}
	s.persistLocked()
	s.mu.Unlock()
}

// countUserTasksLocked 数用户创建的任务总数（调用方持 mu）。
func (s *Scheduler) countUserTasksLocked() int {
	n := 0
	for _, t := range s.tasks {
		if t.Author != AuthorPlugin {
			n++
		}
	}
	return n
}

// countByTargetLocked 数发往同一目标聊天的用户任务数（上限的「单会话」口径）。
func (s *Scheduler) countByTargetLocked(tg *Target) int {
	key := ""
	if tg != nil {
		key = tg.String()
	}
	n := 0
	for _, t := range s.tasks {
		if t.Author == AuthorPlugin {
			continue
		}
		k := ""
		if t.Target != nil {
			k = t.Target.String()
		}
		if k == key {
			n++
		}
	}
	return n
}

// persistLocked 落盘当前任务表（调用方持 mu；失败仅告警，内存状态为准）。
func (s *Scheduler) persistLocked() {
	f := storeFile{NextID: s.nextID, Tasks: make([]Task, 0, len(s.tasks))}
	for _, t := range s.tasks {
		f.Tasks = append(f.Tasks, *t)
	}
	sort.Slice(f.Tasks, func(i, j int) bool { return f.Tasks[i].ID < f.Tasks[j].ID })
	if err := s.st.save(f); err != nil {
		log.Printf("[task] 落盘失败: %v", err)
	}
}
