# 项目速查（selfdev 参考材料）

## 目录结构（写插件只需要关心 plugins/ 与 main.go）

```
qqbot/
├── main.go               # 装配主干：插件在 UsePlugin(...) 清单注册（你要 edit 的地方）
├── setup.go              # agent 装配（工具注册，一般不用动）
├── internal/bot/         # 插件框架：命令/Ctx/发送 API（当文档读，不改）
│   ├── ctx.go            # Ctx：c.ReplyText/ReplyMarkdown/Text()/Args/IsGroup()/UserID()…
│   ├── command.go        # 命令注册与作用域（Desc/Usage/Alias/OwnerOnly/GroupOnly…）
│   └── media.go          # 发图/语音/视频（b.SendGroupText 等主动发送在 bot.go）
├── internal/agent/       # AI 兜底 harness（一般不用动）
├── plugins/              # 全部插件在这里（一个功能一个文件 + 可选 _test.go）
├── skills/               # SKILL.md 技能（本技能所在地）
├── docs/development.md   # 插件框架完整教程（必读）
└── scripts/build-run.sh  # 部署脚本（deploy 工具内部会调，不要手动跑）
```

## 插件骨架模板（新建 plugins/xxx.go）

```go
package plugins

import (
	"qqbot/internal/bot"
	"qqbot/internal/utils"
)

// Xxx <一句话功能>。
type Xxx struct {
	// 依赖（HTTP client、调度器等）
}

// NewXxxFromEnv 按 .env 启停：未配置关键 env 返回 nil（UsePlugin 自动跳过）。
func NewXxxFromEnv() *Xxx {
	if utils.EnvOr("XXX_ENABLE", "0") != "1" {
		return nil
	}
	return &Xxx{}
}

// Install 注册命令（插件安装入口，框架在启动时调用）。
func (p *Xxx) Install(b *bot.Bot) {
	b.Command("xxx", p.run).
		Desc("一句话描述（进 /help）").
		Usage("/xxx <参数>")          // 可选
		// .Alias("别名")              // 可选
		// .RequireAtOrPrefix()        // 名字是常用词时必须加（防群聊裸文本误触）
		// .GroupOnly()                // 可选作用域
}

func (p *Xxx) run(c *bot.Ctx) {
	args := c.Args.Join()               // 整段参数；c.Args.Get(0) 取第 i 个
	if _, err := c.ReplyText("结果"); err != nil {
		// 发送失败要有日志/兜底
	}
}
```

main.go 注册（plugin_edit 在 `plugins.NewMusic(),` 之类的清单里加一行）：

```go
	b.UsePlugin(
		plugins.NewPing(),
		// …
		plugins.NewXxxFromEnv(),   // ← 加在这里
		plugins.NewMusic(),
	)
```

## Ctx 常用速查（完整版 docs/development.md）

| 调用 | 作用 |
|---|---|
| `c.ReplyText(s)` / `c.ReplyMarkdown(md)` | 回复当前消息（被动，自动分群/私聊/频道） |
| `c.Text()` / `c.Args.Join()` / `c.Args.Get(i)` | 消息文本 / 命令参数 |
| `c.IsGroup()` / `c.IsPrivate()` / `c.IsChannel()` | 来源判断 |
| `c.UserID()` / `c.GroupID()` / `c.IsOwner()` | 身份（openid，不是 QQ 号） |
| `c.Bot.SendGroupText(ctx, gid, "", s)` | 群主动发（空锚点；定时/推送场景） |
| `b.UserState(uid).Set(k, v)` / `.GetInt(k)` | 按用户状态 KV（GroupState 同理） |
| `b.OnKeyword("词", h)` / `b.OnGroupMessage(h)` | 非命令监听（Install 里注册） |

## selfdev 工具契约

| 工具 | 参数 | 说明 |
|---|---|---|
| `plugin_list` | 无 | 列 plugins/ 全部 .go（行数、是否测试） |
| `plugin_read` | path, offset?, limit? | 读仓库文件（带行号，默认 1-300 行，上限 500；path 为目录则列清单） |
| `plugin_write` | path, content | 新建/整文件覆盖（仅 plugins/*.go、main.go） |
| `plugin_edit` | path, old_string, new_string | 精确替换（old 必须唯一匹配；0/多次都会报错） |
| `deploy` | commit_message?, check_only? | 检查链 gofmt→vet→build→test 全过才 git 提交+异步重启；check_only=true 只检查 |

## 部署机制（知道即可，不要手动操作）

- deploy 检查链在仓库根同步执行，任一步失败**运行中的服务完全不受影响**，按报错改了再试。
- 全过后 git 只提交你本次 plugin_write/plugin_edit 改过的文件（`selfdev:` 前缀），15 秒后
  由分离进程调 `scripts/build-run.sh restart` 重启（同步重启会杀掉 bot 自己，所以是异步的）。
- 重启完成标志：主人收到「🤖 机器人已上线」私聊通知。若 1 分钟后没收到，让主人看
  `journalctl -u qqbot-agent --since "-5min"` 和 `data/selfdev-deploy.log`。
- 重启失败最常见原因：检查链没全过（不会到重启步）、systemd 重启卡住（单实例锁冲突，
  日志会每 5 秒崩溃重启——让主人 `systemctl status qqbot-agent` 排查）。
- 新命令会随启动自动进 /help 与 QQ 自定义菜单（AGENT_MENU 默认开），无需额外注册。

## 常见坑

- 命令 handler 里别做长阻塞（会堵后续消息）；重活开 goroutine。
- QQ 被动回复必须在收到消息 5 分钟内、锚定消息 id——`c.ReplyText` 已处理，主动发送
  （空锚点）受官方频控（单群 1000 条/天）。
- openid 是匿名 id，与 QQ 号无关；测试里写死任何 id 都无所谓。
- gofmt 规范：import 分组（标准库 / 第三方 / qqbot 内包），deploy 会自动格式化兜底，
  但别依赖它修结构。
- e2e 测试要加 build tag（如 `//go:build xxx_e2e`），默认 `go test` 不跑联网测试。
