package bot

import (
	"fmt"
	"log"
	"net/url"
)

// CmdInputTag 生成官方内嵌「参数指令」标签：客户端把消息渲染成可点标签，点击把
// text 插入输入框（用户补个发送）。text/show 官方要求 urlencode、各上限 100 字符；
// show 为空时取 text；reference 固定 false（不带引用回复）。
//
// 同款「回车指令」qqbot-cmd-enter（点击直接发送）群聊/文字子频道不支持，群聊为主
// 场的本项目统一用 input 版。注意标签要随 markdown（MsgType=2）发，纯文本里只是
// 一串字符串；频道对标签的支持不确定，频道场景应发纯文本版。
func CmdInputTag(text, show string) string {
	if show == "" {
		show = text
	}
	return fmt.Sprintf(`<qqbot-cmd-input text="%s" show="%s" reference="false" />`,
		url.QueryEscape(text), url.QueryEscape(show))
}

// ReplyTaggedOrPlain 回一条带内嵌指令标签的 markdown；频道或 markdown 被审核拒发时
// 降级纯文本（plain 传同内容的无标签版本，保证信息不丢）。
func (c *Ctx) ReplyTaggedOrPlain(md, plain string) {
	if c.IsChannel() {
		_, _ = c.ReplyText(plain)
		return
	}
	if _, err := c.ReplyMarkdown(md); err != nil {
		log.Printf("[bot] 标签 markdown 发送失败，降级纯文本: %v", err)
		_, _ = c.ReplyText(plain)
	}
}
