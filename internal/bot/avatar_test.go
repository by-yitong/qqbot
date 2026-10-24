package bot

import "testing"

// TestAvatarURL 头像直链构造：appid + openid 拼 q.qlogo.cn URL；空入参返回空串。
func TestAvatarURL(t *testing.T) {
	b := &Bot{cfg: Config{AppID: "102898720"}}
	if got, want := b.AvatarURL("ABC123"), "https://q.qlogo.cn/qqapp/102898720/ABC123/640"; got != want {
		t.Errorf("AvatarURL = %q, want %q", got, want)
	}
	if b.AvatarURL("") != "" {
		t.Error("openid 为空应返回空串")
	}
	if got, want := b.AvatarURLTemplate(), "https://q.qlogo.cn/qqapp/102898720/{openid}/640"; got != want {
		t.Errorf("AvatarURLTemplate = %q, want %q", got, want)
	}
	empty := &Bot{}
	if empty.AvatarURL("X") != "" || empty.AvatarURLTemplate() != "" {
		t.Error("未配置 appid 时应返回空串（调用方跳过头像）")
	}
}
