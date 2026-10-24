// 知乎 AI 插件：接 OpenAI 协议网关里的知乎官方模型（zhida-ai，即「看山」），
// 模型内置知乎工具——热榜、搜索、读问题/回答/文章（2026-09 实测：均为实时数据）。
//
// 命令 /知乎：
//
//	/知乎              看当前热榜（无参默认）
//	/知乎 <任意问题>    直接问：热榜第3条怎么回事 / 搜索 GMP 调度 / 总结某个问题的高赞回答
//	/知乎 重置         清空本聊天的上下文（新话题）
//
// 每聊天保留最近几轮对话（zhihuMaxMsgs 条、闲置 zhihuIdle 过期），支持「第2条展开讲讲」
// 这类追问。回复优先 markdown，被审核拒发时降级纯文本。
package plugins

import (
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"qqbot/internal/agent"
	"qqbot/internal/bot"
	"qqbot/internal/utils"
)

const (
	// zhihuMaxMsgs 每聊天保留的历史消息条数（user+assistant 各算一条）。
	zhihuMaxMsgs = 12
	// zhihuIdle 会话闲置过期时间：超过后下次提问从头开始（旧话题多半已过气）。
	zhihuIdle = 30 * time.Minute

	// 知乎知答网关与模型默认值（ZHIHU_BASE_URL / ZHIHU_MODEL 未配置时用）。
	zhihuDefaultBaseURL = "https://aihub.jmwl.dpdns.org/v1"
	zhihuDefaultModel   = "zhida-ai"
)

// zhihuSystemPrompt 约束输出为 QQ 好渲染的轻量 markdown + 防编链接。
const zhihuSystemPrompt = `你在 QQ 聊天里当知乎助手（背后是知乎官方 AI）。回答规则：
- 与知乎相关的问题（热榜/搜回答/看问题/找文章）优先用你内置的知乎能力，别凭记忆编。
- 回答用轻量 Markdown：**加粗**、- 列表、[标题](链接)。不要用表格、代码块和 # 大标题（QQ 不渲染）。
- 篇幅克制：普通回答 300 字以内；热榜/搜索列表最多 10 条，每条一行加粗标题 + 一句话摘要 + 链接。
- 提到具体回答/文章时给出知乎链接（可附作者或赞数）；没有可靠来源就不要给链接。
- 语气自然轻松，可以带少量 emoji；不要过度寒暄，不要连环反问。`

// zhihuHotListPrompt 无参调用时的默认问题。
const zhihuHotListPrompt = "给我看知乎当前的热榜，按你的列表格式来"

// ZhihuPlugin 知乎 AI 插件：/知乎 命令 → zhida-ai 模型（带每聊天短期记忆）。
type ZhihuPlugin struct {
	cli *agent.LLMClient

	mu       sync.Mutex
	sessions map[string]*zhihuSession
}

// zhihuSession 一个聊天的对话历史（内存态，重启即清）。
type zhihuSession struct {
	msgs []agent.Message
	at   time.Time // 最近一次提问时间（闲置过期用）
}

// NewZhihuPlugin 创建插件；baseURL/apiKey/model 为 OpenAI 协议网关三件套。
func NewZhihuPlugin(baseURL, apiKey, model string) *ZhihuPlugin {
	cli := agent.NewLLMClient(baseURL, apiKey, model)
	cli.OnUsage = func(u agent.Usage) { log.Printf("[zhihu] %s", u.String()) }
	return &ZhihuPlugin{cli: cli, sessions: map[string]*zhihuSession{}}
}

