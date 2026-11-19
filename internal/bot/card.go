package bot

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/by-yitong/botgo/dto"
)

// 图文卡片（官方 2026-10-28 新增，群聊/单聊支持）：msg_type=8 + card（type="tuwen"）。
// 官方约束：title 1-50 字、description 1-200 字、url/pic_url 须 https 且 ≤1024 字符；
// 这里客户端侧提前拦（错误更友好），服务端仍会兜底校验（304061 口径）。

const (
	cardTitleMax = 50
	cardDescMax  = 200
	cardURLMax   = 1024
	tuwenCard    = "tuwen"
)

// validateCard 客户端侧校验图文卡片内容（长度按字符数计）。
func validateCard(c dto.CardContent) error {
	if n := utf8.RuneCountInString(c.Title); n < 1 || n > cardTitleMax {
		return fmt.Errorf("卡片标题须 1-%d 字（当前 %d）", cardTitleMax, n)
	}
	if n := utf8.RuneCountInString(c.Description); n < 1 || n > cardDescMax {
		return fmt.Errorf("卡片描述须 1-%d 字（当前 %d）", cardDescMax, n)
	}
	if !strings.HasPrefix(c.URL, "https://") {
		return fmt.Errorf("卡片 url 须为 https 链接: %q", c.URL)
	}
	if len(c.URL) > cardURLMax {
		return fmt.Errorf("卡片 url 超过 %d 字符", cardURLMax)
	}
	if !strings.HasPrefix(c.PicURL, "https://") {
		return fmt.Errorf("卡片 pic_url 须为 https 链接: %q", c.PicURL)
	}
	if len(c.PicURL) > cardURLMax {
		return fmt.Errorf("卡片 pic_url 超过 %d 字符", cardURLMax)
	}
	return nil
}

// sendCard 发图文卡片公共路径（group=true 走群）。
func (b *Bot) sendCard(ctx context.Context, group bool, openID, msgAnchor string, card dto.CardContent) (string, error) {
	if b.api == nil {
		return "", ErrNotConnected
	}
	if err := validateCard(card); err != nil {
		return "", err
	}
	msg := &dto.MessageToCreate{
		MsgType: dto.CardMsg,
		Card:    &dto.Card{Type: tuwenCard, Content: card},
		MsgID:   msgAnchor,
		MsgSeq:  b.nextSeq(),
	}
	if group {
		m, err := b.api.PostGroupMessage(ctx, openID, msg)
		return msgID(m), err
	}
	m, err := b.api.PostC2CMessage(ctx, openID, msg)
	return msgID(m), err
}

// SendGroupCard 发群图文卡片：msgAnchor 空=主动（需权限，受频控）；非空=被动回复。
func (b *Bot) SendGroupCard(ctx context.Context, groupID, msgAnchor string, card dto.CardContent) (string, error) {
	return b.sendCard(ctx, true, groupID, msgAnchor, card)
}

// SendC2CCard 发私聊图文卡片（同 SendGroupCard 语义）。
func (b *Bot) SendC2CCard(ctx context.Context, userID, msgAnchor string, card dto.CardContent) (string, error) {
	return b.sendCard(ctx, false, userID, msgAnchor, card)
}
