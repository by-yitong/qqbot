# QQ 官方三条新消息能力接入设计（图文卡片 / @全体成员 / 素材管理）

- 日期：2026-11-03
- 状态：设计已获用户批准（对话中逐节确认）
- 官方来源：
  - 素材管理 <https://bot.q.qq.com/wiki/develop/api-v2/server-inter/message/material-management.html>
  - 发送群聊消息 <https://bot.q.qq.com/wiki/develop/api-v2/autogen/api/v2_groups_group_openid_messages.post.html>
  - 发送单聊消息 <https://bot.q.qq.com/wiki/develop/api-v2/autogen/api/v2_users_user_openid_messages.post.html>
  - 官方变更记录（用户提供的更新日志：20261021 素材管理、20261026 群聊 @全部成员、20261028 图文卡片）

## 背景与目标

QQ 开放平台 2026-10 新增三项消息能力，本设计将它们接入本仓库（协议补进 `pkg/botgo` fork，能力暴露到 `internal/bot`）：

1. **图文卡片消息**：`msg_type=8` + `card` 字段，群聊/单聊均支持。
2. **@全体成员标签**：群聊文本（msg_type=0）/markdown（msg_type=2）内容内嵌 `<qqbot-at-everyone />`。
3. **素材管理**：图片上传平台换取 `qqbot-media://` 引用，markdown 中引用，摆脱公网图片 URL 被 QQ 服务端限时拉取的不稳定问题。

### 用户已定的边界（决策记录）

| 决策点 | 结论 |
|---|---|
| 接入范围 | 三项全接 |
| 素材管理深度 | 协议层 + 一步上传；**不做**本地 md5 去重缓存，**不改**现有 md 图片发送路径；100 张上限靠查询接口自然管控 |
| @全体成员暴露面 | 仅工具函数（常量），不加指令/不加 agent 工具/不改插件 |
| 图文卡片暴露面 | 仅 Bot 级发送方法，不接 agent 工具/不接具体插件 |

### 非目标

- 不新增任何用户指令、插件改动、agent 工具。
- 不做素材本地缓存/去重/额度记账（@全体 1 次/天的额度由服务端拒绝，调用方自行处理错误）。
- 无新数据表、无新配置项。

## 官方 API 事实（契约依据，子 agent 以此为准，勿再猜）

### 图文卡片（msg_type=8）

群聊与单聊的 messages 端点均支持。请求体：

```json
{
  "msg_type": 8,
  "msg_id": "ROBOT1.0_xxx...",
  "msg_seq": 2,
  "card": {
    "type": "tuwen",
    "content": {
      "title": "标题 1-50 字",
      "description": "描述 1-200 字",
      "url": "https://.../link",
      "pic_url": "https://.../img.jpg"
    }
  }
}
```

约束：`type` 固定 `tuwen`；title 1-50 字符、description 1-200 字符；`url` 与 `pic_url` 必须 https 且长度 ≤1024。与既有锚点语义一致（`msg_id` 空=主动、非空=被动回复）。

### @全体成员标签

- 写法：`<qqbot-at-everyone />`（自闭合带空格）。
- 适用：群聊场景 msg_type=0 的 `content` 与 msg_type=2 的 `markdown.content`。
- 限制：机器人须为该群**管理员**；**每个群每天仅 1 次**；单聊不支持。
- 无独立接口，纯内容内嵌。

### 素材管理（/v2/media-images）

流程：`prepare` → PUT 预签名 URL 直传二进制 → `complete` 得引用。

1. `POST /v2/media-images/prepare`
   请求：`{file_name(含扩展名), file_size(字节), content_type(image/png|jpeg|gif|webp), md5(32位hex), sha1(40位hex)}`
   响应：`{upload_id(4h 有效), presigned_url, expires_in(秒)}`
2. `PUT <presigned_url>`：直传图片二进制；请求头 Content-Type 必须与 prepare 声明的 `content_type` 完全一致（否则 COS 403）；响应头 `ETag`（去引号）即文件 MD5。
3. `POST /v2/media-images/complete`
   请求：`{upload_id, md5(=上传返回的 ETag)}`；服务端与 COS 实际 ETag 比对，不一致返回 40035003 口径下的失败。
   响应：`{image_id, url(qqbot-media://image/v1/<token>), ttl(秒，默认 7776000=90 天滑动), preview_url(临时预览，勿持久化)}`
4. `GET /v2/media-images?cursor={next_cursor}`：`{images:[{image_id,url,file_name,preview_url,created_at,expires_at}], next_cursor, total}`
5. `DELETE /v2/media-images/{image_id}/delete`：空 JSON 响应。

限制：单图 ≤10MB；格式 png/jpeg/gif/webp（扩展名与 content_type 须一致）；单机器人上限 100 张；引用 90 天滑动过期（每次被消息引用顺延）；上传任务 4 小时有效。

markdown 引用语法（与公网图片一致，宽高可选）：
`![说明 #800px #400px](qqbot-media://image/v1/<token>)`；可与公网图片混用；引用与机器人绑定。

错误码：40035001 格式不支持 / 40035002 超 10MB / 40035003 任务不存在或过期 / 40035006 服务端内部错误 / 40035007 超百张 / 40035008 图不存在 / 40035009 已过期 / 40035010 引用无效。

## 设计

### 1. 协议层 `pkg/botgo`

**`dto/message_create.go`**：

```go
CardMsg MessageType = 8 // 图文卡片
// MessageToCreate 新增字段：Card *Card `json:"card,omitempty"`

type Card struct {
    Type    string      `json:"type"`    // 固定 "tuwen"
    Content CardContent `json:"content"`
}
type CardContent struct {
    Title       string `json:"title"`
    Description string `json:"description"`
    URL         string `json:"url"`
    PicURL      string `json:"pic_url"`
}
```

