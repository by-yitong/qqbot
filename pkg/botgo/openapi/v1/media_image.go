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
