# qqbot

基于腾讯官方 [botgo](https://github.com/tencent-connect/botgo) SDK 的 QQ 机器人，自带一个轻量插件框架（命令 / 中间件 / 监听器 / 插件 / 会话状态），外加一个**轻量 agent harness**（工具循环 / 多轮记忆 / 上下文压缩 / 技能 / MCP）。

- **官方机器人**：走 `q.qq.com` 注册的机器人（AppID + AppSecret），合规、稳定。
- **WSS 出站**：机器人主动连接腾讯（内网/NAT 友好，无需公网 HTTPS）。
- **插件框架** `internal/bot`：写功能只关心业务，handler 收一个 `*bot.Ctx`。
- **agent harness** `internal/agent`：未命中命令的消息交给一个可调工具、有记忆、能压缩上下文的 agent（设计参考 [pi](https://github.com/earendil-works/pi) 的 coding-agent）。
- 内置插件：`ping`（在线检测 / 真实 ping）/ `whois` / `echo` / `counter`（会话状态）/ `happy`（每日笑话）/ `agent`（AI 兜底）/ `video`（抖音/快手短视频解析）/ `panel`（宝塔面板状态）/ 入群欢迎 / 入群审批 / 禁言 等更多。

> ⚠️ 官方机器人约束：**只能被动回复**（收到消息后 5 分钟内回复）、**用户/群 ID 是 openid（不是 QQ 号）**、踢人等群管仍无官方接口。主动推送（上线通知、入群欢迎、定时任务等）需在 q.qq.com 开通**主动消息**权限。官方 2026-08 起新增了**群成员入群事件、入群申请事件与审批接口**，入群欢迎/自动审批已可用（见 [群管功能](docs/group.md)）。
>
> 群消息：默认只收 @ 消息（`GROUP_AT_MESSAGE_CREATE`）；在 q.qq.com 开通**全量群消息**权限后可收 `GROUP_MESSAGE_CREATE`（无需 @，本框架已支持两者）。

## 文档

| 文档 | 内容 |
|---|---|
| [docs/development.md](docs/development.md) | 开发教程：目录结构、快速开始、插件框架（命令 / Ctx / 发送 / 中间件 / 写插件）、验证 |
| [docs/agent.md](docs/agent.md) | Agent harness：工具循环 / 记忆（短期+长期）/ 压缩 / 技能 / MCP / 图床部署 / 内置工具 / 关键 env |
| [docs/scheduler.md](docs/scheduler.md) | 定时任务：对话里让 AI 建任务 / 插件声明任务 |
| [docs/group.md](docs/group.md) | 群管功能：入群欢迎 / 入群申请自动审批（AI 判答案）/ 群成员禁言 |

## 安装与运行

依赖（`go.mod` 已含）：`github.com/tencent-connect/botgo`、`github.com/joho/godotenv`、`resty.dev/v3`、`github.com/tidwall/gjson`、`github.com/mark3labs/mcp-go`（MCP）、`gopkg.in/yaml.v3`（技能）。

```bash
cp .env.example .env          # 填入 QQBOT_APP_ID / QQBOT_APP_SECRET
go run .
```

`.env` 关键字段（去 `q.qq.com` 后台拿，完整清单见 `.env.example` 注释）：
```env
QQBOT_APP_ID=你的 AppID
QQBOT_APP_SECRET=你的 AppSecret
BOT_PREFIXES=/                # 命令前缀（可选，多个用 | 分隔）
BOT_OWNERS=                   # 主人 openid（逗号分隔）：/webhook 等 OwnerOnly 命令仅主人可用
AI_API_KEY=                   # 配置则启用 agent 兜底（工具循环/多轮记忆/压缩/技能/MCP）
```

> **发图需要图床**：QQ 富媒体走 URL 模式，需要一个公网可达的文件地址（本地直存或独立部署 `cmd/imghost`），配置方法见 [docs/agent.md「图床」](docs/agent.md)。

## 部署

开发期用 `air` 热重载（`./scripts/dev.sh start`）；**长期后台运行**用 systemd：

```bash
go build -o qqbot .
sudo cp scripts/deploy/qqbot.service /etc/systemd/system/qqbot.service
sudo systemctl daemon-reload
sudo systemctl enable --now qqbot     # 开机自启 + 立即启动
sudo journalctl -u qqbot -f           # 实时日志
```

> 配置务必写在项目根 `.env`（systemd 不继承 shell 环境变量）。unit 文件 `scripts/deploy/qqbot.service` 里的 `WorkingDirectory`/`ExecStart`/`User` 按实际路径修改。

## 验证

```bash
go build ./...
go vet ./...
go test ./...
gofmt -l .
```

## 备注
- WSS（出站）目前可用、内网友好；若腾讯日后弃用 WSS，需切 Webhook（要公网 HTTPS）。
- ID 是 openid（每个应用不同），无法从 QQ 号推导；因此没有「按 QQ 号设超管」。
- 富媒体：bot 支持**发图**（`send_image` 工具 / `ReplyImage`，群+私聊，走富媒体上传 MsgType=7）与**收图**（附件 URL 传入 agent）；原生 Markdown 群内受限（公域校验语料/异步审核，可能发不出或延迟）。频道发图 v1 不支持。