// NewZhihuFromEnv 读 ZHIHU_API_KEY（必填）构造；未配置返回 nil（不注册，密钥不入库）。
// ZHIHU_BASE_URL / ZHIHU_MODEL 可选，默认知乎知答网关与 zhida-ai 模型。
func NewZhihuFromEnv() bot.Plugin {
	key := strings.TrimSpace(os.Getenv("ZHIHU_API_KEY"))
	if key == "" {
		return nil
	}
	p := NewZhihuPlugin(
		utils.EnvOr("ZHIHU_BASE_URL", zhihuDefaultBaseURL),
		key,
		utils.EnvOr("ZHIHU_MODEL", zhihuDefaultModel),
	)
	log.Printf("[zhihu] 已启用知乎 AI 插件 /知乎（模型 %s）", p.cli.Model)
	return p
}

// Install 注册命令。
func (p *ZhihuPlugin) Install(b *bot.Bot) {
	b.Command("知乎", p.run).
		Desc("问知乎：热榜/搜回答/看问题（无参=热榜，重置=清上下文）").
		Usage("/知乎 <问题>").Alias("zhihu").Category(bot.CatAI)
}

func (p *ZhihuPlugin) run(c *bot.Ctx) {
	q := strings.TrimSpace(c.Args.Join())
	if q == "重置" || q == "清空" {
		p.reset(zhihuChatKey(c))
		c.ReplyText("知乎对话已重置，从新话题开始～")
		return
	}
	// 引用回复当作上下文带上（如引用一条分享链接问「总结下这个」）。
	if quote := strings.TrimSpace(c.Quote()); quote != "" {
		if q == "" {
			q = "帮我看看引用的这条内容是怎么回事"
		}
		q = "（引用消息：" + truncStr(quote, 300) + "）\n" + q
	} else if q == "" {
		q = zhihuHotListPrompt
	}

	key := zhihuChatKey(c)
	msgs := p.history(key)
	msgs = append(msgs, agent.NewUser(q))

	res, err := p.cli.Complete(c.Context, zhihuSystemPrompt, msgs, nil)
	if err != nil {
		log.Printf("[zhihu] 调用失败: %v", err)
		c.ReplyText("知乎查询失败：" + err.Error())
		return
	}
	p.save(key, agent.NewUser(q), agent.Message{Role: agent.RoleAssistant, Content: res.Content})

	if _, err := c.ReplyMarkdown(res.Content); err != nil {
		log.Printf("[zhihu] markdown 发送失败，降级纯文本: %v", err)
		c.ReplyText(zhihuPlain(res.Content))
	}
}

// history 取一个聊天的历史（过期视为无历史并清除）。
func (p *ZhihuPlugin) history(key string) []agent.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.sessions[key]
	if !ok {
		return nil
	}
	if time.Since(s.at) > zhihuIdle {
		delete(p.sessions, key)
		return nil
	}
	return s.msgs
}

// save 写回一轮对话，裁剪到 zhihuMaxMsgs 条。
func (p *ZhihuPlugin) save(key string, user, assistant agent.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.sessions[key]
	if s == nil {
		s = &zhihuSession{}
		p.sessions[key] = s
	}
	s.msgs = append(s.msgs, user, assistant)
	if n := len(s.msgs); n > zhihuMaxMsgs {
		s.msgs = append([]agent.Message{}, s.msgs[n-zhihuMaxMsgs:]...)
	}
	s.at = time.Now()
}

// reset 清空一个聊天的历史。
func (p *ZhihuPlugin) reset(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.sessions, key)
}

// zhihuChatKey 会话键：群/频道按聊天、私聊按用户。
func zhihuChatKey(c *bot.Ctx) string {
	switch {
	case c.IsGroup():
		return "g:" + c.GroupID()
	case c.IsChannel():
		return "c:" + c.ChannelID()
	default:
		return "u:" + c.UserID()
	}
}

// zhihuPlain markdown 降级纯文本：链接展开、去加粗号，QQ 纯文本消息里原样符号太丑。
func zhihuPlain(s string) string {
	s = zhihuMdLinkRE.ReplaceAllString(s, "$1：$2")
	s = strings.ReplaceAll(s, "**", "")
	return strings.TrimSpace(s)
}

// zhihuMdLinkRE 匹配 markdown 链接 [文字](url)。
var zhihuMdLinkRE = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
