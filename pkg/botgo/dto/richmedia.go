package dto

// 富媒体上传相关 dto（本地 fork 补充）。
//
// 两种上传模式：
//  1. URL 模式：POST /v2/{groups|users}/{openid}/files，body {file_type, url,
//     srv_send_msg}——url 须公网可达，QQ 服务端自行下载。图 20M/视频 30M/语音 20M
//     （软限）。官方对通用文件（file_type=4）不拉 URL（实测 40093007），须走分片直传。
//  2. 分片直传（官方 chunked-upload 流程）：upload_prepare（带 md5/sha1 校验值，拿
//     分片预签名地址列表）→ 逐片 PUT 预签名地址（无鉴权）→ upload_part_finish 通知
//     → POST files 传 upload_id 合并拿 file_info。官方 file 硬限 200MB。
//
// 返回的 FileInfo 是喂回 MsgType=7 富媒体消息（media.file_info）的不透明串。

// RichMediaFile 上传接口的响应（file_info 用于 MsgType=7 发送）。
type RichMediaFile struct {
	FileUUID string `json:"file_uuid"`
	FileInfo string `json:"file_info"`
	TTL      int    `json:"ttl"`
}

// RichMediaURLReq URL 模式上传请求。
type RichMediaURLReq struct {
	FileType   int    `json:"file_type"` // 1图片/2视频/3语音/4文件
	URL        string `json:"url"`
	SrvSendMsg bool   `json:"srv_send_msg"` // 是否立即以消息发出（false = 只上传拿 file_info）
}

// UploadPrepareReq 分片上传第一步请求（申请上传任务，拿分片列表）。
type UploadPrepareReq struct {
	FileType int    `json:"file_type"`
	FileName string `json:"file_name"`
	FileSize string `json:"file_size"` // 字符串形式的字节数（官方接口如此定义）
	MD5      string `json:"md5"`       // 全量 md5（hex）
	SHA1     string `json:"sha1"`      // 全量 sha1（hex）
	MD5TenM  string `json:"md5_10m"`   // 前 ~10MB（10002432 字节）的 md5（hex）
}

// UploadPrepareResult 分片上传任务信息（upload_config.concurrency 默认 1）。
type UploadPrepareResult struct {
	UploadID string       `json:"upload_id"`
	Parts    []UploadPart `json:"parts"`
}

// UploadPart 一个分片的预签名信息。
type UploadPart struct {
	Index        int    `json:"index"`
	PresignedURL string `json:"presigned_url"`
	BlockSize    int    `json:"block_size"`
}

// UploadPartFinishReq 分片上传完成通知请求。
type UploadPartFinishReq struct {
	UploadID  string `json:"upload_id"`
	PartIndex int    `json:"part_index"`
	BlockSize string `json:"block_size"` // 本片实际字节数（字符串形式）
	MD5       string `json:"md5"`        // 本片 md5（hex）
}

// MergeUploadReq 分片合并请求（upload_id 模式，url 可空）。
type MergeUploadReq struct {
	FileType   int    `json:"file_type"`
	SrvSendMsg bool   `json:"srv_send_msg"`
	FileName   string `json:"file_name"`
	UploadID   string `json:"upload_id"`
}
