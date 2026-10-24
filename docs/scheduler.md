# 定时任务（qqbot/scheduler）

结构化定时任务，两个入口：

- **agent 自主创建**（配了 AI_API_KEY 即可用）：用户说「每天早上8点给我发一条科技新闻摘要」，AI 调 `create_task` 建任务，到点自动执行并把结果发回创建任务的聊天；
- **插件声明**：`sched.Handle` 注册处理器 + `sched.Ensure` 幂等声明默认任务（见 `plugins/happy.go` 的每日笑话示范）。

```text
用户：「每天8点给我发一条科技早报」
  └→ agent 调 create_task{name:"科技早报", type:"daily", at:"08:00",
                          prompt:"搜索并总结今日科技新闻，3条以内"}
       └→ 到点：scheduler 触发 → agent 以任务专属会话(t:t3)执行 prompt（可用全部工具）
            └→ 结果主动发送到创建任务的聊天
```

- 计划为结构化四类：`daily`（at "HH:MM"）/ `weekly`（days 0=周日…6=周六，工作日=[1,2,3,4,5]）/ `interval`（every_minutes）/ `once`（run_at，RFC3339）。时区 `TASK_TZ`（默认 Asia/Shanghai）。比让 LLM 写 cron 可靠，展示也直接转人话（「工作日 09:30」「每30分钟」）。
- 每个 agent 任务用独立会话 `t:任务id`：跨次执行有记忆（记得之前发过什么、用户偏好），又不污染聊天会话；删除任务时一并清掉。
- 重启恢复：错过 ≤5 分钟的照常触发；更早的**不补跑**——周期任务滚到下个计划点、单次任务标记过期（air 热重载/重启不刷屏）。
- 执行保护：每任务同时最多一个实例（重叠跳过）、panic 恢复、15 分钟超时；失败记入 `LastError`（`/task` 列表可见，下个周期自然再试）。
- 数量上限：单聊天 `TASK_MAX_PER_CHAT`（默认10）/ 全局 `TASK_MAX_TOTAL`（默认100），插件声明的任务不计。

管理命令：`/task`（列表，每个任务行尾带「试跑/停用启用/删除」内嵌指令标签，点击填入对应命令不用手打 ID）｜`/task del <id>`｜`/task on|off <id>`｜`/task run <id>`（立即试跑，不影响原计划）。创建不需要命令，直接跟 bot 说即可；agent 侧也可 `list_tasks` / `delete_task` / `toggle_task`。

**平台限制**：定时发送只能走主动消息。频道随时可发；群/私聊主动消息需在 q.qq.com 开通（**本机器人已开通**）且受官方额度频控，失败会记入 LastError。

## 插件侧使用（Happy 每日笑话示范，`HAPPY_DAILY_TARGET` 指定目标后启用）

```go
func NewHappy(sched *scheduler.Scheduler, dailyTarget string) *Happy
// Install 内：
p.sched.Handle("happy.daily", p.daily)          // 注册处理器（代码侧）
p.sched.Ensure(scheduler.Task{                  // 声明默认任务（幂等，不覆盖用户的开关/时间修改）
    Name:     "每日笑话",
    Handler:  "happy.daily",
    Schedule: scheduler.Schedule{Type: scheduler.ScheduleDaily, At: "08:30"},
    Target:   &tg, // scheduler.ParseTarget("group:群openid") 解析而来
})
```

## 相关 env

```env
TASK_FILE=./data/tasks.json    # 任务落盘（重启恢复）
TASK_TZ=Asia/Shanghai          # 计划时区
TASK_MAX_PER_CHAT=10           # 单聊天任务上限（对话里创建的）
TASK_MAX_TOTAL=100             # 全局任务上限（对话里创建的）
HAPPY_DAILY_TARGET=            # 每日笑话发送目标（group:xxx / channel:xxx / c2c:xxx；空=不启用）
```
