package plugins

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"qqbot/internal/bot"
	"qqbot/pkg/bt"
)

// BtPlugin 查询宝塔面板所在服务器的状态（/system?action=GetSystemTotal）。
type BtPlugin struct {
	panel *bt.BtPanel
}

// NewBtPlugin 创建宝塔面板插件；baseURL/apiKey 为面板接口地址与密钥。
func NewBtPlugin(baseURL, apiKey string) *BtPlugin {
	return &BtPlugin{panel: bt.NewBtPanel(baseURL, apiKey)}
}

// NewBtFromEnv 读 BT_PANEL_URL/BT_PANEL_KEY 构造；未配置返回 nil（不注册，密钥不入库）。
func NewBtFromEnv() bot.Plugin {
	u, k := os.Getenv("BT_PANEL_URL"), os.Getenv("BT_PANEL_KEY")
	if u == "" || k == "" {
		return nil
	}
	log.Println("[bt] 已启用宝塔面板状态插件 /panel")
	return NewBtPlugin(u, k)
}

// Install 注册命令。
func (p *BtPlugin) Install(b *bot.Bot) {
	b.Command("panel", p.run).
		Desc("查看宝塔面板服务器状态").
		Alias("宝塔", "bt").
		Category(bot.CatGeneral)
}

func (p *BtPlugin) run(c *bot.Ctx) {
	res, err := p.panel.GetSystemTotal()
	if err != nil {
		c.ReplyText("获取失败：" + err.Error())
		return
	}
	_, _ = c.ReplyMarkdown(render(res))
}

// render 把 GetSystemTotal 结果渲染成 markdown。
func render(res *bt.SystemTotalRes) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 🖥️ 宝塔面板状态\n\n")
	fmt.Fprintf(&b, "- 📦 **面板版本**：%s\n", res.Version)
	fmt.Fprintf(&b, "- 🐧 **系统**：%s\n", res.System)
	fmt.Fprintf(&b, "- ⏱️ **运行时长**：%s\n", res.Time)
	fmt.Fprintf(&b, "- ⚙️ **CPU**：%d 核，使用率 %.1f%%\n", res.CPUNum, res.CPURealUsed)
	fmt.Fprintf(&b, "- 💾 **内存**：%s / %s（使用率 %.0f%%）\n",
		humanMB(res.MemRealUsed), humanMB(res.MemTotal), pctMB(res.MemRealUsed, res.MemTotal))
	fmt.Fprintf(&b, "  - 🟢 可用：%s ｜ 📦 缓存：%s ｜ 🧪 缓冲：%s\n",
		humanMB(res.MemAvailable), humanMB(res.MemCached), humanMB(res.MemBuffers))
	fmt.Fprintf(&b, "- 🕐 **当前时间**：%s\n", time.Now().Format("2006-01-02 15:04:05"))
	return b.String()
}

// humanMB 把 MB 数格式化成人类可读（小于 1G 显示 MB，否则 GiB）。
func humanMB(mb int64) string {
	if mb < 1024 {
		return fmt.Sprintf("%d MB", mb)
	}
	return fmt.Sprintf("%.1f GiB", float64(mb)/1024)
}

// pctMB 计算内存使用百分比。
func pctMB(used, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}
