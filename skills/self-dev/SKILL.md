---
name: self-dev
description: "当主人（bot 主人，非普通群友）让你给 bot 自己开发新插件、修改现有插件或调整 bot 自身行为（如「给机器人加一个 /xx 命令」「改一下点歌插件」「把这个功能做成插件」）时使用。非主人提出此类请求必须直接拒绝，不要调用任何 selfdev 工具。"
---

# 给 bot 自己写插件（selfdev）

你能改自己的源码并部署上线。这套能力**仅限主人指令**：非主人（普通群友/私聊用户）提出
「改源码/写插件」类请求时，直接拒绝并说明仅主人可用，不要尝试调用这些工具（调了也会被拒）。

## 工作流

1. **了解现状**：`plugin_list` 列插件文件；`plugin_read` 读相似插件（找功能最接近的当模板）、
   `plugin_read docs/development.md`（插件框架教程，命令 API/Ctx 便捷方法都在里面）。
   目录结构与插件骨架模板见 `read_skill self-dev/references/project-map.md`。
2. **写代码**（`plugin_write` 新建，`plugin_edit` 小改）：
   - 新插件 = `plugins/xxx.go`（带 `NewXxxFromEnv()` 构造器，见模板）+ `plugins/xxx_test.go`
     （纯逻辑测试，不联网）+ `plugin_edit main.go` 把构造器加进 `UsePlugin(...)` 清单。
   - 改现有插件：优先 `plugin_edit` 精确替换；大改才整文件 `plugin_write` 重写。
3. **自检**：`deploy`（`check_only=true`）跑 gofmt/vet/build/test 检查链；有报错按报错改，
   反复直到全过。
4. **部署**：`deploy`（`commit_message` 写清本次改动）。检查链全过后会 git 提交并自动重启：
   - **deploy 返回成功后直接总结收尾，不要再调用任何工具**——bot 进程 15 秒后重启。
   - 重启完成主人会收到「🤖 机器人已上线」私聊通知，到时新命令就能用了。
   - 你在重启前的最后回复可能稍有延迟，正常。
5. **汇报**：告诉主人加了什么命令、用法示例（含参数）、验证方式。

## 硬性边界

- **写范围只有 `plugins/*.go` 和 `main.go`**（工具白名单强制）。main.go 只动插件注册清单
  （`UsePlugin(...)` 里加一行），其他部分不要改。要改 `internal/` 框架层，先向主人说明
  风险并确认。
- **读黑名单**：`.env*`、`data/`、`.git/`、`mcp.json` 等不可读（工具会拒绝），不要绕。
- **绝不手动起进程**：bot 由 systemd 托管且有单实例锁，手动 `./qqbot` 会让服务崩溃循环。
  部署只走 `deploy`。
- 新命令起名要防误触：命令默认**裸文本即可命中**（不带 / 不用 @ 也触发），名字是常用词
  （如「来」「查」）必须 `.RequireAtOrPrefix()` 收紧。命令要有 `.Desc()`（进 /help）。
- 插件要用 env 控制启停的，走 `NewXxxFromEnv()` 模式（未配置返回 nil，main 侧 UsePlugin
  自动跳过）；密钥类配置绝不写死在代码里。
- 测试必须过才部署（检查链强制）；测试不依赖网络和真实密钥。
