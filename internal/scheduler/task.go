// Package scheduler 提供结构化定时任务：任务声明、JSON 落盘、到点触发。
//
// 它是纯调度核心（不依赖 bot/agent）：到点后回调注册的处理器，发送消息、跑 AI
// 等副作用由接线层实现（plugins/tasks.go）。计划为结构化四类（每天/每周/固定间隔/
// 单次），agent 把自然语言转成它，插件也可以直接声明。
package scheduler

import (
	"fmt"
	"strings"
	"time"
)

// HandlerAgent 是「到点执行一段 agent prompt 并把结果发回目标聊天」的内置处理器名，
// 由 plugins/tasks.go 注册实现。
const HandlerAgent = "agent.run"

// AuthorPlugin 是插件经 Ensure 声明的任务的 Author（不占任务数量上限）。
const AuthorPlugin = "plugin"

// Target 是任务的「结果发送目标」：agent 任务到点后把执行结果发到这里。
// 对应 bot 的三类消息来源（主动发送：频道无限制；群/私聊需 q.qq.com 主动消息权限）。
type Target struct {
	Source string `json:"source"` // "group" | "c2c" | "channel"
	ID     string `json:"id"`     // 群/用户 openid 或子频道 id
}

// ParseTarget 解析 "group:群openid" / "channel:子频道id" / "c2c:用户openid" 形式的
// 目标描述（env 配置、命令参数用这种紧凑写法）。
func ParseTarget(s string) (Target, error) {
	s = strings.TrimSpace(s)
	i := strings.Index(s, ":")
	if i <= 0 {
		return Target{}, fmt.Errorf("目标需为 group:id / channel:id / c2c:id 格式: %q", s)
	}
	src, id := s[:i], strings.TrimSpace(s[i+1:])
	switch src {
	case "group", "channel", "c2c":
	default:
		return Target{}, fmt.Errorf("未知目标类型 %q（group/channel/c2c）", src)
	}
	if id == "" {
		return Target{}, fmt.Errorf("目标 id 为空: %q", s)
	}
	return Target{Source: src, ID: id}, nil
}

// String 返回 "group:xxx" 形式的紧凑描述。
func (t Target) String() string { return t.Source + ":" + t.ID }

// Task 是一个定时任务。
type Task struct {
	ID        string    `json:"id"`               // "t1","t2"…（自增短 ID，命令里好打）
	Name      string    `json:"name"`             // 人类可读名（列表/确认时展示）
	Handler   string    `json:"handler"`          // 处理器名：HandlerAgent 或插件注册的名
	Schedule  Schedule  `json:"schedule"`         // 触发计划
	Payload   string    `json:"payload"`          // agent 任务=到点执行的 prompt；插件任务=自由参数
	Target    *Target   `json:"target,omitempty"` // 结果发送目标（agent 任务必填；插件自管逻辑可空）
	Enabled   bool      `json:"enabled"`          // 停用后到点不触发
	Done      bool      `json:"done"`             // once 任务跑完置 true（终态，不再触发）
	Author    string    `json:"author"`           // 创建者 openid；AuthorPlugin 表示代码声明
	CreatedAt time.Time `json:"created_at"`
	NextRun   time.Time `json:"next_run"` // 下次运行时刻（落盘，重启续上）
	LastRun   time.Time `json:"last_run"`
	RunCount  int       `json:"run_count"`
	LastError string    `json:"last_error"` // 最近一次执行/发送失败原因（列表可见）
}
