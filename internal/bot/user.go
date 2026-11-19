package bot

// 用户基本信息 API 门面（协议实现在 pkg/botgo，本层只做连接保护与类型别名）。

import (
	"context"

	"qqbot/pkg/botgo/dto"
)

// UserInfo 是用户基本信息。
type UserInfo = dto.UserInfo

// GetUserInfo 获取用户基本信息（用户名/头像原图/是否接收主动推送）。
// GET /v2/users/{user_openid}/info，10 QPS；错误码 40184001=与机器人无好友关系。
func (b *Bot) GetUserInfo(ctx context.Context, userOpenID string) (*UserInfo, error) {
	api, err := b.openapi()
	if err != nil {
		return nil, err
	}
	return api.GetUserInfo(ctx, userOpenID)
}
