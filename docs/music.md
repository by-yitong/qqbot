# 点歌（plugins/music.go）

接入 [itapi.top](https://itapi.top) 的 6 个音乐接口（搜索 + 取播放直链），统一成
「搜索列表（≤6 条）→ 按钮选歌 → 发音频」的交互。无需鉴权，开箱即用。

## 命令

| 命令 | 说明 |
|---|---|
| `.点歌 天下` | 用**默认音源**点歌（`.音源` 可按聊天切换，落盘保留） |
| `.QQ点歌 天下` | QQ 音乐（别名 `qq点歌`） |
| `.网易点歌 天下` | 网易云（别名 `wy点歌`、`网易云点歌`） |
| `.酷我点歌 天下` | 酷我（别名 `kuwo点歌`） |
| `.酷我高音质点歌 天下` | 酷我高音质/无损（别名 `无损点歌`、`kuwohq点歌`） |
| `.汽水点歌 天下` | 抖音汽水音乐（别名 `qsyy点歌`） |
| `.5sing点歌 天下` | 5sing 翻唱社区（别名 `5sign点歌`） |
| `.选 2`（或 `.选歌 2`） | 搜索出列表后按序号取歌；点列表里的歌名标签等效 |
| `.音源` | 查看当前音源与全部可选——每个音源名都是标签（点击即切换默认），行尾「直接点歌→」标签填入该音源点歌命令 |
| `.音源 酷我` | 切换本聊天的默认音源（支持 key/中文名/别名，重启保留） |
| `.音源 重置` | 恢复全局默认音源 |

交互流程：搜索**只有 1 条**结果时直接发歌曲介绍 + 音频；**多条**时回复编号歌单
（最多 6 条），每首歌是**官方内嵌指令标签**（`qqbot-cmd-input`，客户端渲染为可点的
歌名标签，点击把「选 N」插入输入框，发送即取歌；回车指令 `qqbot-cmd-enter` 群聊
不支持，故统一用 input 版）。手动发送「选 序号」同样有效。待选列表 10 分钟过期；
无待选时「选 N」安静忽略（不刷屏）。频道场景与 markdown 被拒发时降级纯文本歌单
（`c.ReplyTaggedOrPlain`，音源列表同理）。

## 音源与发送方式

QQ 官方机器人没有音乐卡片消息，语音只认 silk 且本框架转码仅支持 mp3/wav，
因此按各音源直链格式选择通道：

| 音源 key | 名称 | 直链格式 | 发送方式 |
|---|---|---|---|
| `5sign`（默认） | 5sing音乐 | mp3 | **语音**（自动转 silk，需图床） |
| `qq` | QQ音乐 | mp3（免费曲）/ flac（VIP 曲） | 语音 / 文件 |
| `kuwo` | 酷我音乐 | mp3 | 语音 |
| `kuwohq` | 酷我高音质 | ogg / flac | 文件 |
| `qishui` | 汽水音乐 | m4a | 文件 |
| `netease` | 网易云音乐 | mp3 | 语音（见下） |

发送链路：**介绍**（markdown 内嵌封面图 + 歌名/歌手/时长/音质/页面链接，一条消息）
→ **音频**：mp3/wav 走语音（`ReplyVoice` 自动转 silk，需配图床，否则降级文件），
其余格式走**文件**（命名「歌名 - 歌手.ext」，QQ 可按扩展名预览），全失败发直链文本。
markdown（歌单/介绍）被群聊语料审核拒发时自动降级纯文本。

音频始终经本机中转（下载 → 转码/分片上传）：itapi 直链多为 http 且带时效签名，
QQ 的 URL 模式拉不动，必须中转。文件通道上限 200MB（无损 flac 也能发，就是慢）。

## 已知状态（2026-09 实测）

- itapi 文档页响应示例均为空，各接口字段按**实测**编写（见 `plugins/music.go` 注释）。
- **网易（wangyiyun）接口当前空响应**（GET/POST、各种参数组合均返回 HTTP 200 空
  body），插件按文档两步协议实现并做了字段兜底，上游恢复即自动可用；期间报
  「接口无响应」并引导换音源。
- QQ 音乐 VIP 曲目返回 flac（走文件通道）；免费曲返回 mp3 320k（走语音）。
- kw 老接口搜索步是纯文本列表（`type=json` 不生效），正则解析；取链步返回
  `json:` 前缀 + QQ 音乐卡片 JSON，从中提取 mp3 直链。

## 配置（.env，均可选）

```env
MUSIC_API_BASE=https://itapi.top/api   # itapi 基址
MUSIC_DEFAULT_SOURCE=5sign             # 全局默认音源 key
MUSIC_FILE=./data/music.json           # 每聊天音源偏好落盘路径
MUSIC_KUWO_BR=2                        # 酷我高音质档位：1 无损 flac / 2 高品质 / 3 标准
MUSIC_NETEASE_LEVEL=exhigh             # 网易音质 standard/higher/exhigh/lossless
```

语音通道需要图床（`IMGHOST_BASE_URL` 等，silk 转码产物要公网 URL，见
[docs/agent.md「图床」](agent.md)）；未配图床时 mp3 音源自动降级为文件发送。

## 验证

```bash
go test ./plugins/ -run TestMusic -v          # 单测（httptest 模拟各音源）
go test ./plugins/ -run TestMusicE2E -tags music_e2e -v   # 真实 itapi 端到端（联网）
```

`music_e2e` 用 build tag 隔离，常规 `go test ./...` 不会跑；上游接口漂移时用它排查。
