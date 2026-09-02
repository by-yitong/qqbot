# 开发教程

本项目三层：`internal/bot`（插件框架，基于 botgo）→ `internal/agent`（harness）→ `plugins` + `main`（你的功能）。

- 写**普通功能**（命令/关键词/监听）→ 看下面「插件框架」；
- 给 AI 加**能力**（工具/技能/MCP）→ 看 [agent harness](agent.md)；
- **定时任务**（插件声明 / agent 自主创建）→ 看 [定时任务](scheduler.md)。

## 目录结构

```
qqbot/
├── main.go                  # 装配主干：读 .env → bot.New → 注册插件 → agent 装配 → Run
├── setup.go                 # agent 装配（工具注册 / MCP / LLM 主备熔断 / 配套插件）
├── cmd/imghost/             # 图床服务（独立二进制，部署到公网服务器；给 QQ URL 模式发图用）
├── internal/
│   ├── bot/                 # 插件框架（基于 botgo）
│   │   ├── bot.go           # Bot / Config / New / Run（WSS 装配 + 事件分发）+ 收图（attachments）
│   │   ├── ctx.go           # Ctx（三源：群/频道/私聊）+ ReplyText/Image/Voice + Images()/Mentions()
│   │   ├── command.go       # Command / Args / 命令匹配 / 作用域 / Help
│   │   ├── handler.go       # 处理器/中间件类型、监听器、Plugin、Use/UsePlugin
│   │   ├── media.go         # 发图：下载→上传富媒体→MsgType=7（支持 genimg: 图床引用）；发语音（silk）
│   │   ├── groupevent.go    # 群成员入群/入群申请事件（GROUP_MEMBER_ADD / GROUP_JOIN_REQUEST）
│   │   ├── groupmanage.go   # 入群申请审批/列表接口（approval_join_request / join_request_list）
│   │   ├── groupmute.go     # 群成员禁言接口（restrict_chat_setting）
│   │   ├── menu.go          # 自定义菜单与指令面板接口（PUT /v2/menu、POST /v2/panels）
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
│   │   └── tools/           # 内置工具：http_get / current_time / web_search / web_extract / generate_image / send_image / upload_image / search_history / create_task 等定时任务工具 / bash / file_*
│   ├── audio/               # 音频转 silk（QQ 官方语音消息格式；mp3/wav → silk，纯 Go 进程内）
│   ├── imgstore/            # 进程内图床（genimg: 引用 + TTL/容量淘汰 + 落盘归档）+ 远端图床客户端 Host
│   ├── memory/              # 长期记忆（SQLite/GORM）：全量落库(异步+去重) / 检索 / 近期动态（群隔离）
│   ├── scheduler/           # 定时任务（结构化计划 daily/weekly/interval/once + 落盘重启恢复 + 到点回调）
│   └── utils/               # 通用工具（共享 resty 客户端 + gjson + env 读取辅助）
├── pkg/                     # 与框架解耦的独立客户端
│   ├── bt/                  # 宝塔面板 API 客户端（/panel 插件用）
│   └── imgbed/              # 图床上传客户端（/图床 插件与 agent upload_image 工具共用）
├── plugins/                 # 插件（ping/whois/echo/counter/happy/system/zhipu/tasks/online/agent/recorder/session 会话命令/welcome 入群欢迎/joinreview 入群审批/video 短视频解析/bt 面板/imgbed 图床/mute 禁言/webhook）
├── skills/                  # 放 SKILL.md（技能，懒加载注入 agent system prompt 索引）
├── mcp.json.example         # MCP server 配置示例（复制为 mcp.json）
├── menu.json.example        # 自定义菜单/指令面板配置示例（复制为 menu.json；不复制用内置默认）
└── scripts/                 # dev.sh（air 热重载）+ deploy/（systemd）
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
	Desc("复读一句话").        // 进 /help
	Usage("/echo <文本>").
	Alias("say").             // 别名
	Hidden().                 // 不出现在 /help
	RequireAtOrPrefix().      // 必须带前缀或 @机器人 才触发（默认关：裸文本也可命中）
	OwnerOnly().              // 仅主人可用（BOT_OWNERS 配置的 openid；不配置 = 无人可用）
	GroupOnly()               // 作用域：GroupOnly/PrivateOnly/ChannelOnly
```
- 命令触发：**默认裸文本也可命中**——发送 `xxx` 等价 `.xxx`（群里不用 @ 不用前缀，指令面板/免前缀直达都靠它）。`.RequireAtOrPrefix()` 可收紧成必须前缀或 @（名字是常用词、易被群聊误触的命令建议收紧；AI 兜底不走命令表，群消息本来就要求 @ 或前缀）。命中命令后不再触发关键词/监听器。
- **主人权限**：`bot.New` 时 `bot.WithOwners(openid...)`（`.env` 配 `BOT_OWNERS`，逗号分隔）设定主人；`.OwnerOnly()` 的命令仅主人可用（如 `/webhook`），且不进非主人视角的 `/help` 与自动菜单/面板（面板对所有用户可见）。fail-closed：不配置主人则这类命令对所有人拒绝。`c.IsOwner()` 可在任意 handler 里判断发送者是否主人；自己的 openid 用 `/id` 命令查。
- `Args`：`c.Args.Join()` 取整段、`c.Args.Get(i)` 取第 i 个参数。

### `*bot.Ctx` 便捷方法
| 方法 | 作用 |
|---|---|
| `c.ReplyText(s)` | 纯文本回复（被动，锚定到当前消息；群/频道/私聊自动分支） |
| `c.ReplyMarkdown(md)` | 原生 markdown 回复（被动；群/私聊公域会被校验语料/异步审核） |
| `c.Text()` | 消息纯文本（已去 @ 前缀） |
| `c.IsGroup()` / `c.IsChannel()` / `c.IsPrivate()` | 消息来源 |
| `c.UserID()` / `c.GroupID()` / `c.ChannelID()` | openid（字符串） |
| `c.IsOwner()` | 发送者是否主人（`WithOwners` / `BOT_OWNERS` 配置） |
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

## 验证

```bash
go build ./...
go vet ./...
go test ./...        # 命令匹配 / 作用域 / 状态 / help 纯逻辑测试
gofmt -l .
```
