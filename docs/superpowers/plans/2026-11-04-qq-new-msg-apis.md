# QQ 三条新消息能力接入实施计划（图文卡片 / @全体成员 / 素材管理）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 QQ 官方 2026-10 新增的图文卡片（msg_type=8）、@全体成员标签、素材管理（qqbot-media://）接入本仓库：协议补进 `pkg/botgo` fork，薄封装暴露到 `internal/bot`。

**Architecture:** 依 spec `docs/superpowers/specs/2026-11-03-qq-new-msg-apis-design.md`。协议层完全照抄 fork 既有模式（iface 能力接口组 + v1 实现 + resource.go uri 常量 + dto）；Bot 级照抄 Send 系列（锚点语义、`b.nextSeq()`、`msgID(m)`）；素材校验（10MB/格式）放 fork 一步式单一出处，Bot 级纯透传（对 spec §2「Bot 前置校验」的 DRY 细化：校验只做一处）。

**Tech Stack:** Go（无新依赖）、httptest 假 server 测试模式。

## Global Constraints

- 无新增第三方依赖；不改架构；不改任何插件/指令/agent 工具。
- 提交前四件套全过：`go build ./... && go vet ./... && go test ./... && gofmt -l .`（gofmt 输出须为空）。
- 注释风格照抄周边（中文、协议注释写文件头）；官方 API 事实以 spec「官方 API 事实」节为准，勿再猜。
- 两个任务卡对应 Task 1-4（卡1，`pkg/botgo`）与 Task 5-8（卡2，`internal/bot`+docs）；卡2 依赖卡1 合入。
- 契约签名冻结：见各 Task 的 Interfaces 块，一字不改。

---

## 卡 1：协议层 pkg/botgo

### Task 1: dto——卡片类型 + 素材管理结构

**Files:**
- Modify: `pkg/botgo/dto/message_create.go`
- Create: `pkg/botgo/dto/media_image.go`

**Interfaces (Produces):** `dto.Card`、`dto.CardContent`、`dto.CardMsg`、`MessageToCreate.Card` 字段、`dto.MediaImagePrepareReq/Rsp`、`dto.MediaImageCompleteReq`、`dto.MediaImage`、`dto.MediaImageListRsp`、`dto.MediaImageEntry`。

- [ ] **Step 1: message_create.go 加 CardMsg 与 Card**

在 `MessageType` 常量块（`RichMediaMsg MessageType = 7` 之后）加：

```go
	CardMsg MessageType = 8 // 图文卡片消息（card 字段）
```

在 `MessageToCreate` 结构体 `Media` 字段后加：

```go
	Card            *Card                     `json:"card,omitempty"`           // 图文卡片（MsgType=8）
```

（注意对齐既有字段注释列；该结构体字段带注释，照抄格式。）

文件尾部追加：

```go
// Card 图文卡片（MsgType=8，官方 2026-10-28 新增，群聊/单聊支持）。Type 固定 "tuwen"。
type Card struct {
	Type    string      `json:"type"`
	Content CardContent `json:"content"`
}

// CardContent 图文卡片内容：Title 1-50 字、Description 1-200 字、
// URL/PicURL 须 https 且 ≤1024 字符（客户端侧校验在 internal/bot）。
type CardContent struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	PicURL      string `json:"pic_url"`
}
```

- [ ] **Step 2: 新建 dto/media_image.go**

