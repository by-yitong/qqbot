package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/tencent-connect/botgo/dto"
	"github.com/tidwall/gjson"
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
// group=true 走群、false 走 C2C；fileType: 1图片/2视频/3语音。
func (b *Bot) sendRichMedia(ctx context.Context, group bool, openID, msgAnchor string, fileType int, fileURL string) (string, error) {
	if b.api == nil {
		return "", ErrNotConnected
	}
	fileInfo, err := b.uploadRichMedia(ctx, group, openID, fileType, fileURL)
	if err != nil {
		return "", err
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

// uploadRichMedia 按 URL 模式上传富媒体到群或 C2C，返回 file_info（用于 MsgType=7 发送）。
// fileType: 1图片/2视频/3语音。imageURL 须公网可达（QQ 自行下载）。
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

// truncateStr 截断字符串到 n 字符（便于错误日志）。
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
