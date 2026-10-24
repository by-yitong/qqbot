package v1

// 富媒体上传（本地 fork 补充，官方 2026 群/私聊富媒体接口）。
//
//   - URL 模式：UploadGroupRichMedia / UploadUserRichMedia —— POST /v2/{groups|users}/
//     {openid}/files，给公网 url 由 QQ 服务端自行下载（图 20M/视频 30M/语音 20M 软限）。
//     官方对通用文件（file_type=4）不拉 URL（实测 40093007），须走分片直传。
//   - 分片直传：UploadGroupFileChunked / UploadUserFileChunked —— 官方 chunked-upload
//     流程：upload_prepare（带 md5/sha1/md5_10m 校验值）→ 按预签名地址逐片 PUT（无
//     鉴权，地址自带签名）→ upload_part_finish 通知 → /files 传 upload_id 合并拿
//     file_info。file_type=4 硬限 200MB；无格式要求。
//
// 返回的 FileInfo 是喂回 MsgType=7 富媒体消息（PostGroupMessage/PostC2CMessage 的
// media.file_info）的不透明串。srv_send_msg 一律 false——只上传拿 file_info，
// 消息由调用方组发（可带 msg_id 变被动回复）。

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"qqbot/pkg/botgo/dto"
)

// maxChunkedFileBytes 分片直传官方硬限（file 200MB）。
const maxChunkedFileBytes = 200 << 20

// md5PrefixLen upload_prepare 要求的 md5_10m：文件前 10002432 字节（约 10MB）的 MD5。
const md5PrefixLen = 10002432

// chunkHTTP 分片 PUT 预签名地址的客户端（单片可能 10MB 级，不能沿用接口默认超时）。
var chunkHTTP = &http.Client{Timeout: 10 * time.Minute}

// mediaEndpoints 一个上传目标（群/私聊）的三个分片上传端点及 openid 占位名——
// 既有 files 端点占位是 {group_id}/{user_id}，fork 新增的 prepare/finish 是 {openid}，
// 显式记下来避免猜测。
type mediaEndpoints struct {
	prepare, finish, merge                uri
	prepareParam, finishParam, mergeParam string
}

var groupMediaEndpoints = mediaEndpoints{
	prepare: groupUploadPrepareURI, prepareParam: "openid",
	finish: groupUploadPartFinishURI, finishParam: "openid",
	merge: groupRichMediaURI, mergeParam: "group_id",
}

var userMediaEndpoints = mediaEndpoints{
	prepare: userUploadPrepareURI, prepareParam: "openid",
	finish: userUploadPartFinishURI, finishParam: "openid",
	merge: c2cRichMediaURI, mergeParam: "user_id",
}

// UploadGroupRichMedia 群富媒体 URL 模式上传：QQ 自行下载 fileURL。
// fileType: 1图片(png/jpg)/2视频(mp4)/3语音(silk)/4文件（file 官方不拉 URL，走分片直传）。
func (o *openAPI) UploadGroupRichMedia(ctx context.Context, groupID string, fileType int, fileURL string) (*dto.RichMediaFile, error) {
	return o.uploadRichMediaURL(ctx, groupRichMediaURI, "group_id", groupID, fileType, fileURL)
}

// UploadUserRichMedia 私聊富媒体 URL 模式上传（同 UploadGroupRichMedia 语义）。
func (o *openAPI) UploadUserRichMedia(ctx context.Context, userID string, fileType int, fileURL string) (*dto.RichMediaFile, error) {
	return o.uploadRichMediaURL(ctx, c2cRichMediaURI, "user_id", userID, fileType, fileURL)
}

func (o *openAPI) uploadRichMediaURL(ctx context.Context, endpoint uri, openIDParam, openID string, fileType int, fileURL string) (*dto.RichMediaFile, error) {
	rsp, err := o.request(ctx).
		SetPathParam(openIDParam, openID).
		SetBody(dto.RichMediaURLReq{FileType: fileType, URL: fileURL, SrvSendMsg: false}).
		SetResult(dto.RichMediaFile{}).
		Post(o.getURL(endpoint))
	if err != nil {
		return nil, err
	}
	f := rsp.Result().(*dto.RichMediaFile)
	if f.FileInfo == "" {
		// 兜底：有些实现只返回 file_uuid，用它当 file_info 试。
		if f.FileUUID != "" {
			f.FileInfo = f.FileUUID
		} else {
			return nil, fmt.Errorf("上传响应缺少 file_info: %s", truncateForLog(rsp.Body(), 300))
		}
	}
	return f, nil
}

// UploadGroupFileChunked 群分片直传本地字节，返回可发送的 file_info。
// 适用于通用文件（file_type=4，唯一可用路径），也可兜底图片/视频。
func (o *openAPI) UploadGroupFileChunked(ctx context.Context, groupID string, fileType int, filename string, data []byte) (*dto.RichMediaFile, error) {
	return o.uploadFileChunked(ctx, groupMediaEndpoints, groupID, fileType, filename, data)
}