```go
package dto

// 素材管理（官方 2026-10-21 新增）：图片上传平台换 qqbot-media:// 引用，markdown 消息中
// 引用（![说明 #宽px #高px](qqbot-media://image/v1/<token>)），发送时平台自动转存。
// 限制：单图 ≤10MB；png/jpeg/gif/webp；单机器人上限 100 张；引用 90 天滑动过期。

// MediaImagePrepareReq 申请图片上传（md5/sha1 为文件内容十六进制摘要）。
type MediaImagePrepareReq struct {
	FileName    string `json:"file_name"`    // 含扩展名，如 kline.png
	FileSize    uint64 `json:"file_size"`    // 字节
	ContentType string `json:"content_type"` // image/png|image/jpeg|image/gif|image/webp
	MD5         string `json:"md5"`          // 32 位 hex
	SHA1        string `json:"sha1"`         // 40 位 hex
}

// MediaImagePrepareRsp 上传任务与预签名地址（任务 4 小时有效）。
type MediaImagePrepareRsp struct {
	UploadID     string `json:"upload_id"`
	PresignedURL string `json:"presigned_url"` // HTTP PUT 直传图片二进制
	ExpiresIn    uint32 `json:"expires_in"`    // 预签名地址有效期（秒）
}

// MediaImageCompleteReq 完成上传：MD5 用 PUT 响应头 ETag 去引号（即文件 MD5）。
type MediaImageCompleteReq struct {
	UploadID string `json:"upload_id"`
	MD5      string `json:"md5"`
}

// MediaImage complete 响应 / 一步式上传返回。
type MediaImage struct {
	ImageID    string `json:"image_id"`    // 用于查询与删除
	URL        string `json:"url"`         // markdown 引用：qqbot-media://image/v1/<token>
	TTL        uint32 `json:"ttl"`         // 滑动有效期窗口（秒），默认 90 天
	PreviewURL string `json:"preview_url"` // 临时预览（短时效，勿持久化）
}

// MediaImageListRsp 有效图片列表（上传时间倒序，cursor 翻页，空 next_cursor 即末页）。
type MediaImageListRsp struct {
	Images     []MediaImageEntry `json:"images"`
	NextCursor string            `json:"next_cursor"`
	Total      uint64            `json:"total"`
}

// MediaImageEntry 列表项（ExpiresAt 每次被引用后自动顺延）。
type MediaImageEntry struct {
	ImageID    string `json:"image_id"`
	URL        string `json:"url"`
	FileName   string `json:"file_name"`
	PreviewURL string `json:"preview_url"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  int64  `json:"expires_at"`
}
```

- [ ] **Step 3: 编译 + 提交**

```bash
go build ./... && gofmt -l pkg/botgo/dto
git add pkg/botgo/dto/message_create.go pkg/botgo/dto/media_image.go
git commit -m "botgo: dto 增加图文卡片(msg_type=8)与素材管理(media-images)结构"
```

### Task 2: iface + uri 常量

**Files:**
- Modify: `pkg/botgo/openapi/iface.go`（OpenAPI 接口嵌入列表 + 文件尾新接口组）
- Modify: `pkg/botgo/openapi/v1/resource.go`（uri 常量）

**Interfaces (Produces):** `openapi.MediaImageAPI` 五方法（签名如下，冻结）。

- [ ] **Step 1: iface.go 嵌入列表加一行**

`OpenAPI interface` 的 fork 补充段（`RichMediaUploadAPI` 之后）加：

```go
	MediaImageAPI
```

- [ ] **Step 2: iface.go 文件尾（RichMediaUploadAPI 定义之后）追加接口组**

```go
// MediaImageAPI 素材管理接口（官方 2026-10-21：图片上传平台换 qqbot-media:// 引用，
// markdown 中引用、平台转存，摆脱公网图片 URL 限时拉取问题）。
type MediaImageAPI interface {
	// UploadMediaImage 一步式上传：校验 → prepare → 预签名 PUT 直传 → complete。
	UploadMediaImage(ctx context.Context, fileName, contentType string, data []byte) (*dto.MediaImage, error)
	PrepareMediaImage(ctx context.Context, req dto.MediaImagePrepareReq) (*dto.MediaImagePrepareRsp, error)
	CompleteMediaImage(ctx context.Context, req dto.MediaImageCompleteReq) (*dto.MediaImage, error)
	ListMediaImages(ctx context.Context, cursor string) (*dto.MediaImageListRsp, error)
	DeleteMediaImage(ctx context.Context, imageID string) error
}
```

- [ ] **Step 3: resource.go 加 uri 常量**

在 `groupUploadPartFinishURI` 常量块之后加：

```go
	// --- 素材管理（本地 fork 补充：官方 2026-10-21 新增，markdown 图片平台转存）---
	mediaImagesURI        uri = "/v2/media-images"
	mediaImagePrepareURI  uri = "/v2/media-images/prepare"
	mediaImageCompleteURI uri = "/v2/media-images/complete"
	mediaImageDeleteURI   uri = "/v2/media-images/{image_id}/delete"
```

- [ ] **Step 4: 编译（此时 v1 未实现会报错，先跳过编译只提交不了——实现与编译合并到 Task 3 提交）**

本 Task 不单独提交，与 Task 3 一起提交。

### Task 3: v1 实现 media_image.go

**Files:**
- Create: `pkg/botgo/openapi/v1/media_image.go`

**Interfaces:**
- Consumes: Task 1 的 dto、Task 2 的 uri 常量与 iface 签名。
- Produces: `MediaImageAPI` 的 v1 实现。

- [ ] **Step 1: 写实现（完整文件）**

```go
package v1

// 素材管理（本地 fork 补充，官方 2026-10-21）：图片上传平台换 qqbot-media:// 引用，
// markdown 消息中引用（![说明 #宽px #高px](qqbot-media://image/v1/<token>)），发送时
// 平台自动转存，摆脱公网图片 URL 被 QQ 服务端限时拉取的不稳定问题。
//
//   - 一步式 UploadMediaImage：校验 → 算 md5/sha1 → prepare → 预签名 PUT 直传
//     （Content-Type 必须与 prepare 声明完全一致，否则 COS 403）→ complete。
//   - 原子接口：PrepareMediaImage / CompleteMediaImage / ListMediaImages / DeleteMediaImage。
//
// 限制：单图 ≤10MB；png/jpeg/gif/webp（扩展名与 content_type 须一致）；单机器人上限
// 100 张（超限 prepare/complete 报 40035007，用 List/Delete 腾配额）；上传任务 4 小时
// 有效；引用 90 天滑动过期（每次被消息引用顺延）。
//
// 错误码透传不本地翻译：40035001 格式不支持 / 40035002 超 10MB / 40035003 任务不存在
// 或过期 / 40035006 服务端内部错误 / 40035007 超百张 / 40035008 图不存在 /
// 40035009 引用过期 / 40035010 引用无效。

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"qqbot/pkg/botgo/dto"
)

