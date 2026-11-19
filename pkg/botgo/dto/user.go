package dto

// User 用户
type User struct {
	ID               string `json:"id"`
	Username         string `json:"username"`
	Avatar           string `json:"avatar"`
	Bot              bool   `json:"bot"`
	UnionOpenID      string `json:"union_openid"`       // 特殊关联应用的 openid
	UnionUserAccount string `json:"union_user_account"` // 机器人关联的用户信息，与union_openid关联的应用是同一个

	MemberNickname string `json:"member_nickname,omitempty"` // 官方 2026-11 新增：群场景下用户在群内的昵称
	UserOpenID     string `json:"user_openid,omitempty"`     // 官方 2026-11 新增：C2C 场景下用户的 openid
}

// UserInfo 用户基本信息（GET /v2/users/{user_openid}/info，10 QPS，
// 错误码 40184001=与机器人无好友关系）。官方 2026-11-16 新增。
type UserInfo struct {
	UnionOpenID       string `json:"union_openid"`        // 跨应用关联用户 openid（绑定 3 方应用时有效）
	Username          string `json:"username"`            // 用户名
	Avatar            string `json:"avatar"`              // 用户头像原图 URL
	AllowProactiveMsg bool   `json:"allow_proactive_msg"` // 是否接收主动推送
}
