package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"qqbot/internal/scheduler"

	"github.com/tidwall/gjson"
)

// TaskCreate 让 AI 在对话中自主创建定时任务：到点后 AI 会在独立会话里执行 prompt
// （可用全部工具，如联网搜索），并把结果主动发送到当前聊天（默认目标经
// scheduler.WithChatInfo 注入，agent 插件每次 Run 前塞进 ctx）。
type TaskCreate struct{ Sched *scheduler.Scheduler }

// Name 返回工具名。
func (t *TaskCreate) Name() string { return "create_task" }

// Description 返回工具描述。
func (t *TaskCreate) Description() string {
	return "创建一个定时任务：到点后 AI 在独立会话中执行 prompt 并把结果自动发到当前聊天。" +
		"用户说「每天几点提醒我/推送/总结」「每周几定时…」「每隔多久…」「某天某点…」这类需求时使用。"
}

// Parameters 返回参数 JSON Schema。
func (t *TaskCreate) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":          map[string]any{"type": "string", "description": "任务名（简短，如「每日科技早报」）"},
			"type":          map[string]any{"type": "string", "enum": []string{"daily", "weekly", "interval", "once"}, "description": "计划类型：每天/每周/固定间隔/单次"},
			"at":            map[string]any{"type": "string", "description": "daily/weekly 的触发时刻 HH:MM（24小时制，如 08:30）"},
			"days":          map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "weekly 的星期几（0=周日,1=周一,…,6=周六；工作日=[1,2,3,4,5]）"},
			"every_minutes": map[string]any{"type": "integer", "description": "interval 的间隔分钟数（>=1）"},
			"run_at":        map[string]any{"type": "string", "description": "once 的绝对时刻，RFC3339 带时区偏移（如 2026-08-15T09:00:00+08:00）"},
			"prompt":        map[string]any{"type": "string", "description": "到点执行的任务指令（交给 AI 独立执行，写清楚要做什么、输出格式与篇幅），结果自动发到本聊天"},
		},
		"required": []string{"type", "prompt"},
	}
}

// Guidelines 贡献使用要点。
func (t *TaskCreate) Guidelines() []string {
	return []string{
		"用户想要定时/周期/到点自动做的事（提醒、日报、定时推送等），用 create_task 建定时任务，到点结果会自动发到当前聊天",
		"创建前先向用户复述确认任务内容与时间（每天几点/每周几/隔多久/哪一刻）；once 的绝对时间先用 current_time 确认当前时间与时区再填 run_at",
	}
}

// Execute 创建任务。
func (t *TaskCreate) Execute(ctx context.Context, arguments string) (string, error) {
	info, ok := scheduler.ChatInfoFrom(ctx)
	if !ok {
		return "", errors.New("当前会话缺少聊天上下文，无法确定任务的发送目标")
	}
	var sch scheduler.Schedule
	sch.Type = gjson.Get(arguments, "type").String()
	sch.At = gjson.Get(arguments, "at").String()
	for _, d := range gjson.Get(arguments, "days").Array() {
		sch.Days = append(sch.Days, int(d.Int()))
	}
	sch.EveryMins = int(gjson.Get(arguments, "every_minutes").Int())
	sch.RunAt = gjson.Get(arguments, "run_at").String()
	prompt := strings.TrimSpace(gjson.Get(arguments, "prompt").String())
	if prompt == "" {
		return "", errors.New("缺少 prompt（到点执行的任务指令）")
	}
	name := strings.TrimSpace(gjson.Get(arguments, "name").String())
	if name == "" {
		name = shortName(prompt)
	}

	target := info.Target
	created, err := t.Sched.Add(scheduler.Task{
		Name:     name,
		Handler:  scheduler.HandlerAgent,
		Schedule: sch,
		Payload:  prompt,
		Target:   &target,
		Author:   info.UserID,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("已创建任务 [%s]「%s」：%s，下次运行 %s（结果将发送到本聊天；用户可用 /task 命令管理）",
		created.ID, created.Name, sch.Humanize(), created.NextRun.In(t.Sched.Loc()).Format("01-02 15:04")), nil
}

// TaskList 让 AI 查看现有定时任务。
type TaskList struct{ Sched *scheduler.Scheduler }

// Name 返回工具名。
func (t *TaskList) Name() string { return "list_tasks" }

// Description 返回工具描述。
func (t *TaskList) Description() string {
	return "列出全部定时任务（ID、名称、计划、启停状态、下次运行时间）。用户想查看/核对已有定时任务时使用。"
}

// Parameters 返回参数 JSON Schema。
func (t *TaskList) Parameters() map[string]any { return nil }

// Execute 列出任务。
func (t *TaskList) Execute(_ context.Context, _ string) (string, error) {
	tasks := t.Sched.List()
	if len(tasks) == 0 {
		return "当前没有定时任务", nil
	}
	lines := make([]string, 0, len(tasks))
	for _, task := range tasks {
		lines = append(lines, DescribeTask(task))
	}
	return fmt.Sprintf("共 %d 个定时任务：\n%s", len(tasks), strings.Join(lines, "\n")), nil
}

// TaskDelete 让 AI 删除定时任务。
type TaskDelete struct{ Sched *scheduler.Scheduler }

// Name 返回工具名。
func (t *TaskDelete) Name() string { return "delete_task" }

// Description 返回工具描述。
func (t *TaskDelete) Description() string {
	return "删除一个定时任务。用户说「取消/删掉某个定时任务」时先用 list_tasks 查 ID 再删除。"
}

// Parameters 返回参数 JSON Schema。
func (t *TaskDelete) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{"type": "string", "description": "任务 ID（如 t3，list_tasks 可查）"},
		},
		"required": []string{"id"},
	}
}

