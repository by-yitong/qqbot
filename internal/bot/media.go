package bot

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/tencent-connect/botgo/dto"
	"github.com/tidwall/gjson"
	"golang.org/x/oauth2"

	"qqbot/internal/audio"
)

// ErrChannelImageUnsupported 频道发图 v1 不支持（仅群/私聊）。
var ErrChannelImageUnsupported = fmt.Errorf("频道发图暂不支持（仅群/私聊）")

// 腾讯富媒体上传端点（公域）。botgo v0.2.1 未封装上传，这里自行 POST。
// 用 var 便于测试覆盖。
var qqAPIBase = "https://api.sgroup.qq.com"

// richMediaMsg 是 MsgType=7 富媒体发送消息（自定义结构，实现 dto.APIMessage）。
// 不用 dto.MessageToCreate：其 Media.FileInfo 是 []byte，json.Marshal 会再 base64 编码一次，
// 而 QQ 上传接口返回的 file_info 是字符串——双重编码后 QQ 报 40034032「请求参数file_info无效」。
type richMediaMsg struct {
	MsgType int            `json:"msg_type"`         // 7=富媒体
	MsgID   string         `json:"msg_id,omitempty"` // 被动回复锚点（空=主动）
	MsgSeq  uint32         `json:"msg_seq,omitempty"`
	Media   *richMediaInfo `json:"media"`
}

// richMediaInfo 的 FileInfo 是 string：file_info 原样透传（不透明串，勿编码）。
type richMediaInfo struct {
	FileInfo string `json:"file_info"`
}

// GetEventID 实现 dto.APIMessage。
func (m richMediaMsg) GetEventID() string { return m.MsgID }

// GetSendType 实现 dto.APIMessage。返回 Text 使 botgo 路由到 /messages 端点
// （MsgType=7 的发送走 messages，不是 files；MessageToCreate 也是这么返回的）。
func (m richMediaMsg) GetSendType() dto.SendType { return dto.Text }

// sendRichMedia 上传并发送一条富媒体消息（MsgType=7）的公共路径。
// group=true 走群、false 走 C2C；fileType: 1图片/2视频/3语音/4文件。
func (b *Bot) sendRichMedia(ctx context.Context, group bool, openID, msgAnchor string, fileType int, fileURL string) (string, error) {
	fileInfo, err := b.uploadRichMedia(ctx, group, openID, fileType, fileURL)
	if err != nil {
		return "", err
	}
	return b.postRichMediaMsg(ctx, group, openID, msgAnchor, fileInfo)
}

// postRichMediaMsg 用 file_info 发一条 MsgType=7 富媒体消息。
func (b *Bot) postRichMediaMsg(ctx context.Context, group bool, openID, msgAnchor, fileInfo string) (string, error) {
	if b.api == nil {
		return "", ErrNotConnected
	}
	msg := richMediaMsg{
		MsgType: int(dto.RichMediaMsg),
		MsgID:   msgAnchor,
		MsgSeq:  b.nextSeq(),
		Media:   &richMediaInfo{FileInfo: fileInfo},
	}
	if group {
		m, err := b.api.PostGroupMessage(ctx, openID, msg)
		return msgID(m), err
	}
	m, err := b.api.PostC2CMessage(ctx, openID, msg)
	return msgID(m), err
}

// SendGroupImage 发群图片：把 imageURL 按 URL 模式上传富媒体 → 发 MsgType=7 消息。
// msgAnchor 空=主动（需权限）；非空=被动回复（5min 内锚点）。返回新消息 id。
func (b *Bot) SendGroupImage(ctx context.Context, groupID, msgAnchor, imageURL string) (string, error) {
	return b.sendRichMedia(ctx, true, groupID, msgAnchor, 1, imageURL)
}

// SendC2CImage 发私聊图片（同 SendGroupImage 语义）。
func (b *Bot) SendC2CImage(ctx context.Context, userID, msgAnchor, imageURL string) (string, error) {
	return b.sendRichMedia(ctx, false, userID, msgAnchor, 1, imageURL)
}

