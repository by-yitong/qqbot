package bot

import "testing"

// 标签字符串必须与官方文档逐字符一致（含自闭合斜杠前空格）。
func TestAtEveryoneTag(t *testing.T) {
	if AtEveryoneTag != "<qqbot-at-everyone />" {
		t.Errorf("AtEveryoneTag=%q", AtEveryoneTag)
	}
}
