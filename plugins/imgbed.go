package plugins

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"qqbot/internal/bot"
	"qqbot/internal/utils"
	"qqbot/pkg/imgbed"
)

// Imgbed 是图床上传插件：用户把图片和「图床」命令一起发出（群聊需 @bot 或带命令前缀），
// bot 从 QQ CDN 下载原图字节，再 multipart 上传到图床，回复公网外链。
//
// 自然语言（「帮我把这张图传图床」）走的是 agent 兜底的 upload_image 工具，
// 与本插件共用同一个 imgbed.Client。
type Imgbed struct {
	cli      *imgbed.Client
	maxBytes int64        // 单图大小上限；<=0 用 imgbed.DefaultMaxBytes
	fetch    *http.Client // 下载 QQ CDN 图（nil 用 imgbed 包默认）
}

// NewImgbed 创建插件。maxBytes 是单图大小上限（<=0 默认 10MB）。
func NewImgbed(cli *imgbed.Client, maxBytes int64) *Imgbed {
	return &Imgbed{cli: cli, maxBytes: maxBytes, fetch: &http.Client{Timeout: 30 * time.Second}}
}

// NewImgbedFromEnv 读 IMGBED_TOKEN 等变量构造「/图床」命令插件；未配 token 返回 nil 插件。
// 返回的 cli/maxBytes 同时供 agent 的 upload_image 工具复用（自然语言「把这张图传图床」），
// 未配置时 cli 为 nil。
func NewImgbedFromEnv() (p bot.Plugin, cli *imgbed.Client, max int64) {
	tok := os.Getenv("IMGBED_TOKEN")
	if tok == "" {
		return nil, nil, 0
	}
	base := utils.EnvOr("IMGBED_BASE_URL", "https://imgbed.jmwl.dpdns.org")
	cli = &imgbed.Client{BaseURL: base, Token: tok}
	max = int64(utils.EnvInt("IMGBED_MAX_MB", 10)) << 20
	log.Println("[imgbed] 已启用图床上传 /图床 → " + base)
	return NewImgbed(cli, max), cli, max
}

// Install 注册命令。
func (p *Imgbed) Install(b *bot.Bot) {
	b.Command("图床", p.run).
		Desc("把消息里的图片上传图床，回复公网外链").
		Alias("imgbed", "外链", "上传图片").
		Category(bot.CatGeneral)
}

func (p *Imgbed) run(c *bot.Ctx) {
	imgs := c.Images()
	if len(imgs) == 0 {
		_, _ = c.ReplyText("把图片和「图床」命令一起发给我（群里要 @ 我），我传图床后把外链给你；也可以直接说『把这张图传图床』")
		return
	}
	_, _ = c.ReplyText(p.uploadAll(c.Context, imgs))
}

// uploadAll 逐张下载并上传，汇总成一条回复文本（部分失败不影响其余图片）。
func (p *Imgbed) uploadAll(ctx context.Context, imgs []bot.Image) string {
	lines := make([]string, 0, len(imgs))
	ok := 0
	for i, img := range imgs {
		u, err := p.uploadOne(ctx, img)
		if err != nil {
			lines = append(lines, fmt.Sprintf("%d. 失败：%v", i+1, err))
			continue
		}
		ok++
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, u))
	}
	return fmt.Sprintf("已上传 %d/%d 张：\n%s", ok, len(imgs), strings.Join(lines, "\n"))
}

// uploadOne 下载一张 QQ 附件图并上传图床，返回外链。
func (p *Imgbed) uploadOne(ctx context.Context, img bot.Image) (string, error) {
	data, ct, err := imgbed.DownloadImage(ctx, img.URL, p.maxBytes, p.fetch)
	if err != nil {
		return "", err
	}
	if ct == "" {
		ct = img.ContentType
	}
	f, err := p.cli.Upload(ctx, imgbed.FilenameFor(img.Filename, ct), ct, data)
	if err != nil {
		return "", err
	}
	return f.URL, nil
}
