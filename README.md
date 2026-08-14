# qqbot

基于腾讯官方 [botgo](https://github.com/tencent-connect/botgo) SDK 的 QQ 机器人，自带一个轻量插件框架（命令 / 中间件 / 监听器 / 插件 / 会话状态），外加一个**轻量 agent harness**（工具循环 / 多轮记忆 / 上下文压缩 / 技能 / MCP）。

- **官方机器人**：走 `q.qq.com` 注册的机器人（AppID + AppSecret），合规、稳定。
- **WSS 出站**：机器人主动连接腾讯（内网/NAT 友好，无需公网 HTTPS）。
- **插件框架** `internal/bot`：写功能只关心业务，handler 收一个 `*bot.Ctx`。
- **agent harness** `internal/agent`：未命中命令的消息交给一个可调工具、有记忆、能压缩上下文的 agent（设计参考 [pi](https://github.com/earendil-works/pi) 的 coding-agent）。
- 示例插件：`ping` / `echo` / `counter`（会话状态）/ `happy`（HTTP+gjson）/ `agent`（未命中命令时 agent 兜底）。

> ⚠️ 官方机器人约束：**只能被动回复**（收到消息后 5 分钟内回复）、**用户/群 ID 是 openid（不是 QQ 号）**、**没有群管接口**（踢人/禁言等）。上线通知、入群欢迎、主动推送等无法实现。
>
> 群消息：默认只收 @ 消息（`GROUP_AT_MESSAGE_CREATE`）；在 q.qq.com 开通**全量群消息**权限后可收 `GROUP_MESSAGE_CREATE`（无需 @，本框架已支持两者）。

## 目录结构

```
qqbot/
├── main.go                  # 装配：读 .env → bot.New → 注册插件 → 装配 agent → Run
├── cmd/imghost/             # 图床服务（独立二进制，部署到公网服务器；给 QQ URL 模式发图用）
├── internal/
│   ├── bot/                 # 插件框架（基于 botgo）
│   │   ├── bot.go           # Bot / Config / New / Run（WSS 装配 + 事件分发）+ 收图（attachments）
│   │   ├── ctx.go           # Ctx（三源：群/频道/私聊）+ ReplyText/Image + Images()
│   │   ├── command.go       # Command / Args / 命令匹配 / 作用域 / Help
│   │   ├── handler.go       # 处理器/中间件类型、监听器、Plugin、Use/UsePlugin
│   │   ├── media.go         # 发图：下载→上传富媒体→MsgType=7（支持 genimg: 图床引用）
│   │   ├── state.go         # 会话状态（按 openid 的 KV）
│   │   └── doc.go           # 包文档
│   ├── agent/               # agent harness（工具循环 / 记忆 / 压缩 / 技能 / MCP）
│   │   ├── agent.go         # Agent + Run（工具循环：调 LLM → 执行工具 → 再调）
│   │   ├── message.go       # 内部消息模型 + ToOpenAIMessages（边界转换）
│   │   ├── tool.go          # Tool 接口 + Registry + 动态工具区（ToolGuideline）
│   │   ├── llm.go           # OpenAI 兼容 Chat Completions 客户端
│   │   ├── session.go       # 每用户多轮历史（JSONL 落盘 + 每用户锁）
│   │   ├── compaction.go    # 上下文压缩（阈值/增量摘要 + 溢出自救重试）
│   │   ├── skill.go         # SKILL.md 加载 + 懒加载索引（配合 read_skill）
│   │   ├── mcp.go           # MCP 客户端（stdio/http/sse，工具并入 Registry）
│   │   └── tools/           # 内置工具：http_get / current_time / web_search / web_extract / generate_image / send_image / search_history / create_task 等定时任务工具 / bash / file_*
│   ├── imgstore/            # 进程内图床（genimg: 引用 + TTL/容量淘汰 + 落盘归档）+ 远端图床客户端 Host
│   ├── memory/              # 长期记忆（SQLite/GORM）：全量落库(异步+去重) / 检索 / 近期动态（群隔离）
│   ├── scheduler/           # 定时任务（结构化计划 daily/weekly/interval/once + 落盘重启恢复 + 到点回调）
│   └── utils/               # HTTP 工具（共享 resty 客户端 + gjson）
├── plugins/                 # 插件（ping/echo/counter/happy/system/zhipu/tasks/online/agent/recorder）
├── skills/                  # 放 SKILL.md（技能，懒加载注入 agent system prompt 索引）
├── mcp.json.example         # MCP server 配置示例（复制为 mcp.json）
├── menu.json.example        # 自定义菜单/指令面板配置示例（复制为 menu.json；不复制用内置默认）
└── scripts/                 # dev.sh（air 热重载）+ deploy/（systemd）
```

> 三层：`internal/bot`（插件框架，基于 botgo）→ `internal/agent`（harness）→ `plugins` + `main`（你的功能）。

## 安装与运行

依赖（`go.mod` 已含）：`github.com/tencent-connect/botgo`、`github.com/joho/godotenv`、`resty.dev/v3`、`github.com/tidwall/gjson`、`github.com/mark3labs/mcp-go`（MCP）、`gopkg.in/yaml.v3`（技能）。

```bash
cp .env.example .env          # 填入 QQBOT_APP_ID / QQBOT_APP_SECRET
go run .
```

`.env` 关键字段（去 `q.qq.com` 后台拿）：
```env
QQBOT_APP_ID=你的 AppID
QQBOT_APP_SECRET=你的 AppSecret
BOT_PREFIXES=/                # 命令前缀（可选，多个用 | 分隔）
AI_API_KEY=                   # 配置则启用 agent 兜底（工具循环/多轮记忆/压缩/技能/MCP）
```

## 快速开始

```go
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"qqbot/internal/bot"
	"qqbot/plugins"
)

func main() {
	_ = godotenv.Load()
	b := bot.New(bot.Config{
		AppID:     os.Getenv("QQBOT_APP_ID"),
		AppSecret: os.Getenv("QQBOT_APP_SECRET"),
	}, bot.WithPrefixes("/"))

	// 每条消息打印一行
	b.Use(func(next bot.MessageHandler) bot.MessageHandler {
		return func(c *bot.Ctx) {
			log.Printf("[%s] %s: %s", c.Source, c.UserID(), c.Text())
			next(c)
		}
	})

	b.UsePlugin(plugins.NewPing(), plugins.NewEcho(), plugins.NewHappy())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := b.Run(ctx); err != nil && err != context.Canceled {
		log.Fatal(err)
	}
}
```

## 插件框架（qqbot/bot）

handler 都收一个 `*bot.Ctx`：绑好了 Bot / context / 消息内容 / 来源 / 命令参数，并带 `c.ReplyText` 等便捷方法。

```go
b := bot.New(bot.Config{AppID: appID, AppSecret: secret}, bot.WithPrefixes("/"))

// ① 函数式：一行注册命令
b.Command("ping", func(c *bot.Ctx) { c.ReplyText("pong") }).Desc("测试在线").Alias("p")
b.OnKeyword("早安", func(c *bot.Ctx) { c.ReplyText("早安！") })

// ② 插件式
b.UsePlugin(plugins.NewEcho(), plugins.NewCounter())
b.Run(ctx)
```

### 命令
```go
b.Command("echo", echo).
	Desc("复读一句话").   // 进 /help
	Usage("/echo <文本>").
	Alias("say").        // 别名
	Hidden().            // 不出现在 /help
	GroupOnly()          // 作用域：GroupOnly/PrivateOnly/ChannelOnly
```
- 命令触发：群消息需 @（官方强制），命中后**可选**前缀去掉再匹配；私聊直接匹配。命中命令后不再触发关键词/监听器。
- `Args`：`c.Args.Join()` 取整段、`c.Args.Get(i)` 取第 i 个参数。

### `*bot.Ctx` 便捷方法
| 方法 | 作用 |
|---|---|
| `c.ReplyText(s)` | 纯文本回复（被动，锚定到当前消息；群/频道/私聊自动分支） |
| `c.ReplyMarkdown(md)` | 原生 markdown 回复（被动；群/私聊公域会被校验语料/异步审核） |
| `c.Text()` | 消息纯文本（已去 @ 前缀） |
| `c.IsGroup()` / `c.IsChannel()` / `c.IsPrivate()` | 消息来源 |
| `c.UserID()` / `c.GroupID()` / `c.ChannelID()` | openid（字符串） |
| `c.Args` | 命令参数 |
| `c.Bot` / `c.Context` | 框架实例 / context（传给 HTTP 调用） |

> 没有 `c.AtBot()`（群里收到的都是 @）。群/私聊官方只能被动回复（需消息 id 锚定、5 分钟内）。

### Bot 级发送（`internal/bot`）
文本：
| 方法 | 语义 |
|---|---|
| `b.SendChannelText(ctx, channelID, text)` | 频道**主动**发（无需锚点） |
| `b.SendGroupText(ctx, groupID, msgAnchor, text)` | 群：msgAnchor 空=**主动**（需开通权限）；非空=被动回复 |
| `b.SendC2CText(ctx, userID, msgAnchor, text)` | 私聊：msgAnchor 空=**主动**（需开通权限）；非空=被动回复 |

原生 markdown（`MsgType=2`）：
| 方法 | 语义 |
|---|---|
| `b.SendChannelMarkdown(ctx, channelID, md)` | 频道主动发原生 md（频道相对宽松） |
| `b.SendGroupMarkdown(ctx, groupID, msgAnchor, md)` | 群：msgAnchor 空=主动；非空=被动。**公域会被校验语料/异步审核** |
| `b.SendC2CMarkdown(ctx, userID, msgAnchor, md)` | 私聊：同上，公域受限 |

> ⚠️ 群/私聊的原生 markdown 公域机器人不是想发就发：主动消息异步审核、被动消息校验语料，可能发不出或延迟。频道宽松。若群/私聊要稳定发 md，需在 q.qq.com 后台建 markdown 模板走 template（当前未实现，需要再加）。

> 频道一直可主动发。群/私聊的主动发送需在 q.qq.com 开通「主动消息」权限（受官方频控/额度限制）——开通后 `msgAnchor` 传空即为主动消息。处理消息时用 `c.ReplyText` 即可（自动按来源分支 + 取锚点）。

### 监听器 / 中间件
```go
b.OnMessage(h)            // 所有消息
b.OnGroupMessage(h)       // 仅群
b.OnPrivateMessage(h)     // 仅私聊
b.OnChannelMessage(h)     // 仅频道
b.OnKeyword("关键词", h)   // 文本包含即触发
b.Use(mw1, mw2)           // 全局中间件（包裹命令+监听器，不调 next 即拦截）
```

### 写一个插件
```go
type Echo struct{}
func (p *Echo) Install(b *bot.Bot) {
	b.Command("echo", p.run).Desc("复读").Usage("/echo <文本>")
}
func (p *Echo) run(c *bot.Ctx) {
	c.ReplyText(c.Args.Join())
}
// 注册：b.UsePlugin(&Echo{})
```

### 会话状态（按 openid）
```go
b.UserState(c.UserID()).Set("count", 1)
b.UserState(c.UserID()).GetInt("count")   // 还提供 GetString/GetBool/Delete/Clear
b.GroupState(c.GroupID()).GetString("mode")
```

### 自动 /help
默认开启，从非隐藏命令生成帮助列表（`b.Help()` 取文本）。

## Agent harness（qqbot/agent）

配置了 `AI_API_KEY` 后，未命中命令的消息会交给一个 **agent**（`plugins/agent.go`）：它不是「问一次答一次」的 AI，而是能**调工具、记多轮、压缩上下文**的循环。

### 它做了什么
- **工具循环**（参考 pi `agent-loop.ts`）：调 LLM → 若模型要调工具就执行、把结果喂回 → 再调 LLM，直到模型给出纯文本回复。`MaxIterations` / `LoopTimeout` 双重上限，守住官方 5 分钟被动回复窗口。
- **多轮记忆（短期）**：对话历史落盘；**群聊共享**（key=`g:群id`，全群一份历史，消息带 `[昵称]` 说话人标注），私聊按用户（`data/sessions/`）。air 热重载也不丢；同一会话并发消息用互斥锁串行。
- **长期记忆（SQLite）**：群+私聊消息**全量落库**（含未 @bot 的群聊闲聊，需全量消息权限；GORM + 纯 Go sqlite，异步写入+MsgID 去重）。两层消费：① `search_history` 工具按需深查（关键词/说话人/天数，**群隔离**——A 群查不到 B 群）；② 每次 @bot 把**上次 @bot 到这次之间**的新群消息**增量并入 session**（持久消息、下轮自动加载，期间没人说话就不注入）。详见下文「聊天记忆」。
- **上下文压缩**：token 估算超阈值时，把旧消息让 LLM 摘要、保留近期窗口（turn 对齐，不切断工具调用配对）；**结构化摘要**（目标/约束/进展/决定/下一步/关键上下文，强制保留文件路径/函数名/错误原文）；**增量更新**（基于上一轮摘要并入新进展，而非从头生成）；**溢出自救**（LLM 报上下文超长时，自动压缩后重试一次）。
- **System prompt 动态拼装**（参考 pi `buildSystemPrompt`）：人设（`AI_SYSTEM_PROMPT`，留空用内置默认）+ **Available tools**（每个工具一行）+ **Guidelines**（工具自报的使用要点，如「想要图片→先 web_search 找直链→再 send_image」+ 恒定的简洁/诚实提示）+ 技能索引。每轮按当前启用的工具重建，运行时新加的工具/技能下轮就生效。
- **技能 Skills**（懒加载，参考 pi / Agent Skills 标准）：`skills/*.md`（YAML frontmatter + 正文）自动加载，system prompt 里只贴 **`<available_skills>` 索引**（name + description），模型判断任务匹配时用 `read_skill` 工具按 name 现读正文——技能多/正文长也不每轮摊进 context。
- **MCP**：`mcp.json`（Claude Desktop 格式）里的 MCP server 自动连接（stdio/http/sse），工具并入 agent。env 值支持 `${VAR}` 引用 `.env` 注入的变量（如 `"Z_AI_API_KEY": "${AI_API_KEY}"`），密钥不用抄进 mcp.json；仅展开 `${NAME}` 形式，字面 `$` 不受影响。
- **收图**：用户在群/私聊发图片，bot 从消息附件（`attachments`）取出图片 URL 注入 agent 输入（`[用户发送了N张图片，URL：...]`），AI 据此拿到 URL（可用于图生图等）。⚠️ 两点诚实说明：① QQ 图片 URL（`multimedia.nt.qq.com.cn`）带 `rkey` 时效，外部服务能否直接抓取待真机验证，必要时需经 bot 下载中转/上图床；② AI 目前只拿到 URL、不具备「看」图能力（未接 vision 模型）。纯图片消息（无文本）也会进入 agent（不会被空文本过滤掉）。
- **生图 + 图床**（`generate_image`）：`IMAGE_API_KEY` 配置后，AI 可文生图（`/images/generations`）/ 图生图（`/images/edits`，输入图可是 URL 或 `genimg:` 引用）。生图 API 常只回 base64（几十万字节，不能进 LLM 上下文），解码后**上传图床拿公网 URL**（本地也归档一份），AI 再调 `send_image` 发出。链路：`generate_image(prompt)` → 公网 URL → `send_image(url)` → QQ。详见下文「图床」一节。
- **补充与打断**（参考 pi 的 steering，详见下文）：agent 处理是**异步**的（botgo 事件串行分发，同步跑会堵死后续消息）；处理中再 @bot 发消息＝**补充**（不打断，turn 边界并入当前处理）；`.stop` 停止当前处理；`.reset` 清空会话短期记忆；`.status` 看状态。
- **Token 用量统计**：每次 LLM 调用（含工具循环内的多次调用与压缩摘要）解析响应 `usage` 并计时，日志逐调用打一行（`[agent] tokens: 模型 输入1234 输出567 缓存命中1000(81%) 12.3s 46tok/s`），同时落记忆库；`.usage` 命令查**今日/累计**：调用次数、输入/输出/总 token、缓存命中率（网关不报则不显示，不瞎编）、平均输出速率。

### 图床（QQ 发图必需）

QQ 群/私聊的富媒体上传（`/v2/groups/{id}/files`）**简单 multipart 直传已废**——QQ 报 `850026 富媒体文件下载失败`，官方只支持「URL 模式」（给一个 url，QQ 自己下载）。所以需要一个**公网可达**的图床提供图片 URL。本项目自带一个极简图床服务 `cmd/imghost`：

```
go build -o imghost ./cmd/imghost
./imghost   # 部署到有公网 IP 的服务器
```

配置（env）：`IMGHOST_LISTEN`（监听，默认 `:8088`）、`IMGHOST_BASE_URL`（**公网**基础 URL，如 `https://img.example.com`，构造返回的图片 URL）、`IMGHOST_TOKEN`（上传鉴权，仅 bot 上传用）、`IMGHOST_DIR`（存储目录）。`POST /upload`（Bearer 鉴权）→ 返回 `{"url":...}`；`GET /<id>.<ext>`（公开，QQ 下载）。防路径穿越、限单文件 20MB。

> ⚠️ 部署：图床的 `GET` 地址 QQ 必须能从公网访问到；建议套 nginx/caddy 走 **HTTPS**（QQ 下载图片更稳，部分情况 HTTP 会被拒）。

bot 侧配置（`.env`）：`IMGHOST_BASE_URL`（图床公网地址，与服务的 `IMGHOST_BASE_URL` 一致）、`IMGHOST_TOKEN`（同上）。两者填了，`generate_image` 就会把生成的图上传图床、返回公网 URL；不填则只在本地（QQ 发不出）。生成的图同时按 `IMG_SAVE_DIR`（默认 `./data/images`）本地归档一份。

### 聊天记忆（长期，SQLite）

短期记忆（session，压缩式）只覆盖 bot 参与的对话；长期记忆把**群+私聊的消息全量落库**（含未 @bot 的群聊闲聊——需要 q.qq.com 开通全量消息权限，你的群开过就有；没开的群退化为只记 @bot 的）。GORM + 纯 Go sqlite（`glebarez/sqlite`，无 cgo），异步批量写入（带缓冲队列，洪峰不挡主流程），`MsgID` 唯一约束去重（QQ 重连重投自动忽略）。

两层消费：

1. **`search_history` 工具（按需深查）**：AI 自己决定何时查——「上次讨论的 XX 是什么结论」「小李说过什么」。参数：关键词 / 说话人 / 天数（默认 7 上限 90）/ 条数（默认 20 上限 50）。**群隔离是硬约束**（查询范围经 ctx 注入，A 群查不到 B 群，私聊只能查本人——不依赖模型自觉）。
2. **群聊记录增量并入 session**：每次 @bot，自动把**上一次 @bot 到这一次之间**的群聊新消息（排除 bot 自己的回复）作为「[群聊记录]」user 消息插在本次问题前面——**随 session 落盘，下轮自动随历史加载，不重复注入**。中间没人说话就不注入。相当于 bot 是「真的在群里看着」：看过的消息留在记忆里，追问不必查库。要点：
   - **放在请求尾部而非 system prompt**——system+历史的前缀单调增长，LLM 上下文缓存可整段命中（放 system 里每条消息都变，缓存全灭，实测命中率被腰斩到 ~50%）；
   - **增量覆盖点（marker）落盘**（`AGENT_GAP_MARKERS`，默认 `data/gap_markers.json`）：重启后接着增量，不回退全量、不重复注入；`.reset` 清 session 时 marker 一并清，下次重新铺底；
   - session 因此长得更快，由上下文压缩兜底（超阈值时旧闲聊被摘要收敛）；
   - 新群首次接触全量窗口铺底（`AGENT_RECENT_WINDOW` 分钟 / `AGENT_RECENT_MAX` 条），私聊不注入（私聊每条消息本就在 session 里）；
   - 用量统计（`.usage`）里的缓存命中率可以直接验证效果。

群聊的 session 也是共享的（key=`g:群id`）：A @bot 画猫、B 接着说「换黑的」，B 在同一份历史里接得上。消息正文带 `[昵称]` 前缀（说话人标注）。bot 的回复也会落进长期记忆（说话人=机器人）。

> ⚠️ 隐私须知：群记忆共享意味着任何人在群里对 bot 说的话都进入该群共享上下文与落库记录（含未 @bot 的闲聊）。适合自用/朋友圈子；库文件 `data/memory.db` 勿外传。查询用 LIKE 子串匹配（中文短消息够用），百万级数据再考虑 FTS/向量检索。

### 补充与打断（steering / interrupt）

agent 的处理是**异步**的：botgo 的 WSS 事件是单 goroutine 串行分发的，若同步跑 agent，一次几十秒的 Run 会堵住分发循环，后续消息（包括 `.stop`）根本到不了 handler。异步化后，同会话的新消息由 session 锁自然排队。

**补充（steering，参考 pi）**：agent 正在处理时，再 @bot（或带前缀）发一条消息——不是排队等上一轮结束，而是**注入当前处理**：

```
你：@bot 画只猫
bot：（处理中，生成约 60s）
你：@bot 加个红色蝴蝶结     ← 补充：回一句轻确认，turn 边界并入
bot：[图：戴红蝴蝶结的猫]    ← 最终回复已包含补充
```

- 注入发生在 **turn 边际**（一批工具跑完、下次调 LLM 前），不打断在飞的工具（如 120s 的生图）。
- 补充作为普通 user 消息进历史（带 `[昵称]` 前缀，群里分得清谁在补充），AI 刚答完恰好又来了补充也会再答一轮。
- 可靠性：入队确认与 Run 退出判断在同一把锁下互斥——**只要 bot 回了确认，消息绝不丢失**（要么并入当前处理，要么作为新一轮对话）。
- 补充会重置迭代计数（用户主动补充不该吃掉工具预算）；总时长仍受 `AGENT_LOOP_TIMEOUT` 硬保护。确认回复可用 `AGENT_STEER_ACK=0` 关闭。

**命令**（走命令通道，不经 agent，秒回）：

| 命令 | 作用 |
|---|---|
| `.stop` | 停止当前会话正在进行的处理（无任务时提示） |
| `.reset` | 清空当前会话的短期记忆（session；长期记忆不受影响） |
| `.status` | 查看当前会话状态（空闲/处理中+时长、待并入补充条数、模型） |
| `.usage` | 查看 token 用量统计（今日/累计：调用次数、输入/输出/总量、缓存命中率、平均输出速率；数据在记忆库，需 `AGENT_MEMORY` 开启） |
| `.menu` | 管理自定义菜单与指令面板（apply 发布 / clear 清除 / 无参查看） |

被 `.stop` 打断的 Run 保留部分历史（问到哪、工具做到哪），下轮对话模型接得上；已开始执行的工具调用以占位结果收尾，不留孤儿（OpenAI 协议要求每个 tool_call 必须有 result）。

### 自定义菜单与指令面板（官方 2026 新增接口）

官方新增了 [自定义菜单与指令面板](https://bot.q.qq.com/wiki/develop/api-v2/server-inter/menu-panel/)（botgo v0.2.1 未覆盖，本项目自行 HTTP 调用，`internal/bot/menu.go`）：

- **自定义菜单**（`PUT /v2/menu`）：C2C 单聊窗口底部的按钮栏，全局生效。按钮类型 `send_message`（点击把文本**填入输入框**，用户再按发送）/ `link` / `menu`（折叠）。
- **指令面板**（`POST /v2/panels`）：按场景（c2c/group/channel/dm）展示的指令/链接面板，可全局或指定用户/群；群聊场景的入口。

默认**启动自动发布**（`AGENT_MENU=1`，`OnStart` 钩子里执行）：C2C 菜单 = 帮助/状态/用量 + 会话折叠（停止/清空）；群面板 = help/status/usage 指令。自定义：复制 `menu.json.example` 为 `menu.json`（`AGENT_MENU_CONFIG` 可改路径）。面板是追加式的，重新发布时按 `remark: qqbot-auto` 标记删除旧面板再重建（手动在官方后台建的面板不动）。

管理命令：`.menu`（查看当前菜单）、`.menu apply`（重新发布）、`.menu clear`（清除）。注意内容有敏感词审核（HTTP 40030020）。默认按钮填入的是**中文别名**（帮助/状态/用量/停止/清空对话）：命令都注册了中文 Alias，私聊免前缀直接命中；群里普通消息不匹配命令，但**整条恰好等于命令名/别名时放行**（否则指令面板在群里点了没反应）——只有整词触发，正常聊天带这些词不会误触。

### 内置工具
| 工具 | 说明 | 默认 |
|---|---|---|
| `http_get` | HTTP GET，**带 SSRF 守卫**（拒内网/回环，可用 `AGENT_HTTP_ALLOW` 放行） | 开 |
| `current_time` | 当前时间 | 开 |
| `web_search` / `web_extract` | anysearch 联网搜索 / 抓网页正文（`ANYSEARCH_API_KEY` 可选） | 开 |
| `generate_image` | AI 生图（OpenAI 协议，文生图/图生图；`IMAGE_API_KEY` 配置） | 开（配 key） |
| `send_image` | 向当前会话发图片（AI 给 URL 或 `genimg:` 引用，自主下载+上传腾讯+发富媒体） | 开 |
| `search_history` | 查本会话长期聊天记录（关键词/说话人/天数；群隔离） | 开（配记忆库） |
| `read_skill` | 按 name 加载某技能的详细正文（配合懒加载技能索引） | 开 |
| `skill_add` / `mcp_add` | 运行时加载技能 / 连接 MCP server | 开 |
| `bash` | 执行 shell（⚠️ RCE 风险） | **关**，需 `AGENT_ENABLE_BASH=1` + 白名单 |
| `file_read` / `file_write` | 读写文件（限定根目录内） | **关**，需 `AGENT_ENABLE_FS=1` |

> ⚠️ **安全**：QQ bot 公开可达（谁都能私聊/群里 @），与 pi 本地 CLI 不同。`bash`/文件工具默认关闭——开启等于把服务器暴露给所有能发消息的人。仅在自用 bot 或充分白名单下开启。

### 写一个自定义工具
实现 `agent.Tool` 接口，注册进 registry 即可（`main.go` 已有例子）：
```go
type MyTool struct{}
func (t *MyTool) Name() string             { return "my_tool" }
func (t *MyTool) Description() string      { return "什么时候用、做什么" }
func (t *MyTool) Parameters() map[string]any {
    return map[string]any{"type":"object","properties":map[string]any{"x":map[string]any{"type":"string"}}}
}
func (t *MyTool) Execute(ctx context.Context, args string) (string, error) {
    // args 是模型给的原始 JSON 字符串，用 gjson 取字段
    return "结果", nil
}
// 注册：reg.Register(&MyTool{})
```
工具**出错也应返回人类可读文本**（由 agent 包成 tool result 喂回模型），不要中断循环。

### 运行时动态加 MCP / Skill（发 URL / 贴配置即可）
不用重启，在对话里就能给 agent 加能力，两种入口：
- **命令（确定性）**：`/mcp` 后跟 http/sse URL，或直接粘贴 MCP 配置 JSON（stdio/http/sse 均可）；`/skill <SKILL.md URL>` 加载技能。
- **AI 自主（自动）**：agent 内置 `skill_add` / `mcp_add` 工具——你发个 URL 或贴段 MCP 配置（如 Claude Desktop 的 `mcpServers` JSON），AI 识别后自己调工具接上，下一条消息就能用新工具/技能。

MCP 配置 JSON 支持三种形态：`{"mcpServers":{"名字":{...}}}` 整段、带 `"name"` 的单个 server、无名单 server（名字从 `command`/`args` 推导）；env 值支持 `${VAR}` 引用 `.env` 变量（密钥不落明文）。**运行时添加的会持久化到 `data/mcp_runtime.json`（`AGENT_RUNTIME_MCP` 可改），重启自动重连**；与 mcp.json 同名的以 mcp.json 为准，重复添加同名 server 会被拒。

```go
// 代码层等价方法（ag *agent.Agent）：
ag.AddMCPFromURL(ctx, "http", url)       // 返回接入的工具数
ag.AddMCPFromConfig(ctx, agent.MCPServerConfig{Type: "stdio", Command: "npx", Args: [...]})  // stdio/http/sse 通吃
ag.AddSkillFromURL(ctx, url)              // 返回 Skill
ag.AddSkill(Skill{...})                   // 直接加一个
```
> ⚠️ 这等于让对话方给 bot 注入工具/prompt；**stdio 类型更是在宿主机执行任意命令**（bash 工具同样默认可开，风险一致）。**自用 OK；公开部署务必加 openid 白名单中间件**（当前未内置鉴权，按你「自用」需求做成默认放开）。

### 浏览器（agent-browser MCP）

[agent-browser](https://github.com/vercel-labs/agent-browser)（Vercel，Rust CLI）经 MCP 接入后，AI 获得**真实浏览器**能力：open/read（网页转 markdown）/snapshot（无障碍树+元素引用）/click/fill/select/eval/screenshot/pdf/多标签页——能对付 JS 渲染、需要点击交互的页面，与 anysearch 的 `web_extract`（纯抓取）互补。

前置（一次性，本机已完成）：
```bash
npx -y agent-browser@0.34.0 install   # 下载 Chrome for Testing 到 ~/.agent-browser
```
`mcp.json` 里的配置（版本钉死，daemon 空闲 1 小时自动退出省内存，下次调用自动拉起）：
```json
"agent-browser": {
  "type": "stdio", "command": "npx", "args": ["-y", "agent-browser@0.34.0", "mcp"],
  "env": { "AGENT_BROWSER_IDLE_TIMEOUT_MS": "3600000" }
}
```
截图工具返回本地保存路径，`send_image` 现在同时接受 URL 与本地路径（自动上传图床转公网 URL 再发 QQ）——「截个 xx 网站的图发群里」整链可用。

### 关键 env
```env
AI_API_KEY=                   # agent 总开关（留空则不启用）
AI_BASE_URL=https://api.deepseek.com/v1
AI_MODEL=deepseek-chat
AGENT_HISTORY_DIR=./data/sessions      # 短期记忆目录（群共享 g_群id.jsonl / 用户 openid.jsonl）
AGENT_COMPACTION_THRESHOLD=6000        # 压缩 token 阈值（0=关）
AGENT_SKILLS_DIR=./skills              # 技能目录
AGENT_MCP_CONFIG=./mcp.json            # MCP 配置
AGENT_RUNTIME_MCP=./data/mcp_runtime.json  # 对话里动态添加的 MCP 持久化（重启重连）
AGENT_MAX_ITERATIONS=8                 # 工具循环上限
AGENT_MEMORY=1                         # 长期记忆开关（SQLite 全量落库）
AGENT_MEMORY_DB=./data/memory.db       # 记忆库文件
AGENT_RECENT_WINDOW=60                 # 群聊记录并入：新群首次接触的全量铺底窗口（分钟）
AGENT_RECENT_MAX=30                    # 注入条数上限（增量与全量共用）
AGENT_GAP_MARKERS=./data/gap_markers.json  # 增量覆盖点落盘（重启无缝续上，.reset 时清除）
BOT_PID_FILE=./data/bot.pid                # 单实例守护（第二个实例启动时拒绝；两个实例=消息被处理两次）
AGENT_STEER_ACK=1                      # 补充入队成功后回一句轻确认（0=关）
AGENT_MENU=1                           # 启动自动发布自定义菜单+指令面板（0=关）
AGENT_MENU_CONFIG=./menu.json          # 自定义菜单配置（不存在的话用内置默认）
```

## 定时任务（qqbot/scheduler）

结构化定时任务，两个入口：

- **agent 自主创建**（配了 AI_API_KEY 即可用）：用户说「每天早上8点给我发一条科技新闻摘要」，AI 调 `create_task` 建任务，到点自动执行并把结果发回创建任务的聊天；
- **插件声明**：`sched.Handle` 注册处理器 + `sched.Ensure` 幂等声明默认任务（见 `plugins/happy.go` 的每日笑话示范）。

```text
用户：「每天8点给我发一条科技新闻摘要」
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

管理命令：`/task`（列表）｜`/task del <id>`｜`/task on|off <id>`｜`/task run <id>`（立即试跑，不影响原计划）。创建不需要命令，直接跟 bot 说即可；agent 侧也可 `list_tasks` / `delete_task` / `toggle_task`。

**平台限制**：定时发送只能走主动消息。频道随时可发；**群/私聊主动消息需在 q.qq.com 开通且受官方额度频控**，未开通时发送会失败并记入 LastError。

插件侧使用（Happy 每日笑话示范，`HAPPY_DAILY_TARGET` 指定目标后启用）：

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

相关 env：

```env
TASK_FILE=./data/tasks.json    # 任务落盘（重启恢复）
TASK_TZ=Asia/Shanghai          # 计划时区
TASK_MAX_PER_CHAT=10           # 单聊天任务上限（对话里创建的）
TASK_MAX_TOTAL=100             # 全局任务上限（对话里创建的）
HAPPY_DAILY_TARGET=            # 每日笑话发送目标（group:xxx / channel:xxx / c2c:xxx；空=不启用）
```

## 验证

```bash
go build ./...
go vet ./...
go test ./...        # 命令匹配 / 作用域 / 状态 / help 纯逻辑测试
gofmt -l .
```

## 后台运行（systemd）

开发期用 `air` 热重载（`./scripts/dev.sh start`）；**长期后台运行**用 systemd：

```bash
go build -o qqbot .
sudo cp scripts/deploy/qqbot.service /etc/systemd/system/qqbot.service
sudo systemctl daemon-reload
sudo systemctl enable --now qqbot     # 开机自启 + 立即启动
sudo journalctl -u qqbot -f           # 实时日志
```

> 配置务必写在项目根 `.env`（systemd 不继承 shell 环境变量）。unit 文件 `scripts/deploy/qqbot.service` 里的 `WorkingDirectory`/`ExecStart`/`User` 按实际路径修改。

## 备注
- WSS（出站）目前可用、内网友好；若腾讯日后弃用 WSS，需切 Webhook（要公网 HTTPS）。
- ID 是 openid（每个应用不同），无法从 QQ 号推导；因此没有「按 QQ 号设超管」。
- 富媒体：bot 支持**发图**（`send_image` 工具 / `ReplyImage`，群+私聊，走富媒体上传 MsgType=7）与**收图**（附件 URL 传入 agent）；原生 Markdown 群内受限（公域校验语料/异步审核，可能发不出或延迟）。频道发图 v1 不支持。