// maxMediaImageBytes 素材图片官方硬限 10MB。
const maxMediaImageBytes = 10 << 20

// mediaImageHTTP 预签名 PUT 直传客户端（独立实例，防外链慢速拖垮接口默认客户端）。
var mediaImageHTTP = &http.Client{Timeout: 5 * time.Minute}

// mediaImageExts 允许的 content_type → 扩展名（官方要求文件名扩展名与类型一致；
// image/jpeg 的文件名允许 .jpg 或 .jpeg）。
var mediaImageExts = map[string][]string{
	"image/png":  {"png"},
	"image/jpeg": {"jpg", "jpeg"},
	"image/gif":  {"gif"},
	"image/webp": {"webp"},
}

// UploadMediaImage 一步式上传素材图片，返回 qqbot-media:// 引用。
func (o *openAPI) UploadMediaImage(ctx context.Context, fileName, contentType string, data []byte) (*dto.MediaImage, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("图片内容为空")
	}
	if len(data) > maxMediaImageBytes {
		return nil, fmt.Errorf("素材图片超过大小上限 %d MB", maxMediaImageBytes>>20)
	}
	exts, ok := mediaImageExts[contentType]
	if !ok {
		return nil, fmt.Errorf("content_type 仅支持 png/jpeg/gif/webp: %s", contentType)
	}
	lower := strings.ToLower(fileName)
	matched := false
	for _, ext := range exts {
		if strings.HasSuffix(lower, "."+ext) {
			matched = true
			break
		}
	}
	if !matched {
		return nil, fmt.Errorf("文件名扩展名须与 content_type 一致: %s vs %s", fileName, contentType)
	}

	md5Full := md5.Sum(data)
	sha1Full := sha1.Sum(data)
	prep, err := o.PrepareMediaImage(ctx, dto.MediaImagePrepareReq{
		FileName:    fileName,
		FileSize:    uint64(len(data)),
		ContentType: contentType,
		MD5:         hex.EncodeToString(md5Full[:]),
		SHA1:        hex.EncodeToString(sha1Full[:]),
	})
	if err != nil {
		return nil, err
	}
	etag, err := putMediaImage(ctx, prep.PresignedURL, contentType, data)
	if err != nil {
		return nil, fmt.Errorf("素材直传失败: %w", err)
	}
	return o.CompleteMediaImage(ctx, dto.MediaImageCompleteReq{UploadID: prep.UploadID, MD5: etag})
}

// putMediaImage PUT 二进制到预签名地址（无鉴权头，地址自带签名）；Content-Type 必须
// 与 prepare 声明完全一致（否则 COS 403）。返回响应头 ETag 去引号（即文件 MD5）。
func putMediaImage(ctx context.Context, presignedURL, contentType string, data []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, presignedURL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := mediaImageHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateForLog(body, 200))
	}
	etag := strings.Trim(resp.Header.Get("ETag"), `"`)
	if etag == "" {
		return "", fmt.Errorf("直传响应缺少 ETag")
	}
	return etag, nil
}

// PrepareMediaImage 申请上传，拿预签名地址（任务 4 小时内有效）。
func (o *openAPI) PrepareMediaImage(ctx context.Context, req dto.MediaImagePrepareReq) (*dto.MediaImagePrepareRsp, error) {
	rsp, err := o.request(ctx).
		SetBody(req).
		SetResult(dto.MediaImagePrepareRsp{}).
		Post(o.getURL(mediaImagePrepareURI))
	if err != nil {
		if rsp != nil {
			return nil, fmt.Errorf("media prepare 失败: %w（响应: %s）", err, truncateForLog(rsp.Body(), 300))
		}
		return nil, fmt.Errorf("media prepare 失败: %w", err)
	}
	prep := rsp.Result().(*dto.MediaImagePrepareRsp)
	if prep.UploadID == "" || prep.PresignedURL == "" {
		return nil, fmt.Errorf("media prepare 响应缺少 upload_id/presigned_url: %s", truncateForLog(rsp.Body(), 300))
	}
	return prep, nil
}

// CompleteMediaImage 完成上传，拿 qqbot-media:// 引用（服务端与 COS 实际 ETag 比对）。
func (o *openAPI) CompleteMediaImage(ctx context.Context, req dto.MediaImageCompleteReq) (*dto.MediaImage, error) {
	rsp, err := o.request(ctx).
		SetBody(req).
		SetResult(dto.MediaImage{}).
		Post(o.getURL(mediaImageCompleteURI))
	if err != nil {
		if rsp != nil {
			return nil, fmt.Errorf("media complete 失败: %w（响应: %s）", err, truncateForLog(rsp.Body(), 300))
		}
		return nil, fmt.Errorf("media complete 失败: %w", err)
	}
	img := rsp.Result().(*dto.MediaImage)
	if img.URL == "" {
		return nil, fmt.Errorf("media complete 响应缺少 url: %s", truncateForLog(rsp.Body(), 300))
	}
	return img, nil
}