// SendGroupVideo 发群视频（file_type=2，URL 模式，QQ 自行下载该 URL）。
func (b *Bot) SendGroupVideo(ctx context.Context, groupID, msgAnchor, videoURL string) (string, error) {
	return b.sendRichMedia(ctx, true, groupID, msgAnchor, 2, videoURL)
}

// SendC2CVideo 发私聊视频（同 SendGroupVideo 语义）。
func (b *Bot) SendC2CVideo(ctx context.Context, userID, msgAnchor, videoURL string) (string, error) {
	return b.sendRichMedia(ctx, false, userID, msgAnchor, 2, videoURL)
}

// SendGroupVoice 发群语音（file_type=3，URL 模式）。官方要求 silk（QQ 原生语音格式）；
// voiceURL 指向 mp3/wav 时自动转码（下载→转 silk→图床发布公网 URL），.silk 直接透传。
func (b *Bot) SendGroupVoice(ctx context.Context, groupID, msgAnchor, voiceURL string) (string, error) {
	url, err := b.ensureSilkURL(ctx, voiceURL)
	if err != nil {
		return "", err
	}
	return b.sendRichMedia(ctx, true, groupID, msgAnchor, 3, url)
}

// SendC2CVoice 发私聊语音（同 SendGroupVoice 语义）。
func (b *Bot) SendC2CVoice(ctx context.Context, userID, msgAnchor, voiceURL string) (string, error) {
	url, err := b.ensureSilkURL(ctx, voiceURL)
	if err != nil {
		return "", err
	}
	return b.sendRichMedia(ctx, false, userID, msgAnchor, 3, url)
}

// SendGroupFile 发群文件（URL 版）：通用文件的 URL 模式腾讯不拉取（实测
// 40093007 富媒体文件下载失败，http/https、IP/域名均如此），这里改为服务端
// 代下载（上限 200MB）→ 分片直传。file_name 取 URL 路径末段。
func (b *Bot) SendGroupFile(ctx context.Context, groupID, msgAnchor, fileURL string) (string, error) {
	data, name, err := fetchFile(ctx, fileURL)
	if err != nil {
		return "", err
	}
	return b.SendGroupFileBytes(ctx, groupID, msgAnchor, name, data)
}

// SendC2CFile 发私聊文件（同 SendGroupFile 语义）。
func (b *Bot) SendC2CFile(ctx context.Context, userID, msgAnchor, fileURL string) (string, error) {
	data, name, err := fetchFile(ctx, fileURL)
	if err != nil {
		return "", err
	}
	return b.SendC2CFileBytes(ctx, userID, msgAnchor, name, data)
}

// SendGroupFileNamed 发群文件（URL 版）并指定展示文件名。音频直链的 URL 末段是
// 不可读的资源 ID（如 O400004W2yPD3GPnSO.ogg），点歌等场景用「歌名 - 歌手.ext」
// 命名，QQ 侧才能按扩展名预览播放。
func (b *Bot) SendGroupFileNamed(ctx context.Context, groupID, msgAnchor, fileURL, filename string) (string, error) {
	return b.sendFileNamed(ctx, true, groupID, msgAnchor, fileURL, filename)
}

// SendC2CFileNamed 发私聊文件并指定文件名（同 SendGroupFileNamed 语义）。
func (b *Bot) SendC2CFileNamed(ctx context.Context, userID, msgAnchor, fileURL, filename string) (string, error) {
	return b.sendFileNamed(ctx, false, userID, msgAnchor, fileURL, filename)
}

// sendFileNamed 代下载 URL 文件并以指定文件名分片直传；filename 为空时回落 URL 末段。
func (b *Bot) sendFileNamed(ctx context.Context, group bool, openID, msgAnchor, fileURL, filename string) (string, error) {
	data, name, err := fetchFile(ctx, fileURL)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(filename) != "" {
		name = filename
	}
	return b.sendMediaBytes(ctx, group, openID, msgAnchor, 4, name, data)
}

// SendGroupFileBytes 发群文件（本地字节，file_type=4 走分片直传；官方软限 200MB，
// 无格式要求——pdf/zip 等通用文件走这里）。
func (b *Bot) SendGroupFileBytes(ctx context.Context, groupID, msgAnchor, filename string, data []byte) (string, error) {
	return b.sendMediaBytes(ctx, true, groupID, msgAnchor, 4, filename, data)
}

