package plugins

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"qqbot/internal/bot"
)

// System 提供「服务器状态」命令：用 markdown 展示主机/系统/CPU/内存/磁盘/负载/运行时长等。
type System struct {
	startTime time.Time // 进程启动时间（算 bot 运行时长）
}

// NewSystem 创建系统状态插件。
func NewSystem() *System {
	return &System{startTime: time.Now()}
}

// Install 注册命令。
func (p *System) Install(b *bot.Bot) {
	b.Command("状态", p.run).Desc("查看服务器状态").Alias("服务器状态", "status", "server").Category(bot.CatGeneral)
}

func (p *System) run(c *bot.Ctx) {
	_, _ = c.ReplyMarkdown(p.render())
}

// render 收集系统信息并渲染成 markdown。
func (p *System) render() string {
	hostname, _ := os.Hostname()

	var b strings.Builder
	fmt.Fprintf(&b, "# 🖥️ 服务器状态\n\n")
	fmt.Fprintf(&b, "- 🏷️ **主机**：%s\n", emptyOr(hostname, "未知"))
	fmt.Fprintf(&b, "- 📦 **系统**：%s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "- 🧬 **Go 版本**：%s\n", runtime.Version())
	fmt.Fprintf(&b, "- ⚙️ **CPU**：%d 核\n", runtime.NumCPU())
	fmt.Fprintf(&b, "- ⏱️ **运行时长**：%s\n", time.Since(p.startTime).Round(time.Second))

	// 进程内存
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	fmt.Fprintf(&b, "- 🧠 **进程内存**：%s / %s（堆/系统）\n", humanSize(ms.HeapAlloc), humanSize(ms.Sys))

	// 系统内存（Linux /proc/meminfo）
	if total, avail, ok := readMemInfo(); ok {
		used := total - avail
		fmt.Fprintf(&b, "- 💾 **系统内存**：%s / %s（已用 %.0f%%）\n", humanSize(used), humanSize(total), pct(used, total))
	}

	// 系统负载（Linux /proc/loadavg）
	if load := readLoadAvg(); load != "" {
		fmt.Fprintf(&b, "- 📊 **系统负载**：%s\n", load)
	}

	// 磁盘 /
	if total, free, ok := readDisk("/"); ok {
		used := total - free
		fmt.Fprintf(&b, "- 💽 **磁盘 /**：%s / %s（已用 %.0f%%）\n", humanSize(used), humanSize(total), pct(used, total))
	}

	fmt.Fprintf(&b, "- 🕐 **当前时间**：%s\n", time.Now().Format("2006-01-02 15:04:05"))
	return b.String()
}

// --- Linux 系统信息读取（非 Linux 或读取失败则跳过对应行）---

// readLoadAvg 读 /proc/loadavg 的 1/5/15 分钟负载。
func readLoadAvg() string {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return ""
	}
	f := strings.Fields(string(data))
	if len(f) >= 3 {
		return strings.Join(f[:3], " ")
	}
	return ""
}

// readMemInfo 读 /proc/meminfo 的 MemTotal 与 MemAvailable（字节）。
func readMemInfo() (total, avail uint64, ok bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		var kb uint64
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			if _, err := fmt.Sscanf(line, "MemTotal: %d", &kb); err == nil {
				total = kb * 1024
			}
		case strings.HasPrefix(line, "MemAvailable:"):
			if _, err := fmt.Sscanf(line, "MemAvailable: %d", &kb); err == nil {
				avail = kb * 1024
			}
		}
	}
	return total, avail, total > 0
}

// readDisk 读挂载点磁盘总量/空闲（字节），用 syscall.Statfs（Linux/Darwin）。
func readDisk(path string) (total, free uint64, ok bool) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, false
	}
	total = stat.Blocks * uint64(stat.Bsize)
	free = stat.Bfree * uint64(stat.Bsize)
	return total, free, true
}

// pct 计算 used/total 百分比。
func pct(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

// humanSize 把字节数格式化成人类可读（KiB/MiB/GiB…）。
func humanSize(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// emptyOr 空串时返回 fallback。
func emptyOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