// ListMediaImages 查询有效图片（上传时间倒序；cursor 空串为首页，翻页传上页 next_cursor）。
func (o *openAPI) ListMediaImages(ctx context.Context, cursor string) (*dto.MediaImageListRsp, error) {
	req := o.request(ctx).SetResult(dto.MediaImageListRsp{})
	if cursor != "" {
		req = req.SetQueryParam("cursor", cursor)
	}
	rsp, err := req.Get(o.getURL(mediaImagesURI))
	if err != nil {
		return nil, fmt.Errorf("media list 失败: %w", err)
	}
	return rsp.Result().(*dto.MediaImageListRsp), nil
}

// DeleteMediaImage 删除图片释放配额（引用立即失效；已发消息不受影响——发送时已转存）。
func (o *openAPI) DeleteMediaImage(ctx context.Context, imageID string) error {
	_, err := o.request(ctx).
		SetPathParam("image_id", imageID).
		Delete(o.getURL(mediaImageDeleteURI))
	return err
}
```

- [ ] **Step 2: 编译 + vet + 提交（含 Task 2 的两个文件）**

```bash
go build ./... && go vet ./pkg/botgo/... && gofmt -l pkg/botgo
git add pkg/botgo/openapi/iface.go pkg/botgo/openapi/v1/resource.go pkg/botgo/openapi/v1/media_image.go
git commit -m "botgo: 素材管理 media-images 五接口实现（prepare/PUT直传/complete/list/delete）"
```

### Task 4: fork 测试 media_image_test.go

**Files:**
- Create: `pkg/botgo/openapi/v1/media_image_test.go`

**Interfaces:**
- Consumes: Task 3 全部方法；既有 `testAPI(t, srv)` 助手（`richmedia_test.go`）。

- [ ] **Step 1: 写测试（完整文件）**

```go
package v1

// 素材管理协议测试：经真实 openAPI 实例打到 httptest 假 server（覆盖
// constant.APIDomain 指向本机），复用 richmedia_test.go 的 testAPI 助手。

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"qqbot/pkg/botgo/dto"
)

// TestUploadMediaImage 一步式全流程：prepare 校验请求体（文件名/大小/类型/摘要）→
// 预签名 PUT（校验方法、Content-Type、字节一致；回 ETag）→ complete（校验 upload_id
// 与去引号的 ETag）→ 返回 qqbot-media:// 引用。
func TestUploadMediaImage(t *testing.T) {
	data := []byte("PNGDATA-fake-bytes")
	var mu sync.Mutex
	putBody := []byte(nil)
	putCT := ""

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/v2/media-images/prepare"):
			var req dto.MediaImagePrepareReq
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("prepare 请求体非 JSON: %v", err)
			}
			md5Full := fmt.Sprintf("%x", md5.Sum(data))
			sha1Full := fmt.Sprintf("%x", sha1.Sum(data))
			if req.FileName != "k.png" || req.FileSize != uint64(len(data)) ||
				req.ContentType != "image/png" || req.MD5 != md5Full || req.SHA1 != sha1Full {
				t.Errorf("prepare 字段错: %+v", req)
			}
			fmt.Fprintf(w, `{"upload_id":"UP1","presigned_url":%q,"expires_in":3600}`, srv.URL+"/put")
		case r.URL.Path == "/put":
			if r.Method != http.MethodPut {
				t.Errorf("直传应 PUT, got %s", r.Method)
			}
			mu.Lock()
			putBody = append([]byte(nil), body...)
			putCT = r.Header.Get("Content-Type")
			mu.Unlock()
			w.Header().Set("ETag", `"`+fmt.Sprintf("%x", md5.Sum(data))+`"`)
		case strings.HasSuffix(r.URL.Path, "/v2/media-images/complete"):
			var req dto.MediaImageCompleteReq
			_ = json.Unmarshal(body, &req)
			if req.UploadID != "UP1" || req.MD5 != fmt.Sprintf("%x", md5.Sum(data)) {
				t.Errorf("complete 字段错（须为去引号 ETag）: %+v", req)
			}
			fmt.Fprint(w, `{"image_id":"IMG1","url":"qqbot-media://image/v1/TOKEN","ttl":7776000,"preview_url":"https://pv"}`)
		default:
			t.Errorf("意外请求: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	api := testAPI(t, srv)
	img, err := api.UploadMediaImage(context.Background(), "k.png", "image/png", data)
	if err != nil {
		t.Fatalf("UploadMediaImage: %v", err)
	}
	if img.ImageID != "IMG1" || img.URL != "qqbot-media://image/v1/TOKEN" || img.TTL != 7776000 {
		t.Errorf("MediaImage=%+v", img)
	}
	if string(putBody) != string(data) || putCT != "image/png" {
		t.Errorf("直传字节/Content-Type 错: ct=%q len=%d", putCT, len(putBody))
	}
}

