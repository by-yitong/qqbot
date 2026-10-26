# 开发教程

本项目三层：`internal/bot`（插件框架，基于 pkg/botgo）→ `internal/agent`（harness）→ `plugins` + `main`（你的功能）。

**QQ 官方协议（HTTP API）统一放 `pkg/botgo`**（botgo v0.2.1 的本地 fork，上游已停更）：
官方新出的群管理/菜单/富媒体等接口直接补在 fork 里（dto 结构 + `openapi/v1/` 实现 +
`openapi/iface.go` 子接口，照现有文件的样式写）。`internal/bot` 及以上**不放协议**——
只做连接保护（`b.openapi()` → `ErrNotConnected`）、类型别名与业务编排（便捷组合、
自动翻页聚合、菜单发布编排等），插件/agent 永远只调 `internal/bot` 的方法。

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
│   ├── bot/                 # 插件框架（基于 pkg/botgo，只做编排不含协议）
│   │   ├── bot.go           # Bot / Config / New / Run（WSS 装配 + 事件分发）+ 收图（attachments）
│   │   ├── ctx.go           # Ctx（三源：群/频道/私聊）+ ReplyText/Image/Voice + Images()/Mentions()
│   │   ├── command.go       # Command / Args / 命令匹配 / 作用域 / Help
│   │   ├── handler.go       # 处理器/中间件类型、监听器、Plugin、Use/UsePlugin
│   │   ├── media.go         # 发图/视频/语音/文件编排：上传富媒体（协议在 pkg/botgo）→MsgType=7；语音 mp3/wav 自动转 silk
│   │   ├── groupevent.go    # 群成员入群/入群申请事件（GROUP_MEMBER_ADD / GROUP_JOIN_REQUEST）
│   │   ├── group.go         # 群管理 API 门面（信息/成员/黑名单/禁言/入群审批，协议在 pkg/botgo）
│   │   ├── menu.go / recall.go # 菜单/撤回的薄委托层（类型别名 + b.openapi() 转发）
│   │   ├── state.go         # 会话状态（按 openid 的 KV）
│   │   └── doc.go           # 包文档
│   ├── agent/               # agent harness（工具循环 / 记忆 / 压缩 / 技能 / MCP）
│   │   ├── agent.go         # Agent + Run（工具循环：调 LLM → 执行工具 → 再调）
│   │   ├── message.go       # 内部消息模型 + ToOpenAIMessages（边界转换）
│   │   ├── tool.go          # Tool 接口 + Registry + 动态工具区（ToolGuideline）
│   │   ├── llm.go           # OpenAI 兼容 Chat Completions 客户端
│   │   ├── session.go       # 每用户多轮历史（JSONL 落盘 + 每用户锁 + 按消息时间戳的时间衰减 TrimByAge）
│   │   ├── compaction.go    # 上下文压缩（阈值/增量摘要 + 溢出自救重试）
│   │   ├── skill.go         # SKILL.md 加载 + 懒加载索引（配合 read_skill）
│   │   ├── mcp.go           # MCP 客户端（stdio/http/sse，工具并入 Registry）
│   │   └── tools/           # 内置工具：http_get / current_time / web_search / web_extract / generate_image / send_image / upload_image / search_history / create_task 等定时任务工具 / bash / file_*
│   ├── audio/               # 音频转 silk（QQ 官方语音消息格式；mp3/wav → silk，纯 Go 进程内）
│   ├── imgstore/            # 进程内图床（genimg: 引用 + TTL/容量淘汰 + 落盘归档）+ 远端图床客户端 Host
│   ├── memory/              # 长期记忆（SQLite/GORM）：全量落库(异步+去重) / 检索 / 近期动态（群隔离）
│   ├── scheduler/           # 定时任务（结构化计划 daily/weekly/interval/once + 落盘重启恢复 + 到点回调）
│   └── utils/               # 通用工具（共享 resty 客户端 + gjson + env 读取辅助）
├── pkg/
│   ├── botgo/               # QQ 官方协议封装（botgo v0.2.1 本地 fork，上游停更后在此维护：
│   │                        #   dto/ 新结构 + openapi/v1/ 实现 + openapi/iface.go 子接口；
│   │                        #   已补：群信息/禁言/成员/黑名单/入群审批/自动审批策略、菜单与
│   │                        #   指令面板、富媒体上传（URL 模式 + 分片直传）；新协议都补这里）
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
	AdminOnly().              // 仅群主/群管理员或主人可用（member_role 来自群消息事件下发）
	GroupOnly()               // 作用域：GroupOnly/PrivateOnly/ChannelOnly