（全字符串字段，无 `richMediaMsg` 那种 []byte 双重编码陷阱，直接挂 `MessageToCreate`。）

**`dto/media_image.go`（新文件）**：prepare 请求/响应、complete 请求/响应、list 响应（含 images 数组与 next_cursor、total）结构体。一步式与 complete 的返回统一命名为 `dto.MediaImage{ImageID, URL, PreviewURL, TTL}`。

**`openapi/iface.go`**：新能力接口组（同 `RichMediaUploadAPI` 的组织方式）：

```go
type MediaImageAPI interface {
    // UploadMediaImage 一步式：算 md5/sha1 → prepare → 预签名 PUT 直传 → complete。
    UploadMediaImage(ctx context.Context, fileName, contentType string, data []byte) (*dto.MediaImage, error)
    PrepareMediaImage(ctx context.Context, req dto.MediaImagePrepareReq) (*dto.MediaImagePrepareRsp, error)
    CompleteMediaImage(ctx context.Context, req dto.MediaImageCompleteReq) (*dto.MediaImageCompleteRsp, error)
    ListMediaImages(ctx context.Context, cursor string) (*dto.MediaImageListRsp, error)
    DeleteMediaImage(ctx context.Context, imageID string) error
}
```

（一步式必须进 iface：`internal/bot` 经 `b.api`（接口类型）调用，同 `UploadGroupFileChunked` 先例。）

**`openapi/v1/media_image.go`（新文件）**：

- 四个原子方法 + 一步式 `UploadMediaImage` 实现（原子方法用 `o.request(ctx)...` 既有模式）。
- 一步式：算 md5/sha1 → prepare → 独立 http.Client PUT 直传（Content-Type 严格一致；读 ETag 去引号作 md5）→ complete → 返回 `{ImageID, URL, PreviewURL, TTL}`。PUT 客户端仿 `richmedia.go` 的 `chunkHTTP`（独立实例，防外链超时拖垮默认客户端）。
- 文件头注释留错误码表与协议说明（fork 惯例：协议注释写在实现文件头）。

**`resource.go`**：新增四个 uri 常量（`/v2/media-images`、`/v2/media-images/prepare`、`/v2/media-images/complete`、`/v2/media-images/{image_id}/delete`）。

### 2. Bot 级 `internal/bot`

- `media.go`：`func (b *Bot) UploadMediaImage(ctx context.Context, fileName, contentType string, data []byte) (*dto.MediaImage, error)` —— 前置校验（≤10MB；扩展名与 content_type 白名单一致）后透传 fork 一步式，错误原样返回。
- `card.go`（新文件）：
  ```go
  func (b *Bot) SendGroupCard(ctx context.Context, groupID, msgAnchor string, card dto.CardContent) (string, error)
  func (b *Bot) SendC2CCard(ctx context.Context, userID, msgAnchor string, card dto.CardContent) (string, error)
  ```
  走 `MessageToCreate{MsgType: dto.CardMsg, Card: &dto.Card{Type: "tuwen", Content: card}, MsgID: msgAnchor, MsgSeq: b.nextSeq()}`；锚点语义同现有 Send 系列（空=主动）。客户端侧校验（utf8.RuneCountInString）：title 1-50、description 1-200、url/picURL 须 `https://` 前缀且 ≤1024。
- `tag.go`：`const AtEveryoneTag = "<qqbot-at-everyone />"`，注释写明三重限制（仅群聊/须群管理员/每群每天 1 次，超限由服务端拒绝）。

### 3. 文档

- `docs/development.md`：「Bot 级发送」表加 `SendGroupCard`/`SendC2CCard`/`UploadMediaImage` 三行；「消息能力现状」段补 msg_type=8、@全体限制、素材管理三句。
- 项目根 `AGENTS.md`「消息能力现状」要点同步一句（图文卡片可发；@全体标签可用但 1 次/天/群；素材管理 qqbot-media 已封装）。

### 4. 测试

- fork：按 `openapi/v1/richmedia_test.go` 的 httptest 模式——四个原子接口的请求体/响应解析、一步式全流程（含 ETag 去引号、Content-Type 一致性、prepare→PUT→complete 顺序）、10MB/格式校验分支。
- `internal/bot`：按 `media_test.go` 既有模式——Card 校验（长度边界 50/200、非 https、超 1024）、SendGroupCard/SendC2CCard 组包正确性（msg_type=8、card 字段、锚点）、AtEveryoneTag 常量值。
- 验收：`go build ./... && go vet ./... && go test ./... && gofmt -l .` 全过。

## 任务拆分与依赖

| 卡 | 内容 | 依赖 | 执行者 |
|---|---|---|---|
| 卡1 | `pkg/botgo`：dto 两处 + iface + v1 实现 + uri + fork 测试 | 无 | backend-go |
| 卡2 | `internal/bot`：card.go + media.go 上传封装 + tag.go + 测试；development.md 与 AGENTS.md 文档 | 卡1（契约冻结后可并行编写，但编译依赖卡1 合入） | backend-go |

部署：两卡合入后走标准 `./scripts/build-run.sh`（本设计不含上线步骤，由用户决定何时部署）。

## 风险与备注

- 图文卡片/素材管理为新接口，公域机器人可能仍受异步审核或灰度限制，发送失败路径须保留错误透传（不吞错）。
- `preview_url` 短时效，dto 保留字段但调用方不得持久化（注释注明）。
- @全体成员每群 1 次/天为服务端硬限，本期不做本地记账，调用方按错误码自行降级。