// SendC2CFileBytes 发私聊文件（同 SendGroupFileBytes 语义）。
func (b *Bot) SendC2CFileBytes(ctx context.Context, userID, msgAnchor, filename string, data []byte) (string, error) {
	return b.sendMediaBytes(ctx, false, userID, msgAnchor, 4, filename, data)
}

// SendGroupMediaBytes 群发本地媒体字节（分片直传；fileType 1图片/2视频/4文件，
// 语音须先转 silk 走 SendGroupVoice）。图床未配置时的兜底路径。
func (b *Bot) SendGroupMediaBytes(ctx context.Context, groupID, msgAnchor string, fileType int, filename string, data []byte) (string, error) {
	return b.sendMediaBytes(ctx, true, groupID, msgAnchor, fileType, filename, data)
}

// SendC2CMediaBytes 发私聊媒体字节（同 SendGroupMediaBytes 语义）。
func (b *Bot) SendC2CMediaBytes(ctx context.Context, userID, msgAnchor string, fileType int, filename string, data []byte) (string, error) {
	return b.sendMediaBytes(ctx, false, userID, msgAnchor, fileType, filename, data)
}

// sendMediaBytes 分片上传本地字节并按 MsgType=7 发送。
func (b *Bot) sendMediaBytes(ctx context.Context, group bool, openID, msgAnchor string, fileType int, filename string, data []byte) (string, error) {
	fileInfo, err := b.uploadFileChunked(ctx, group, openID, fileType, filename, data)
	if err != nil {
		return "", err
	}
	return b.postRichMediaMsg(ctx, group, openID, msgAnchor, fileInfo)
}

// --- 分片上传（官方 chunked-upload 流程） ---

// maxFileBytes 通用文件官方硬限 200MB（服务端代下载的上限）。
const maxFileBytes = 200 << 20

// md5PrefixLen upload_prepare 要求的 md5_10m：文件前 10002432 字节（约 10MB）的 MD5。
const md5PrefixLen = 10002432

// fileHTTP 代下载文件的客户端（200MB 大文件不能沿用语音的 60s 超时）。
var fileHTTP = &http.Client{Timeout: 10 * time.Minute}

// chunkHTTP 分片 PUT 预签名地址的客户端（单片可能 10MB 级）。
var chunkHTTP = &http.Client{Timeout: 10 * time.Minute}

