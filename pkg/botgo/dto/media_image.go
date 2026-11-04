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
