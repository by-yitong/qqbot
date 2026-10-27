# AGENTS.md —— 给 AI（ZCode 等）的项目记忆

QQ 官方机器人（Go；QQ 官方协议封装在 `pkg/botgo`——botgo v0.2.1 的本地 fork，上游停更，
新接口直接补 fork 里，`internal/bot` 及以上不放协议）。代码结构、插件框架、agent harness 的开发文档在
[docs/development.md](docs/development.md)（改代码前先读），这里只记**部署运维**和
容易踩的坑。

## 部署方式：systemd（唯一的生产运行方式）

生产进程由 systemd 托管，unit 名 **`qqbot-agent.service`**（`/etc/systemd/system/qqbot-agent.service`，
enabled 开机自启，`Restart=always` 崩溃自动拉起，User=admin，工作目录=仓库根，直接跑仓库根的 `./qqbot` 二进制）。

改完代码上线（标准流程，一条命令）：

```bash
cd /opt/1panel/www/sites/qiqi.prizen.cn/golang/qqbot
./scripts/build-run.sh              # 编译 → systemctl restart → 确认 active
./scripts/build-run.sh restart      # 不编译直接重启（改了 .env 重载配置用）
```

`build-run.sh` 是 systemd 的封装（2026-09-16 起不再手动 nohup 跑进程）：
编译失败不动正在运行的服务；重启前自动停 air；`stop` / `status` / `logs`
子命令对应 systemctl / journalctl。等价的手动方式：

```bash
go build -o qqbot . && go test ./... && sudo systemctl restart qqbot-agent
```

推送到 GitHub master 也会触发自动部署（`.github/workflows/deploy.yml`，Secrets 已配
`DEPLOY_SSH_HOST/USER/KEY`）：CI 先跑提交前验证四件套（build/vet/gofmt/test），过了由
runner 把 master **push 到生产仓**——生产机在墙内、自己拉 GitHub 不稳（GnuTLS 抖动），
方向反转为 runner→生产；生产仓 `receive.denyCurrentBranch=updateInstead`，工作树随
push 原地更新——再 SSH 执行 `build-run.sh`。生产仓工作区脏时拒绝部署；生产仓有未同步
的本地提交（如 selfdev 产物）时 push 非 fast-forward 被拒，不会强推。

- **重启必须走 systemctl**（或上面的脚本）。SIGINT 优雅停（15s 超时强杀），机器人会断开 WSS 再重连。
- 看状态/日志（journalctl 免 sudo 可读）：
  ```bash
  systemctl status qqbot-agent
  journalctl -u qqbot-agent -f          # 实时
  journalctl -u qqbot-agent --since "-10min"
  ```
- 确认上线的标志：日志里出现 `connected` + `READY`，以及「机器人已上线」私聊通知。

## ⚠️ 单实例锁：别用手动方式跑生产

`main.go` 有单实例守护（`data/bot.pid`）：已有活着的 qqbot 进程时新进程直接退出。
历史教训：**手动 `./scripts/build-run.sh` 拉起的实例和 systemd 服务抢锁，会让
systemd 每 5 秒崩溃重启刷屏**（2026-09-16 已清理，统一走 systemd）。

- **不要手动跑生产进程**：`nohup`/`setsid` 直接起 `./qqbot` 会和 systemd 服务抢锁，
  让服务每 5 秒崩溃重启刷屏（2026-09-16 已清理过一次，统一走 systemd）；
- 开发热重载用 `./scripts/dev.sh`（air）：**先 `sudo systemctl stop qqbot-agent`**
  （否则 air 起不来），改完再 `./scripts/build-run.sh restart` 收尾；
- 万一锁异常（pid 文件指向活进程但想强启）：`rm data/bot.pid` 可跳过检查——正常情况不需要。

## 其他要点

- 消息能力现状（2026-09 实测）：**主动消息已开通**——群/私聊空锚点（`msgAnchor` 传空）即可
  主动发，上线通知、入群欢迎、webhook 推送、定时任务都在用（受官方频控/额度：群主动消息单群
  1000 条/天）；**原生 markdown 可发**——群/私聊 `MsgType=2`，含内嵌指令标签
  （`bot.CmdInputTag`）与 `![图片](url)` 图片语法，无需建 md 模板。⚠️ **keyboard 按钮分场景**：
  被动消息带按钮可用（点歌选歌、菜单在用）；**主动消息带按钮会被官方整条拒绝**
  （40011021「rows.buttons.id 无效」，2026-10 实测），事件触发的可点交互一律用指令标签。
  官方公域仍保留 md 异步审核权，个别消息可能延迟，发送失败要留纯文本兜底。
  详见 docs/development.md「Bot 级发送」。
- 配置在仓库根 `.env`（参考 `.env.example`），改了要 `systemctl restart` 才生效；
  条件插件按 env 启用（各 `NewXxxFromEnv()`）。
- `scripts/deploy/qqbot.service` 是 unit 模板（与本机已装的 `qqbot-agent.service` 保持一致）；
  改 unit 后要 `sudo systemctl daemon-reload`。
- 提交前验证：`go build ./... && go vet ./... && go test ./... && gofmt -l .`。
- **selfdev（AI 自改插件，默认关）**：`AGENT_ENABLE_SELFDEV=1` 且配了 `BOT_OWNERS` 时，
  bot 的 agent 获得 plugin_list / plugin_read / plugin_write / plugin_edit / deploy 五个工具
  （`internal/agent/tools/selfdev.go` + `skills/self-dev/`），仅主人可用：写限 plugins/*.go
  与 main.go；deploy 检查链（gofmt/vet/build/test）全过才 git 提交（`selfdev:` 前缀，只提交
  AI 改的文件）并由 setsid 分离进程 15s 后调 build-run.sh restart（同步重启会杀掉 bot 自身）。
  部署日志 `data/selfdev-deploy.log`。前提：bot 进程用户 `sudo systemctl restart` 免密。
- 机器人自身行为语义（命令裸命中、@ 过滤等）见 docs/development.md；
  群消息里未点名本 bot 的两类消息整条忽略：@ 了其他机器人（bot.go `mentionsOtherBot`）、
  @全体成员（`mentionsEveryone`——注意官方给 @全体成员 的 mention 也带 `is_you:true`
  +`scope:"all"`，判定点名必须用 `atSelf` 排除 scope=all，2026-10 曾因此抢答全体公告）。
