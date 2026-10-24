package plugins

import (
	"fmt"
	"os"
	"strings"
	"time"

	"qqbot/internal/bot"
	"qqbot/internal/utils"
)

type ZhiPu struct {
}

func NewZhiPu() *ZhiPu {
	return &ZhiPu{}
}

// Install 注册命令。
func (p *ZhiPu) Install(b *bot.Bot) {
	b.Command("zhipu", p.run).
		Desc("查询智谱coding plan用量").
		Usage("/zhipu").Alias("智谱用量").Category(bot.CatAI)
}

func (p *ZhiPu) run(c *bot.Ctx) {
	var apikey = strings.TrimSpace(os.Getenv("ZHIPU_API_KEY"))
	if apikey == "" {
		c.ReplyText("请配置智谱apikey")
		return
	}

	api := "https://open.bigmodel.cn/api/monitor/usage/quota/limit"

	var res ZhiPuTimeLimitRes
	if _, err := utils.HttpClient.R().SetAuthToken(apikey).SetResult(&res).Get(api); err != nil {
		c.ReplyText("请求失败，" + err.Error())
		return
	}
	if res.Code != 200 {
		c.ReplyText("获取失败，" + res.Msg)
		return
	}

	var mcpLimit *Limit   // TIME_LIMIT：MCP 本月次数额度（usage=总额度, currentValue=已用）
	var tokenLimit *Limit // TOKENS_LIMIT：N 小时额度（number=小时数, percentage=已用%）
	for i := range res.Data.Limits {
		switch res.Data.Limits[i].Type {
		case "TIME_LIMIT":
			mcpLimit = &res.Data.Limits[i]
		case "TOKENS_LIMIT":
			tokenLimit = &res.Data.Limits[i]
		}
	}

	c.ReplyMarkdown(renderZhipuUsage(res.Data.Level, mcpLimit, tokenLimit))
}

// renderZhipuUsage 渲染用量卡片（纯函数便于测试）。nil 的额度段整体跳过。
// 展示三块：N 小时额度（已用% + 重置）、MCP 本月用量（已用/总量 + 重置）、各 MCP 次数。
func renderZhipuUsage(level string, mcp, tokens *Limit) string {
	if level == "" {
		level = "未知"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## 🤖 智谱 Coding Plan %s 套餐用量\n\n", level))
	if tokens != nil {
		window := ""
		if tokens.Number > 0 {
			window = fmt.Sprintf("%d小时", tokens.Number)
		} else {
			window = "额度"
		}
		sb.WriteString(fmt.Sprintf("\n### ⏱️ %s额度：已用 **%d%%** %s\n", window, tokens.Percentage, quotaEmoji(tokens.Percentage)))
		sb.WriteString(fmt.Sprintf("重置：%s（约%s）\n", fmtResetTime(tokens.NextResetTime), fmtIn(futureDur(tokens.NextResetTime))))
	}
	if mcp != nil {
		total := derefI64(mcp.Usage)
		sb.WriteString(fmt.Sprintf("\n### 📦 MCP 本月用量：%d / %d（%d%%）%s\n",
			derefI64(mcp.CurrentValue), total, mcp.Percentage, quotaEmoji(mcp.Percentage)))
		sb.WriteString(fmt.Sprintf("重置：%s（约%s）\n", fmtResetTime(mcp.NextResetTime), fmtIn(futureDur(mcp.NextResetTime))))
		if len(mcp.UsageDetails) > 0 {
			sb.WriteString("\n**各 MCP 使用次数**\n\n| MCP | 次数 |\n|---|---:|\n")
			for _, d := range mcp.UsageDetails {
				sb.WriteString(fmt.Sprintf("| %s | %d |\n", mcpLabel(d.ModelCode), d.Usage))
			}
		}
	}
	if tokens == nil && mcp == nil {
		sb.WriteString("\n未找到额度信息\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// mcpLabel 给常见 MCP 编码配中文名，未知的原样显示。
func mcpLabel(code string) string {
	names := map[string]string{
		"search-prime": "联网搜索",
		"web-reader":   "网页读取",
		"zread":        "开源仓库",
	}
	if n, ok := names[code]; ok {
		return n + " `" + code + "`"
	}
	return "`" + code + "`"
}

// derefI64 解引用，nil 视为 0。
func derefI64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// quotaEmoji 用量百分比配个灯，一眼看紧不紧张。
func quotaEmoji(p int64) string {
	switch {
	case p >= 90:
		return "🔴"
	case p >= 70:
		return "🟠"
	case p >= 40:
		return "🟡"
	default:
		return "🟢"
	}
}

// fmtResetTime 毫秒时间戳 → 本地 "MM-dd HH:mm"。
func fmtResetTime(ms int64) string {
	if ms <= 0 {
		return "未知"
	}
	return time.UnixMilli(ms).Format("01-02 15:04")
}

// futureDur 距离重置还有多久（已过则为 0）。
func futureDur(ms int64) time.Duration {
	if d := time.Until(time.UnixMilli(ms)); d > 0 {
		return d
	}
	return 0
}

// fmtIn 时长 → 中文约数（"2天3小时" / "5小时20分" / "40分钟" / "即将重置"）。
func fmtIn(d time.Duration) string {
	if d <= 0 {
		return "即将重置"
	}
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%d天%d小时后", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%d小时%d分后", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%d分钟后", int(d.Minutes()))
	}
}

type ZhiPuTimeLimitRes struct {
	Code    int64  `json:"code"`
	Msg     string `json:"msg"`
	Data    Data   `json:"data"`
	Success bool   `json:"success"`
}

type Data struct {
	Limits []Limit `json:"limits"`
	Level  string  `json:"level"`
}

type Limit struct {
	Type          string        `json:"type"`
	Unit          int64         `json:"unit"`
	Number        int64         `json:"number"`
	Usage         *int64        `json:"usage,omitempty"`
	CurrentValue  *int64        `json:"currentValue,omitempty"`
	Remaining     *int64        `json:"remaining,omitempty"`
	Percentage    int64         `json:"percentage"`
	NextResetTime int64         `json:"nextResetTime"`
	UsageDetails  []UsageDetail `json:"usageDetails,omitempty"`
}

type UsageDetail struct {
	ModelCode string `json:"modelCode"`
	Usage     int64  `json:"usage"`
}

// {
//   "code": 200,
//   "msg": "Operation successful",
//   "data": {
//     "limits": [
//       {
//         "type": "TIME_LIMIT",
//         "unit": 5,
//         "number": 1, // 1个月限额
//         "usage": 4000,
//         "currentValue": 1020,
//         "remaining": 2980,
//         "percentage": 25, // mcp总已用 25%
//         "nextResetTime": 1788405210997, // 下次重置时间
//         "usageDetails": [
//           {
//             "modelCode": "search-prime", // 联网搜索mcp
//             "usage": 414
//           },
//           {
//             "modelCode": "web-reader", // 网页读取mcp
//             "usage": 606
//           },
//           {
//             "modelCode": "zread", // 开源仓库mcp
//             "usage": 0
//           }
//         ]
//       },
//       {
//         "type": "TOKENS_LIMIT",
//         "unit": 3,
//         "number": 5, // 5小时限额
//         "percentage": 1, // 已使用1%
//         "nextResetTime": 1786725341735 // 重置时间
//       }
//     ],
//     "level": "max"
//   },
//   "success": true
// }
// 展示的时候，展示 5小时用量 以及下次重置时间 mcp本月用量 重置时间 各个mcp使用次数 即可  level max 套餐类型：max