// uploadFileChunked 官方分片上传流程：upload_prepare（带 md5/sha1/md5_10m 校验值）
// → 按 parts 的预签名地址逐片 PUT → upload_part_finish 通知 → /files 传 upload_id
// 合并拿 file_info。适用于本地字节直传（通用文件唯一可用路径，也可兜底媒体）。
func (b *Bot) uploadFileChunked(ctx context.Context, group bool, openID string, fileType int, filename string, data []byte) (string, error) {
	if b.tokenSource == nil {
		return "", ErrNotConnected
	}
	tok, err := b.tokenSource.Token()
	if err != nil {
		return "", fmt.Errorf("取 token 失败: %w", err)
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

	scope := "groups"
	if !group {
		scope = "users"
	}

	// 1. 申请上传任务，拿分片列表。
	prepPayload, _ := json.Marshal(map[string]any{
		"file_type": fileType,
		"file_name": filename,
		"file_size": strconv.Itoa(len(data)),
		"md5":       hex.EncodeToString(md5Full[:]),
		"sha1":      hex.EncodeToString(sha1Full[:]),
		"md5_10m":   hex.EncodeToString(md5Head[:]),
	})
	prepBody, err := b.qqPOST(ctx, tok, fmt.Sprintf("%s/v2/%s/%s/upload_prepare", qqAPIBase, scope, openID), prepPayload)
	if err != nil {
		return "", fmt.Errorf("upload_prepare 失败: %w", err)
	}
	uploadID := gjson.GetBytes(prepBody, "upload_id").String()
	if uploadID == "" {
		return "", fmt.Errorf("upload_prepare 响应缺少 upload_id: %s", truncateStr(string(prepBody), 300))
	}

	// 2+3. 顺序 PUT 每个分片并通知完成（upload_config.concurrency 默认 1）。
	parts := gjson.GetBytes(prepBody, "parts").Array()
	if len(parts) == 0 {
		return "", fmt.Errorf("upload_prepare 未返回分片: %s", truncateStr(string(prepBody), 300))
	}
	offset := 0
	for _, p := range parts {
		presigned := p.Get("presigned_url").String()
		size := int(p.Get("block_size").Int())
		if presigned == "" || size <= 0 {
			return "", fmt.Errorf("分片信息异常: index=%d", p.Get("index").Int())
		}
		end := offset + size
		if end > len(data) {
			end = len(data)
		}
		part := data[offset:end]
		if len(part) == 0 {
			return "", fmt.Errorf("分片 %d 越界（offset=%d size=%d 总长=%d）", p.Get("index").Int(), offset, size, len(data))
		}
		if err := putChunk(ctx, presigned, part); err != nil {
			return "", fmt.Errorf("分片 %d 上传失败: %w", p.Get("index").Int(), err)
		}
		finPayload, _ := json.Marshal(map[string]any{
			"upload_id":  uploadID,
			"part_index": p.Get("index").Int(),
			"block_size": strconv.Itoa(len(part)),
			"md5":        fmt.Sprintf("%x", md5.Sum(part)),
		})
		endpoint := fmt.Sprintf("%s/v2/%s/%s/upload_part_finish", qqAPIBase, scope, openID)
		if _, err := b.qqPOST(ctx, tok, endpoint, finPayload); err != nil {
			return "", fmt.Errorf("分片 %d 完成通知失败: %w", p.Get("index").Int(), err)
		}
		offset = end
	}
	if offset != len(data) {
		return "", fmt.Errorf("分片总大小 %d 与文件 %d 不符", offset, len(data))
	}

	// 4. 合并拿 file_info（upload_id 模式，url 可空）。
	mergePayload, _ := json.Marshal(map[string]any{
		"file_type":    fileType,
		"srv_send_msg": false,
		"file_name":    filename,
		"upload_id":    uploadID,
	})
	mergeBody, err := b.qqPOST(ctx, tok, fmt.Sprintf("%s/v2/%s/%s/files", qqAPIBase, scope, openID), mergePayload)
	if err != nil {
		return "", fmt.Errorf("分片合并失败: %w", err)
	}
	fileInfo := gjson.GetBytes(mergeBody, "file_info").String()
	if fileInfo == "" {
		if fu := gjson.GetBytes(mergeBody, "file_uuid").String(); fu != "" {
			fileInfo = fu
		}
	}
	if fileInfo == "" {
		return "", fmt.Errorf("合并响应缺少 file_info: %s", truncateStr(string(mergeBody), 300))
	}
	return fileInfo, nil
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
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateStr(string(respBody), 200))
	}
	return nil
}

// qqPOST 带 QQBot 鉴权头 POST JSON 到腾讯接口，返回响应体（HTTP >= 400 报错）。
func (b *Bot) qqPOST(ctx context.Context, tok *oauth2.Token, endpoint string, payload []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "QQBot "+tok.AccessToken)
	req.Header.Set("X-Union-Appid", b.appID)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 300))
	}
	return body, nil
}

// fetchFile 代下载公网 URL 的文件（上限 200MB），返回字节与文件名（URL 路径末段）。
func fetchFile(ctx context.Context, rawURL string) ([]byte, string, error) {
	data, err := downloadFileWith(ctx, fileHTTP, rawURL, maxFileBytes)
	if err != nil {
		return nil, "", fmt.Errorf("下载文件失败: %w", err)
	}
	name := "file.bin"
	if u, err := url.Parse(rawURL); err == nil {
		if base := path.Base(u.Path); base != "" && base != "/" && !strings.HasSuffix(base, "/") {
			name = base
		}
	}
	return data, name, nil
}