// Execute 删除任务。
func (t *TaskDelete) Execute(_ context.Context, arguments string) (string, error) {
	id := strings.TrimSpace(gjson.Get(arguments, "id").String())
	task, ok := t.Sched.Delete(id)
	if !ok {
		return "", fmt.Errorf("任务 %s 不存在", id)
	}
	return fmt.Sprintf("已删除任务 [%s]「%s」", task.ID, task.Name), nil
}

// TaskToggle 让 AI 启用/停用定时任务。
type TaskToggle struct{ Sched *scheduler.Scheduler }

// Name 返回工具名。
func (t *TaskToggle) Name() string { return "toggle_task" }

// Description 返回工具描述。
func (t *TaskToggle) Description() string {
	return "启用或停用一个定时任务（不删除，可随时再开）。用户说「暂停/恢复某个定时任务」时使用。"
}

// Parameters 返回参数 JSON Schema。
func (t *TaskToggle) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{"type": "string", "description": "任务 ID（如 t3）"},
			"on": map[string]any{"type": "boolean", "description": "true=启用，false=停用"},
		},
		"required": []string{"id", "on"},
	}
}

// Execute 启停任务。
func (t *TaskToggle) Execute(_ context.Context, arguments string) (string, error) {
	id := strings.TrimSpace(gjson.Get(arguments, "id").String())
	on := gjson.Get(arguments, "on").Bool()
	task, ok := t.Sched.Toggle(id, on)
	if !ok {
		return "", fmt.Errorf("任务 %s 不存在", id)
	}
	state := "已启用"
	if !on {
		state = "已停用"
	}
	return fmt.Sprintf("任务 [%s]「%s」%s", task.ID, task.Name, state), nil
}

// DescribeTask 渲染一行任务摘要（工具与 /task 命令共用）。
func DescribeTask(task scheduler.Task) string {
	var sb strings.Builder
	status := "✅"
	if task.Done {
		status = "🏁"
	} else if !task.Enabled {
		status = "⏸"
	}
	fmt.Fprintf(&sb, "%s [%s] %s｜%s", status, task.ID, task.Name, task.Schedule.Humanize())
	if task.Done {
		sb.WriteString("｜已完成")
	} else if task.Enabled {
		fmt.Fprintf(&sb, "｜下次 %s", task.NextRun.Format("01-02 15:04"))
	} else {
		sb.WriteString("｜已停用")
	}
	if task.RunCount > 0 {
		fmt.Fprintf(&sb, "｜已跑%d次", task.RunCount)
	}
	if task.Target != nil {
		switch task.Target.Source {
		case "group":
			sb.WriteString("｜发到群")
		case "c2c":
			sb.WriteString("｜发到私聊")
		case "channel":
			sb.WriteString("｜发到频道")
		}
	}
	if task.LastError != "" {
		fmt.Fprintf(&sb, "｜⚠%s", tailRunes(task.LastError, 40))
	}
	return sb.String()
}

// shortName 从 prompt 截前 12 个 rune 做任务名。
func shortName(prompt string) string {
	r := []rune(prompt)
	if len(r) <= 12 {
		return prompt
	}
	return string(r[:12]) + "…"
}

// tailRunes 取尾部 n 个 rune（错误信息收尾展示）。
func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}