// TestUploadMediaImageValidation 客户端侧校验：空内容、超 10MB、非法 content_type、
// 扩展名不一致——都不应发出任何 HTTP 请求。
func TestUploadMediaImageValidation(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
	}))
	t.Cleanup(srv.Close)

	api := testAPI(t, srv)
	oversize := make([]byte, maxMediaImageBytes+1)
	cases := []struct {
		name        string
		fileName    string
		contentType string
		data        []byte
	}{
		{"空内容", "a.png", "image/png", nil},
		{"超10MB", "a.png", "image/png", oversize},
		{"非法类型", "a.png", "image/bmp", []byte("x")},
		{"扩展名不符", "a.bmp", "image/png", []byte("x")},
		{"无扩展名", "png", "image/png", []byte("x")},
	}
	for _, c := range cases {
		if _, err := api.UploadMediaImage(context.Background(), c.fileName, c.contentType, c.data); err == nil {
			t.Errorf("%s: 应报错", c.name)
		}
	}
	if calls != 0 {
		t.Errorf("校验失败不应发请求, got %d 次", calls)
	}
}

// jpeg 扩展名允许 .jpg 与 .jpeg 两种，均应走通全流程。
func TestUploadMediaImageJPEGExt(t *testing.T) {
	data := []byte("jpeg-bytes")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/v2/media-images/prepare"):
			fmt.Fprintf(w, `{"upload_id":"U","presigned_url":%q,"expires_in":60}`, srv.URL+"/p")
		case r.URL.Path == "/put" || r.URL.Path == "/p":
			w.Header().Set("ETag", `"`+fmt.Sprintf("%x", md5.Sum(data))+`"`)
		case strings.HasSuffix(r.URL.Path, "/v2/media-images/complete"):
			fmt.Fprint(w, `{"image_id":"J1","url":"qqbot-media://image/v1/J","ttl":7776000}`)
		default:
			t.Errorf("意外请求: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	api := testAPI(t, srv)
	for _, name := range []string{"a.jpg", "a.jpeg"} {
		if _, err := api.UploadMediaImage(context.Background(), name, "image/jpeg", data); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestPrepareMediaImageBadRsp prepare 响应缺 upload_id 应报错。
func TestPrepareMediaImageBadRsp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(srv.Close)

	api := testAPI(t, srv)
	if _, err := api.PrepareMediaImage(context.Background(), dto.MediaImagePrepareReq{}); err == nil ||
		!strings.Contains(err.Error(), "upload_id") {
		t.Fatalf("缺 upload_id 应报错: %v", err)
	}
}

// TestListMediaImages cursor 透传与响应解析。
func TestListMediaImages(t *testing.T) {
	var gotCursor string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		gotCursor = r.URL.Query().Get("cursor")
		fmt.Fprint(w, `{"images":[{"image_id":"I1","url":"qqbot-media://image/v1/A","file_name":"a.png","created_at":1,"expires_at":2}],"next_cursor":"","total":1}`)
	}))
	t.Cleanup(srv.Close)

	api := testAPI(t, srv)
	rsp, err := api.ListMediaImages(context.Background(), "CUR1")
	if err != nil {
		t.Fatal(err)
	}
	if gotCursor != "CUR1" {
		t.Errorf("cursor=%q", gotCursor)
	}
	if rsp.Total != 1 || len(rsp.Images) != 1 || rsp.Images[0].FileName != "a.png" {
		t.Errorf("ListRsp=%+v", rsp)
	}
}

// TestDeleteMediaImage 走 DELETE /v2/media-images/{image_id}/delete。
func TestDeleteMediaImage(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		gotMethod, gotPath = r.Method, r.URL.Path
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(srv.Close)

	api := testAPI(t, srv)
	if err := api.DeleteMediaImage(context.Background(), "IMG9"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/v2/media-images/IMG9/delete" {
		t.Errorf("delete: %s %s", gotMethod, gotPath)
	}
}
```

- [ ] **Step 2: 跑测试**

```bash
go test ./pkg/botgo/openapi/v1/ -run 'MediaImage' -v
```

Expected: 全 PASS。

- [ ] **Step 3: 提交**

```bash
git add pkg/botgo/openapi/v1/media_image_test.go
git commit -m "botgo: 素材管理接口测试（一步式全流程/校验/list/delete）"
```

---

## 卡 2：Bot 级封装 + 文档（依赖卡 1 合入）

### Task 5: 图文卡片发送 card.go

**Files:**
- Create: `internal/bot/card.go`
- Create: `internal/bot/card_test.go`

**Interfaces:**
- Consumes: 卡1 的 `dto.CardMsg`/`dto.Card`/`dto.CardContent`；既有 `b.api`/`ErrNotConnected`/`b.nextSeq()`/`msgID(m)`、`b.api.PostGroupMessage/PostC2CMessage`。
- Produces: `(*Bot).SendGroupCard` / `(*Bot).SendC2CCard`。

- [ ] **Step 1: 写实现（完整文件）**

```go
package bot

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"qqbot/pkg/botgo/dto"
)