```
- 命令触发：**默认裸文本也可命中**——发送 `xxx` 等价 `.xxx`（群里不用 @ 不用前缀，指令面板/免前缀直达都靠它）。`.RequireAtOrPrefix()` 可收紧成必须前缀或 @（名字是常用词、易被群聊误触的命令建议收紧；AI 兜底不走命令表，群消息本来就要求 @ 或前缀）。命中命令后不再触发关键词/监听器。
- **主人权限**：`bot.New` 时 `bot.WithOwners(openid...)`（`.env` 配 `BOT_OWNERS`，逗号分隔）设定主人；`.OwnerOnly()` 的命令仅主人可用（如 `/禁言`），且不进非主人视角的 `/help` 与自动菜单/面板（面板对所有用户可见）。fail-closed：不配置主人则这类命令对所有人拒绝。`c.IsOwner()` 可在任意 handler 里判断发送者是否主人（webhook 插件的群地址相关操作即 handler 内校验）；自己的 openid 用 `/id` 命令查。
- **管理员权限**：`.AdminOnly()` 的命令仅**群主/群管理员或主人**可用（如 `/踢`）。角色来自官方群消息事件下发的 `member_role`（`author.member_role`，botgo v0.2.1 的 dto 未解析，框架在群消息入口经 `b.EnrichGroupRoles` 从原始 payload 补齐）；主人恒可用，非群聊或角色未知时只剩主人可用（fail-closed）。`c.IsGroupAdmin()` / `c.MemberRole()` 可在 handler 里判断；被 @ 者的角色与机器人标记在 `c.Mentions()` 返回项的 `.Role` / `.Bot` 上。
- `Args`：`c.Args.Join()` 取整段、`c.Args.Get(i)` 取第 i 个参数。

### `*bot.Ctx` 便捷方法
| 方法 | 作用 |
|---|---|
| `c.ReplyText(s)` | 纯文本回复（被动，锚定到当前消息；群/频道/私聊自动分支） |
| `c.ReplyMarkdown(md)` | 原生 markdown 回复（被动；本机器人实测可发） |
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
| `b.SendGroupText(ctx, groupID, msgAnchor, text)` | 群：msgAnchor 空=**主动**（本机器人已开通）；非空=被动回复 |
| `b.SendC2CText(ctx, userID, msgAnchor, text)` | 私聊：msgAnchor 空=**主动**（本机器人已开通）；非空=被动回复 |

原生 markdown（`MsgType=2`）：
| 方法 | 语义 |
|---|---|
| `b.SendChannelMarkdown(ctx, channelID, md)` | 频道主动发原生 md（频道宽松） |
| `b.SendGroupMarkdown(ctx, groupID, msgAnchor, md)` | 群：msgAnchor 空=主动；非空=被动 |
| `b.SendC2CMarkdown(ctx, userID, msgAnchor, md)` | 私聊：同上 |

> **本机器人现状（2026-09 实测）**：主动消息已开通、原生 markdown 可发——群/私聊空锚点
> 主动发（上线通知、入群欢迎、webhook 推送、定时任务在用）与原生 md（含内嵌指令标签、
> `![图片](url)` 图片语法）都正常，**无需**在 q.qq.com 建 markdown 模板。官方公域语义上仍保留
> 主动消息频控/额度（群主动消息单群 1000 条/天）与 md 异步审核权，个别消息可能延迟——
> 发送失败要留纯文本兜底（webhook 插件已有先例）。频道一直可主动发。处理消息时用
> `c.ReplyText` / `c.ReplyMarkdown` 即可（自动按来源分支 + 取锚点）。
>
> ⚠️ **keyboard 按钮**：被动消息带 keyboard 可用（点歌选歌、菜单在用）；**主动消息带
> keyboard 会被官方整条拒绝**（40011021「rows.buttons.id 无效」，2026-10 实测），事件触发
> 场景的可点交互用内嵌指令标签（`CmdInputTag`）代替（入群审批提醒卡片如此）。
>
> ⚠️ **富媒体 URL 模式的拉取时限**：POST /files 给 URL 后是 QQ 服务端代拉，实测有
> ~10s 时限（超时报 code 850027「富媒体文件上传超时」）+ 视频 30M 软限——几十 MB 的
> 大视频 URL 模式必然失败（抖音直链、自建图床中转都一样，2026-10 生产实测 61MB 视频
> 双双超时）。大文件走分片直传：`c.ReplyMediaBytes(fileType, filename, data)`（我们
> 推字节给 QQ，协议见 pkg/botgo richmedia.go；openapi 客户端超时已放宽 60s）。

**内嵌指令标签**（md 正文里的可点命令，`internal/bot/tag.go`）：
- `bot.CmdInputTag(text, show)` 生成官方 `qqbot-cmd-input` 标签：客户端把该段渲染成
  可点标签，点击把 text 插入输入框（用户补发送）；text/show 按官方要求 urlencode、
  上限 100 字符。同款「回车指令」`qqbot-cmd-enter`（点击直接发送）**群聊/文字子频道
  不支持**，本项目统一用 input 版。标签必须随 markdown 发，纯文本里就是乱码。
- `c.ReplyTaggedOrPlain(md, plain)` 发标签版 markdown 的标准姿势：频道场景与 md 被
  审核拒发时自动降级纯文本（plain 传同内容的无标签版）。
- 在用：点歌歌单/音源列表（`plugins/music.go`）、`帮助`（`b.HelpMarkdownFor(c)`，
  与 `HelpFor` 同一套可见性过滤）、`menu` 查看、审批列表（`plugins/joinreview.go`）、
  任务列表（`plugins/tasks.go`）、黑名单列表（`plugins/member.go`，openid 手打不出，
  点「解除」标签是唯一实用路径）。

### 用户头像 URL（免鉴权直取）

```
https://q.qlogo.cn/qqapp/{appid}/{openid}/{size}
```

- `appid` 必须是**本 bot 的 `QQBOT_APP_ID`**——openid 按 app 隔离，appid 不匹配时
  不报错，**静默返回 40×40 默认灰头像**（约 1.5KB）；群 openid / 未知 openid 同样只给默认头像。
- `size` 单段可选 `0` / `40` / `100` / `140` / `640`（640 为大图；两段后缀如 `/0/100` 返回 400）。
- 免鉴权 GET、HTTP 200 image/jpeg，可直接当富媒体图片 URL 用（发图、入群审批提醒卡片
  经 `![avatar](url)` 内嵌申请人头像等）。判断「是不是默认头像」可看 Content-Length 是否 ~1.5KB / 尺寸 40×40。
- 封装在 `internal/bot/avatar.go`：`b.AvatarURL(openid)`（单人 640 直链）、
  `b.AvatarURLTemplate()`（`{openid}` 占位模板）。在用：入群审批提醒卡片内嵌申请人头像
  （`plugins/joinreview.go` notify）、group_members 工具输出尾部附头像模板提示
  （`internal/agent/tools/group.go`）。
- 2026-09 实测：appid 102898720 + 用户 openid → 640×640 真实头像；换 appid 或传群
  openid → 同一张 40×40 占位图（md5 一致）。无需调用官方用户信息接口，无 QPS 顾虑。

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
默认开启，从非隐藏命令生成帮助列表（`b.Help()` 取文本）。中文 `帮助` 与内置 `help`
命令回标签版（`b.HelpMarkdownFor(c)`）：每条命令名是 `CmdInputTag` 标签，点击填入
输入框，带 Usage 的命令 text 尾补空格供用户接敲参数；可见性过滤
（Hidden/OwnerOnly/AdminOnly）与纯文本版同一套。

帮助按分类分组展示：命令注册时用 `.Category(bot.CatXxx)` 归类（常量见 `command.go`：
通用/娱乐/音乐/群管/AI 助手/定时任务/管理；不设置归「通用」）。组内保持注册顺序，
分组顺序取各分类首个命令的注册先后。

## 验证

```bash
go build ./...
go vet ./...
go test ./...        # 命令匹配 / 作用域 / 状态 / help 纯逻辑测试
gofmt -l .
```

## 让 AI 自己写插件（selfdev，默认关）

配置 `AGENT_ENABLE_SELFDEV=1`（且必须配 `BOT_OWNERS`，fail-closed）后，bot 的 agent
获得给自己写插件的能力（`internal/agent/tools/selfdev.go` + `skills/self-dev/` 技能）：

- **工具**（全部仅主人可用）：`plugin_list`（列插件）、`plugin_read`（读仓库文件，带行号
  分段；`.env*`/`data/` 等敏感路径不可读）、`plugin_write` / `plugin_edit`（写/精确替换，
  **只允许 `plugins/*.go` 与 `main.go`**）、`deploy`（部署）。
- **deploy 流程**：检查链 `gofmt → go vet → go build → go test` 全过才继续（任一步失败
  运行中的服务不受影响）→ git 只提交本次 AI 改过的文件（`selfdev:` 前缀）→ setsid 分离
  进程 15s 后调 `scripts/build-run.sh restart` 异步重启（同步重启会杀掉 bot 自身、工具
  调用无法返回）→ 重启完成主人收到「机器人已上线」通知。`check_only=true` 只跑检查不重启。
- 发起人身份（是否主人）经 `tools.WithRequester` 注入 agent ctx（plugins/agent.go，
  所有聊天来源），工具侧每次调用再校验——未注入/非主人一律拒绝。
