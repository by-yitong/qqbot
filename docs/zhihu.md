# 知乎 AI（plugins/zhihu.go）

接 OpenAI 协议网关里的知乎官方模型 **zhida-ai**（即知乎 AI「看山」）。模型内置知乎
工具——**热榜、搜索、读问题/回答/文章**，均为实时数据（2026-09 实测）。配置
`ZHIHU_API_KEY` 即启用，无需其他依赖。

## 命令

| 命令 | 说明 |
|---|---|
| `/知乎` | 无参默认：看当前热榜（模型列表格式，前 10 条带链接） |
| `/知乎 <任意问题>` | 直接问。例：`/知乎 热榜第3条怎么回事`、`/知乎 搜索 GMP 调度的高赞回答`、`/知乎 为什么重庆可以在山上建城市 大家怎么说` |
| `/知乎 重置` | 清空本聊天的对话上下文（`清空` 同义） |
| `zhihu` | 英文别名 |

追问：每聊天（群/私聊各自独立）保留最近 12 条消息、闲置 30 分钟过期，所以可以
`/知乎 第2条展开讲讲`、`/知乎 再详细说说砂岩那点` 这样接着问。

引用回复：带着引用发 `/知乎` 会把被引消息作为上下文（如引用一条分享链接问
「总结下这个」；无参数时默认让模型看被引内容）。

## 输出

回复优先 markdown（加粗/列表/链接）；群聊 markdown 被语料审核拒发时自动降级
纯文本（链接展开成 `标题：url`、去加粗号）。system prompt 约束了篇幅（普通回答
300 字内、列表最多 10 条）、禁表格/代码块/大标题（QQ 不渲染）、无可靠来源不给链接。

## 配置（.env）

```ini
# 必填，未配置则插件不注册
ZHIHU_API_KEY=sk-xxx
# 可选：网关地址（默认知乎知答网关；任意 OpenAI 协议网关均可）
ZHIHU_BASE_URL=https://aihub.jmwl.dpdns.org/v1
# 可选：模型名（默认 zhida-ai；该网关另有 yyb-ai 应用宝模型）
ZHIHU_MODEL=zhida-ai
```

实现：复用 `internal/agent.LLMClient`（OpenAI Chat Completions，120s 超时、5xx 退避
重试），不带 tools——工具循环在网关侧模型内部完成。token 用量经 `OnUsage` 打日志
`[zhihu] zhida-ai 输入x 输出y 19.5s`。

## 验证

```bash
# 单测（会话裁剪/过期/重置、纯文本降级）
go test ./plugins/ -run TestZhihu -v
# 真实网关端到端（热榜/搜索，读 .env 的 ZHIHU_API_KEY）
go test ./plugins/ -run TestZhihuE2E -tags zhihu_e2e -v
```