// 图文卡片（官方 2026-10-28 新增，群聊/单聊支持）：msg_type=8 + card（type="tuwen"）。
// 官方约束：title 1-50 字、description 1-200 字、url/pic_url 须 https 且 ≤1024 字符；
// 这里客户端侧提前拦（错误更友好），服务端仍会兜底校验（304061 口径）。

const (
	cardTitleMax = 50
	cardDescMax  = 200
	cardURLMax   = 1024
	tuwenCard    = "tuwen"
)

// validateCard 客户端侧校验图文卡片内容（长度按字符数计）。
func validateCard(c dto.CardContent) error {
	if n := utf8.RuneCountInString(c.Title); n < 1 || n > cardTitleMax {
		return fmt.Errorf("卡片标题须 1-%d 字（当前 %d）", cardTitleMax, n)
	}
	if n := utf8.RuneCountInString(c.Description); n < 1 || n > cardDescMax {
		return fmt.Errorf("卡片描述须 1-%d 字（当前 %d）", cardDescMax, n)
	}
	if !strings.HasPrefix(c.URL, "https://") {
		return fmt.Errorf("卡片 url 须为 https 链接: %q", c.URL)
	}
	if len(c.URL) > cardURLMax {
		return fmt.Errorf("卡片 url 超过 %d 字符", cardURLMax)
	}
	if !strings.HasPrefix(c.PicURL, "https://") {
		return fmt.Errorf("卡片 pic_url 须为 https 链接: %q", c.PicURL)
	}
	if len(c.PicURL) > cardURLMax {
		return fmt.Errorf("卡片 pic_url 超过 %d 字符", cardURLMax)
	}
	return nil
}

// sendCard 发图文卡片公共路径（group=true 走群）。
func (b *Bot) sendCard(ctx context.Context, group bool, openID, msgAnchor string, card dto.CardContent) (string, error) {
	if b.api == nil {
		return "", ErrNotConnected
	}
	if err := validateCard(card); err != nil {
		return "", err
	}
	msg := &dto.MessageToCreate{
		MsgType: dto.CardMsg,
		Card:    &dto.Card{Type: tuwenCard, Content: card},
		MsgID:   msgAnchor,
		MsgSeq:  b.nextSeq(),
	}
	if group {
		m, err := b.api.PostGroupMessage(ctx, openID, msg)
		return msgID(m), err
	}
	m, err := b.api.PostC2CMessage(ctx, openID, msg)
	return msgID(m), err
}

// SendGroupCard 发群图文卡片：msgAnchor 空=主动（需权限，受频控）；非空=被动回复。
func (b *Bot) SendGroupCard(ctx context.Context, groupID, msgAnchor string, card dto.CardContent) (string, error) {
	return b.sendCard(ctx, true, groupID, msgAnchor, card)
}

// SendC2CCard 发私聊图文卡片（同 SendGroupCard 语义）。
func (b *Bot) SendC2CCard(ctx context.Context, userID, msgAnchor string, card dto.CardContent) (string, error) {
	return b.sendCard(ctx, false, userID, msgAnchor, card)
}
```

- [ ] **Step 2: 写测试 card_test.go（完整文件）**

```go
package bot

// 图文卡片（msg_type=8）发送测试：经 testBot 打到 httptest 假 server，
// 校验组包（msg_type/card/锚点/seq）与客户端侧校验。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qqbot/pkg/botgo/dto"
)

func validCard() dto.CardContent {
	return dto.CardContent{
		Title:       "标题",
		Description: "描述",
		URL:         "https://example.com/a",
		PicURL:      "https://example.com/p.jpg",
	}
}