// uploadRichMedia 按 URL 模式上传富媒体到群或 C2C，返回 file_info（用于 MsgType=7 发送）。
// fileType: 1图片(png/jpg,软限20MB)/2视频(mp4,30MB)/3语音(silk,20MB)/4文件(200MB)。
// imageURL 须公网可达（QQ 自行下载）。
//
// 为什么是 URL 模式：简单 multipart 直传（file/file_data 字段）已废——QQ 报 850026
// 「富媒体文件下载失败」。官方 /files 只支持 URL 模式（给 url，QQ 下载）和分片上传。
// 故图床提供一个公网 URL，本函数按 URL 模式交给 QQ 下载。
func (b *Bot) uploadRichMedia(ctx context.Context, group bool, openID string, fileType int, imageURL string) (string, error) {
	if b.tokenSource == nil {
		return "", ErrNotConnected
	}
	tok, err := b.tokenSource.Token()
	if err != nil {
		return "", fmt.Errorf("取 token 失败: %w", err)
	}
	endpoint := qqAPIBase + "/v2/groups/" + openID + "/files"
	if !group {
		endpoint = qqAPIBase + "/v2/users/" + openID + "/files"
	}

	payload, err := json.Marshal(map[string]any{
		"file_type":    fileType,
		"url":          imageURL,
		"srv_send_msg": false,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "QQBot "+tok.AccessToken)
	req.Header.Set("X-Union-Appid", b.appID)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("上传失败: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("上传 HTTP %d: %s", resp.StatusCode, truncateStr(string(respBody), 300))
	}
	// 腾讯返回 {file_uuid, file_info, ttl}；file_info 是喂回 MsgType=7 的不透明串。
	fileInfo := gjson.GetBytes(respBody, "file_info").String()
	if fileInfo == "" {
		// 兜底：有些实现只返回 file_uuid，用它当 file_info 试。
		if fu := gjson.GetBytes(respBody, "file_uuid").String(); fu != "" {
			fileInfo = fu
		}
	}
	if fileInfo == "" {
		return "", fmt.Errorf("上传响应缺少 file_info: %s", truncateStr(string(respBody), 300))
	}
	return fileInfo, nil
}

// 语音转码相关：下载源音频的客户端与大小上限（防超大文件拖垮内存）。
var voiceHTTP = &http.Client{Timeout: 60 * time.Second}

const maxVoiceBytes = 16 << 20 // 16MB，约几分钟语音的 mp3

// ensureSilkURL 确保语音 URL 指向 QQ 可播的 silk：后缀 .silk 直接透传；
// 否则下载源音频（mp3/wav，按后缀或内容嗅探）→ audio.ToSilk 转码 →
// 经图床（WithMediaHost）发布公网 URL（QQ 是 URL 模式下载，转码产物必须先能被公网取到）。
func (b *Bot) ensureSilkURL(ctx context.Context, voiceURL string) (string, error) {
	if strings.EqualFold(urlExt(voiceURL), "silk") {
		return voiceURL, nil
	}
	if b.mediaHost == nil || !b.mediaHost.Configured() {
		return "", fmt.Errorf("语音自动转码需要图床托管转码产物（未配置 IMGHOST_BASE_URL）")
	}
	data, err := downloadFile(ctx, voiceURL, maxVoiceBytes)
	if err != nil {
		return "", fmt.Errorf("下载音频失败: %w", err)
	}
	silk, err := audio.ToSilk(data, urlExt(voiceURL))
	if err != nil {
		return "", fmt.Errorf("音频转 silk 失败: %w", err)
	}
	pub, err := b.mediaHost.Upload(ctx, silk, "silk")
	if err != nil {
		return "", fmt.Errorf("发布转码后的 silk 失败: %w", err)
	}
	return pub, nil
}

// urlExt 取 URL 路径的扩展名（去点、转小写；无扩展返回空）。
func urlExt(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(path.Ext(u.Path), "."))
}

// downloadFile 下载 URL 内容（语音转码用，60s 超时），超过 max 字节报错。
func downloadFile(ctx context.Context, rawURL string, max int64) ([]byte, error) {
	return downloadFileWith(ctx, voiceHTTP, rawURL, max)
}

// downloadFileWith 用指定客户端下载 URL 内容，超过 max 字节报错
// （LimitReader 多读 1 字节以区分截断）。
func downloadFileWith(ctx context.Context, client *http.Client, rawURL string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 200))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("超过大小上限 %d MB", max>>20)
	}
	return data, nil
}

// truncateStr 截断字符串到 n 字符（便于错误日志）。
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
