package bot

import (
	"context"
	"testing"

	"github.com/tencent-connect/botgo/dto"
)

func TestIsImageAttachment(t *testing.T) {
	cases := []struct {
		ct, fn string
		want   bool
	}{
		{"image/png", "x.png", true},
		{"IMAGE/PNG", "x", true},                 // 大小写不敏感
		{"image/jpeg; charset=utf-8", "x", true}, // 带 ; 后缀
		{"", "photo.JPG", true},                  // content_type 空按扩展名兜底
		{"", "doc.pdf", false},                   // 非图扩展名
		{"voice", "", false},                     // 语音
		{"video/mp4", "clip.mp4", false},         // 视频
		{"", "", false},                          // 全空
		{"application/octet-stream", "img", false},
	}
	for _, tc := range cases {
		if got := isImageAttachment(tc.ct, tc.fn); got != tc.want {
			t.Errorf("isImageAttachment(%q,%q)=%v want %v", tc.ct, tc.fn, got, tc.want)
		}
	}
}

func TestExtractImages(t *testing.T) {
	atts := []*dto.MessageAttachment{
		{URL: "https://x/a.png", ContentType: "image/png", FileName: "a.png", Width: 100, Height: 200},
		{URL: "https://x/voice.amr", ContentType: "voice", FileName: "v.amr"}, // 非图，跳过
		{URL: "https://x/b.jpg", ContentType: "image/jpeg", FileName: "b.jpg"},
		nil,                          // nil 跳过
		{ContentType: "image/png"},   // 无 URL 跳过
		{URL: "", FileName: "c.gif"}, // 空 URL 跳过
	}
	imgs := extractImages(atts)
	if len(imgs) != 2 {
		t.Fatalf("want 2 images, got %d: %+v", len(imgs), imgs)
	}
	if imgs[0].URL != "https://x/a.png" || imgs[0].Width != 100 || imgs[0].Filename != "a.png" {
		t.Errorf("img0 = %+v", imgs[0])
	}
	if imgs[1].URL != "https://x/b.jpg" {
		t.Errorf("img1 = %+v", imgs[1])
	}
}

func TestExtractImagesEmpty(t *testing.T) {
	if got := extractImages(nil); got != nil {
		t.Errorf("nil 附件应返回 nil, got %v", got)
	}
	if got := extractImages([]*dto.MessageAttachment{}); got != nil {
		t.Errorf("空附件应返回 nil, got %v", got)
	}
}

func TestFromMessageExtractsImages(t *testing.T) {
	b := newTestBot()
	m := &dto.Message{
		ID:      "m1",
		GroupID: "g1",
		Content: "<@123> 你好",
		Author:  &dto.User{ID: "u1"},
		Attachments: []*dto.MessageAttachment{
			{URL: "https://cdn/img.png", ContentType: "image/png", FileName: "img.png", Width: 1254, Height: 1254},
		},
	}
	c := b.fromMessage(context.Background(), "group", m, true)
	imgs := c.Images()
	if len(imgs) != 1 {
		t.Fatalf("want 1 image, got %d", len(imgs))
	}
	if imgs[0].URL != "https://cdn/img.png" || imgs[0].Width != 1254 {
		t.Errorf("img = %+v", imgs[0])
	}
	// 无附件消息：Images() 返回 nil
	m2 := &dto.Message{ID: "m2", Content: "纯文本", Author: &dto.User{ID: "u2"}}
	c2 := b.fromMessage(context.Background(), "c2c", m2, false)
	if got := c2.Images(); got != nil {
		t.Errorf("无附件应 Images()==nil, got %v", got)
	}
}
