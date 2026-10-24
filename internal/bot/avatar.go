package bot

import "fmt"

// 用户头像直链（q.qlogo.cn，免鉴权 GET，返回 image/jpeg）：
//
//	https://q.qlogo.cn/qqapp/{appid}/{openid}/{size}
//
// appid 必须是本 bot 的 appid——openid 按 app 隔离，appid 不匹配、误传群 openid
// 或未知 openid 时不报错，静默返回 40×40 默认灰头像（~1.5KB）。size 单段可选
// 0/40/100/140/640（640 为大图；两段后缀返回 400）。详见 docs/development.md。
//
// 2026-09 实测有效，比官方用户信息接口省事且无 QPS 顾虑。

// AvatarURL 返回某 openid 用户的头像大图 URL（640）。openid 为空返回空串。
// 该 URL 免鉴权、公网可达，可直接作为富媒体图片 URL 发送（SendGroupImage 等）。
func (b *Bot) AvatarURL(openid string) string {
	if openid == "" || b.cfg.AppID == "" {
		return ""
	}
	return fmt.Sprintf("https://q.qlogo.cn/qqapp/%s/%s/640", b.cfg.AppID, openid)
}

// AvatarURLTemplate 返回头像 URL 模板：把 {openid} 换成任一成员 openid 即其头像
// （群管工具的输出提示用，让 AI 能自行拼出每个成员的头像链接）。
func (b *Bot) AvatarURLTemplate() string {
	if b.cfg.AppID == "" {
		return ""
	}
	return fmt.Sprintf("https://q.qlogo.cn/qqapp/%s/{openid}/640", b.cfg.AppID)
}