// cardSrv 记录 messages 端点收到的请求体。
func cardSrv(t *testing.T) (*httptest.Server, *dto.MessageToCreate) {
	t.Helper()
	var got dto.MessageToCreate
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("messages 请求体非 JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"ROBOT1.0_CARD","timestamp":"2026-11-04T10:00:00+08:00"}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestSendGroupCard(t *testing.T) {
	srv, got := cardSrv(t)
	b := testBot(t, srv)
	id, err := b.SendGroupCard(context.Background(), "G", "ANCHOR", validCard())
	if err != nil {
		t.Fatal(err)
	}
	if id != "ROBOT1.0_CARD" {
		t.Errorf("id=%q", id)
	}
	if got.MsgType != dto.CardMsg || got.Card == nil || got.Card.Type != "tuwen" {
		t.Errorf("组包错: msg_type=%v card=%+v", got.MsgType, got.Card)
	}
	if got.Card.Content.Title != "标题" || got.Card.Content.PicURL != "https://example.com/p.jpg" {
		t.Errorf("card content 错: %+v", got.Card.Content)
	}
	if got.MsgID != "ANCHOR" || got.MsgSeq == 0 {
		t.Errorf("锚点/序号错: msg_id=%q seq=%d", got.MsgID, got.MsgSeq)
	}
}

func TestSendC2CCard(t *testing.T) {
	srv, got := cardSrv(t)
	b := testBot(t, srv)
	if _, err := b.SendC2CCard(context.Background(), "U", "", validCard()); err != nil {
		t.Fatal(err)
	}
	if got.MsgID != "" || got.Card == nil {
		t.Errorf("主动消息应无锚点: %+v", got)
	}
}

func TestValidateCard(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*dto.CardContent)
	}{
		{"标题空", func(c *dto.CardContent) { c.Title = "" }},
		{"标题51字", func(c *dto.CardContent) { c.Title = strings.Repeat("字", 51) }},
		{"描述201字", func(c *dto.CardContent) { c.Description = strings.Repeat("字", 201) }},
		{"url非https", func(c *dto.CardContent) { c.URL = "http://example.com" }},
		{"pic_url超长", func(c *dto.CardContent) { c.PicURL = "https://" + strings.Repeat("a", 1100) }},
	}
	for _, tc := range cases {
		c := validCard()
		tc.mut(&c)
		if err := validateCard(c); err == nil {
			t.Errorf("%s: 应报错", tc.name)
		}
	}
	if err := validateCard(validCard()); err != nil {
		t.Errorf("合法卡片不应报错: %v", err)
	}
	// 边界值正好通过：50 字标题、200 字描述、1024 字符 url。
	c := validCard()
	c.Title = strings.Repeat("字", 50)
	c.Description = strings.Repeat("字", 200)
	c.URL = "https://" + strings.Repeat("a", 1024-8)
	if err := validateCard(c); err != nil {
		t.Errorf("边界值不应报错: %v", err)
	}
}