// UploadUserFileChunked 私聊分片直传（同 UploadGroupFileChunked 语义）。
func (o *openAPI) UploadUserFileChunked(ctx context.Context, userID string, fileType int, filename string, data []byte) (*dto.RichMediaFile, error) {
	return o.uploadFileChunked(ctx, userMediaEndpoints, userID, fileType, filename, data)
}

// uploadFileChunked 官方分片上传流程。
func (o *openAPI) uploadFileChunked(ctx context.Context, ep mediaEndpoints, openID string, fileType int, filename string, data []byte) (*dto.RichMediaFile, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("文件内容为空")
	}
	if len(data) > maxChunkedFileBytes {
		return nil, fmt.Errorf("超过大小上限 %d MB", maxChunkedFileBytes>>20)
	}
	if filename == "" {
		filename = "file.bin"
	}

	// 校验值：全量 md5/sha1 + 前 ~10MB 的 md5。
	md5Full := md5.Sum(data)
	sha1Full := sha1.Sum(data)
	prefix := data
	if len(prefix) > md5PrefixLen {
		prefix = prefix[:md5PrefixLen]
	}
	md5Head := md5.Sum(prefix)

	// 1. 申请上传任务，拿分片列表。
	rsp, err := o.request(ctx).
		SetPathParam(ep.prepareParam, openID).
		SetBody(dto.UploadPrepareReq{
			FileType: fileType,
			FileName: filename,
			FileSize: strconv.Itoa(len(data)),
			MD5:      hex.EncodeToString(md5Full[:]),
			SHA1:     hex.EncodeToString(sha1Full[:]),
			MD5TenM:  hex.EncodeToString(md5Head[:]),
		}).
		SetResult(dto.UploadPrepareResult{}).
		Post(o.getURL(ep.prepare))
	if err != nil {
		return nil, fmt.Errorf("upload_prepare 失败: %w", err)
	}
	prep := rsp.Result().(*dto.UploadPrepareResult)
	if prep.UploadID == "" {
		return nil, fmt.Errorf("upload_prepare 响应缺少 upload_id: %s", truncateForLog(rsp.Body(), 300))
	}
	if len(prep.Parts) == 0 {
		return nil, fmt.Errorf("upload_prepare 未返回分片: %s", truncateForLog(rsp.Body(), 300))
	}

	// 2+3. 顺序 PUT 每个分片并通知完成（upload_config.concurrency 默认 1）。
	offset := 0
	for _, part := range prep.Parts {
		if part.PresignedURL == "" || part.BlockSize <= 0 {
			return nil, fmt.Errorf("分片信息异常: index=%d", part.Index)
		}
		end := offset + part.BlockSize
		if end > len(data) {
			end = len(data)
		}
		chunk := data[offset:end]
		if len(chunk) == 0 {
			return nil, fmt.Errorf("分片 %d 越界（offset=%d size=%d 总长=%d）", part.Index, offset, part.BlockSize, len(data))
		}
		if err := putChunk(ctx, part.PresignedURL, chunk); err != nil {
			return nil, fmt.Errorf("分片 %d 上传失败: %w", part.Index, err)
		}
		if _, err := o.request(ctx).
			SetPathParam(ep.finishParam, openID).
			SetBody(dto.UploadPartFinishReq{
				UploadID:  prep.UploadID,
				PartIndex: part.Index,
				BlockSize: strconv.Itoa(len(chunk)),
				MD5:       fmt.Sprintf("%x", md5.Sum(chunk)),
			}).
			Post(o.getURL(ep.finish)); err != nil {
			return nil, fmt.Errorf("分片 %d 完成通知失败: %w", part.Index, err)
		}
		offset = end
	}
	if offset != len(data) {
		return nil, fmt.Errorf("分片总大小 %d 与文件 %d 不符", offset, len(data))
	}

	// 4. 合并拿 file_info（upload_id 模式，url 可空）。
	rsp, err = o.request(ctx).
		SetPathParam(ep.mergeParam, openID).
		SetBody(dto.MergeUploadReq{
			FileType:   fileType,
			SrvSendMsg: false,
			FileName:   filename,
			UploadID:   prep.UploadID,
		}).
		SetResult(dto.RichMediaFile{}).
		Post(o.getURL(ep.merge))
	if err != nil {
		return nil, fmt.Errorf("分片合并失败: %w", err)
	}
	f := rsp.Result().(*dto.RichMediaFile)
	if f.FileInfo == "" {
		if f.FileUUID != "" {
			f.FileInfo = f.FileUUID
		} else {
			return nil, fmt.Errorf("合并响应缺少 file_info: %s", truncateForLog(rsp.Body(), 300))
		}
	}
	return f, nil
}

// putChunk PUT 一个分片到预签名地址（无鉴权头，地址自带签名）。
func putChunk(ctx context.Context, presignedURL string, part []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, presignedURL, bytes.NewReader(part))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := chunkHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateForLog(body, 200))
	}
	return nil
}

// truncateForLog 截断字节串（便于错误日志）。
func truncateForLog(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n])
}