// 校验失败不应发出 HTTP 请求。
func TestSendGroupCardValidationNoCall(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++ }))
	t.Cleanup(srv.Close)
	b := testBot(t, srv)
	c := validCard()
	c.URL = "http://x"
	if _, err := b.SendGroupCard(context.Background(), "G", "", c); err == nil {
		t.Fatal("应报错")
	}
	if calls != 0 {
		t.Errorf("校验失败不应发请求, got %d", calls)
	}
}
```

- [ ] **Step 3: 跑测试**

```bash
go test ./internal/bot/ -run 'Card' -v
```

Expected: 全 PASS。

- [ ] **Step 4: 提交**

```bash
git add internal/bot/card.go internal/bot/card_test.go
git commit -m "bot: SendGroupCard/SendC2CCard 图文卡片发送（msg_type=8，客户端侧校验）"
```

### Task 6: 素材上传 Bot 级封装

**Files:**
- Modify: `internal/bot/media.go`（文件尾追加）
- Modify: `internal/bot/media_test.go`（文件尾追加测试）

**Interfaces:**
- Consumes: 卡1 的 `MediaImageAPI.UploadMediaImage`（经 `b.openapi()`）。
- Produces: `(*Bot).UploadMediaImage`。

- [ ] **Step 1: media.go 追加**

```go
// UploadMediaImage 上传素材图片到平台，返回 qqbot-media:// 引用：markdown 消息中用
// ![说明 #宽px #高px](url) 引用（与公网图片语法一致，可混用），发送时平台自动转存。
// 90 天滑动有效期（每次被引用顺延）；校验（≤10MB、png/jpeg/gif/webp、扩展名与
// content_type 一致）在 fork 一步式里做，这里纯透传。
func (b *Bot) UploadMediaImage(ctx context.Context, fileName, contentType string, data []byte) (*dto.MediaImage, error) {
	api, err := b.openapi()
	if err != nil {
		return nil, err
	}
	return api.UploadMediaImage(ctx, fileName, contentType, data)
}
```

- [ ] **Step 2: media_test.go 追加测试**

```go
// TestUploadMediaImage Bot 级透传链路：prepare→PUT→complete 走通，返回引用；
// 空内容在 fork 校验被拦（未发 HTTP）。
func TestUploadMediaImage(t *testing.T) {
	calls := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/v2/media-images/prepare"):
			fmt.Fprintf(w, `{"upload_id":"U1","presigned_url":%q,"expires_in":600}`, srv.URL+"/put")
		case r.URL.Path == "/put":
			w.Header().Set("ETag", `"d41d8cd98f00b204e9800998ecf8427e"`)
		case strings.HasSuffix(r.URL.Path, "/v2/media-images/complete"):
			fmt.Fprint(w, `{"image_id":"I1","url":"qqbot-media://image/v1/T","ttl":7776000,"preview_url":"https://p"}`)
		default:
			t.Errorf("意外请求: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	b := testBot(t, srv)
	// 空内容：fork 校验拦截，未发任何请求。
	if _, err := b.UploadMediaImage(context.Background(), "a.png", "image/png", []byte{}); err == nil ||
		!strings.Contains(err.Error(), "图片内容为空") {
		t.Fatalf("空内容应被拦: %v", err)
	}
	if calls != 0 {
		t.Fatalf("空内容不应发请求, got %d 次", calls)
	}
	img, err := b.UploadMediaImage(context.Background(), "a.png", "image/png", []byte("x"))
	if err != nil {
		t.Fatalf("UploadMediaImage: %v", err)
	}
	if img.URL != "qqbot-media://image/v1/T" || img.ImageID != "I1" {
		t.Errorf("MediaImage=%+v", img)
	}
}
```

- [ ] **Step 3: 跑测试**

```bash
go test ./internal/bot/ -run 'UploadMediaImage' -v
```

Expected: PASS。

- [ ] **Step 4: 提交**

```bash
git add internal/bot/media.go internal/bot/media_test.go
git commit -m "bot: UploadMediaImage 素材上传透传（qqbot-media:// 引用）"
```

### Task 7: @全体成员标签常量

**Files:**
- Modify: `internal/bot/tag.go`（文件尾追加）
- Modify: `internal/bot/menu_test.go` 或新建 `internal/bot/tag_test.go`

**Interfaces (Produces):** `bot.AtEveryoneTag` 常量。

- [ ] **Step 1: tag.go 追加**

```go
// AtEveryoneTag 官方 @全体成员内嵌标签（2026-10-26 新增）：拼进群聊文本（MsgType=0）
// content 或 markdown（MsgType=2）content 开头即可 @全体成员。三重硬限：仅群聊场景；
// 机器人须为该群管理员；每群每天仅可发 1 次（超限/无权限整条消息被官方拒绝，调用方
// 须准备纯文本降级；单聊不支持）。
const AtEveryoneTag = "<qqbot-at-everyone />"
```

- [ ] **Step 2: 新建 tag_test.go**

```go
package bot

import "testing"

// 标签字符串必须与官方文档逐字符一致（含自闭合斜杠前空格）。
func TestAtEveryoneTag(t *testing.T) {
	if AtEveryoneTag != "<qqbot-at-everyone />" {
		t.Errorf("AtEveryoneTag=%q", AtEveryoneTag)
	}
}
```

- [ ] **Step 3: 跑测试 + 提交**

```bash
go test ./internal/bot/ -run 'AtEveryoneTag' -v
git add internal/bot/tag.go internal/bot/tag_test.go
git commit -m "bot: AtEveryoneTag @全体成员内嵌标签常量（群管/每群每日1次）"
```

### Task 8: 文档更新

**Files:**
- Modify: `docs/development.md`（「Bot 级发送」表格 + 消息能力现状段）
- Modify: `AGENTS.md`（「消息能力现状」要点）

- [ ] **Step 1: development.md**

「Bot 级发送（internal/bot）」的文本表（`SendChannelText`/`SendGroupText` 所在表）按原格式追加一行：

```markdown
| `b.SendGroupCard(ctx, groupID, msgAnchor, card)` / `b.SendC2CCard(ctx, userID, msgAnchor, card)` | 发**图文卡片**（MsgType=8，`dto.CardContent{Title≤50字, Description≤200字, URL, PicURL}` 须 https）；锚点语义同文本 |
```

 markdown 表（如有单独 markdown 表）不需要改。在「Bot 级发送」节合适位置（富媒体段落附近）追加小节：

```markdown
**素材管理（qqbot-media）**：`b.UploadMediaImage(ctx, fileName, contentType, data)` 上传
图片到平台拿 `qqbot-media://` 引用（≤10MB，png/jpeg/gif/webp，单机器人 100 张，引用 90 天
滑动过期），markdown 中 `![说明 #宽px #高px](qqbot-media://...)` 引用、平台转存，解决公网
图片 URL 被 QQ 限时拉取不稳的问题。管理接口（List/Delete）在 `pkg/botgo`（MediaImageAPI）。

**@全体成员**：群文本/markdown content 拼入 `bot.AtEveryoneTag`（`<qqbot-at-everyone />`）
即 @全体；限群聊、机器人须群管理员、每群每天 1 次，超限整条被拒，须备纯文本降级。
```

（执行者先读该节现有格式，标题层级与表格列数照抄周边；上面文案可微调对齐。）

- [ ] **Step 2: AGENTS.md「消息能力现状」**

该段末尾追加一句（照抄该节要点式风格）：

```markdown
- 2026-10 官方新增已封装：图文卡片（`SendGroupCard/SendC2CCard`，msg_type=8）、素材管理
  （`UploadMediaImage`→`qqbot-media://` 引用，md 图片平台转存）、@全体标签
  （`bot.AtEveryoneTag`，群管+每群每日 1 次）。
```

- [ ] **Step 3: 提交**

```bash
git add docs/development.md AGENTS.md
git commit -m "docs: 图文卡片/素材管理/@全体标签接入说明"
```

### Task 9: 收尾四件套

- [ ] **Step 1: 全量验证**

```bash
go build ./... && go vet ./... && go test ./... && gofmt -l .
```

Expected: 四项全过（gofmt 无输出）。失败则修到过再汇报，不许带病交付。

- [ ] **Step 2: 汇报**

改了哪些文件、测试结果、遗留疑问（如官方灰度/审核风险实测项）。
