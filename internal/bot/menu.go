package bot

// 自定义菜单与指令面板（协议实现在 pkg/botgo：GET/PUT /v2/menu、POST/DELETE /v2/panels）。
// 本层保留发布编排（配置加载 / 按命令自动生成 / 覆盖式应用）。
//
//   - 自定义菜单：C2C 单聊窗口底部按钮栏，PUT /v2/menu 覆盖式设置，对所有用户生效。
//     按钮类型 send_message（点击把文本填入输入框，用户仍需按发送）/ link / menu（折叠）。
//   - 指令面板：按场景（c2c/group/channel/dm）展示指令/链接面板，POST /v2/panels 创建。
//
// 文档：https://bot.q.qq.com/wiki/develop/api-v2/server-inter/menu-panel/

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"qqbot/pkg/botgo/dto"
)

// AutoPanelRemark 是本程序自动创建面板的 remark 标记：重新发布时按它识别并删除旧面板
// （面板是追加式的，不删会堆积到 20 个上限）。
const AutoPanelRemark = "qqbot-auto"

// Menu 是全局自定义菜单（C2C 场景）。
type Menu = dto.Menu

// MenuItem 是一级菜单项。
type MenuItem = dto.MenuItem

// SubMenuItem 是二级菜单项。
type SubMenuItem = dto.SubMenuItem

// PanelReq 是创建指令面板的请求。
type PanelReq = dto.PanelReq

// Panel 是面板配置（元素最多 20 个）。
type Panel = dto.Panel

// PanelItem 是面板元素。
type PanelItem = dto.PanelItem

// PanelEntry 是面板列表查询结果。
type PanelEntry = dto.PanelEntry

// --- API 调用（委托 pkg/botgo）---

// SetMenu 覆盖式设置全局自定义菜单，返回新版本号。
func (b *Bot) SetMenu(ctx context.Context, m Menu) (int, error) {
	api, err := b.openapi()
	if err != nil {
		return 0, err
	}
	return api.SetMenu(ctx, m)
}

// GetMenu 查询当前全局自定义菜单。
func (b *Bot) GetMenu(ctx context.Context) (Menu, error) {
	api, err := b.openapi()
	if err != nil {
		return Menu{}, err
	}
	m, err := api.GetMenu(ctx)
	if err != nil {
		return Menu{}, err
	}
	return *m, nil
}

// CreatePanel 创建指令面板，返回 panel_id。
func (b *Bot) CreatePanel(ctx context.Context, p PanelReq) (string, error) {
	api, err := b.openapi()
	if err != nil {
		return "", err
	}
	return api.CreatePanel(ctx, p)
}

// ListPanels 查询某场景的面板列表（自动翻页）。
func (b *Bot) ListPanels(ctx context.Context, scope string) ([]PanelEntry, error) {
	api, err := b.openapi()
	if err != nil {
		return nil, err
	}
	return api.ListPanels(ctx, scope)
}

// DeletePanel 删除一个指令面板。
func (b *Bot) DeletePanel(ctx context.Context, panelID string) error {
	api, err := b.openapi()
	if err != nil {
		return err
	}
	return api.DeletePanel(ctx, panelID)
}

// --- 配置与发布编排 ---

// MenuConfig 是菜单 + 面板的完整发布配置（menu.json 的格式）。
type MenuConfig struct {
	Menu   Menu       `json:"menu"`
	Panels []PanelReq `json:"panels"`
}

// DefaultMenuConfig 内置默认：C2C 底部菜单（命令直达）+ 群指令面板（全局）。
// command/menu 按钮点击后是把文本填入输入框，内容用中文别名（如「状态」）——
// 命令默认裸文本即可命中（见 matchCommand），面板点了就有反应。
func DefaultMenuConfig() MenuConfig {
	return MenuConfig{
		Menu: Menu{Items: []MenuItem{
			{Name: "帮助", Type: "send_message", SendMessage: "帮助"},
			{Name: "状态", Type: "send_message", SendMessage: "状态"},
			{Name: "用量", Type: "send_message", SendMessage: "用量"},
			{Name: "会话", Type: "menu", SubMenuItems: []SubMenuItem{
				{Name: "停止当前任务", Type: "send_message", SendMessage: "停止"},
				{Name: "清空对话", Type: "send_message", SendMessage: "清空对话"},
			}},
		}},
		Panels: []PanelReq{
			{
				Scope: "group", TargetType: "all",
				Panel: Panel{
					Remark: AutoPanelRemark,
					Items: []PanelItem{
						{Type: "command", Name: "帮助", Desc: "查看所有命令"},
						{Type: "command", Name: "状态", Desc: "当前处理状态"},
						{Type: "command", Name: "用量", Desc: "token 用量统计"},
					},
				},
			},
		},
	}
}

// AutoMenuConfig 按当前已注册命令自动生成发布配置：每个非隐藏、未 HideBotMenu、
// 未 RequireAtOrPrefix（面板填入的是裸命令名，点了不会命中）的命令进 C2C 底部菜单
// （send_message，点击填入命令名）和群指令面板（command 项）。
// OwnerOnly / AdminOnly 命令不进面板/菜单——面板对所有用户可见，受限命令不该出现在那里。
// 在发布时（OnStart / .menu apply）调用，保证所有命令都已注册完毕。
func (b *Bot) AutoMenuConfig() MenuConfig {
	b.mu.RLock()
	defer b.mu.RUnlock()
	cfg := MenuConfig{}
	panel := Panel{Remark: AutoPanelRemark}
	for _, c := range b.cmds {
		if c.hidden || c.hideBotMenu || c.requireAt || c.ownerOnly || c.adminOnly {
			continue
		}
		if len(cfg.Menu.Items) < 10 { // 菜单上限 10 项
			cfg.Menu.Items = append(cfg.Menu.Items, MenuItem{
				Name: truncateStr(c.name, 10), Type: "send_message", SendMessage: c.name,
			})
		}
		if len(panel.Items) < 20 { // 面板上限 20 项
			panel.Items = append(panel.Items, PanelItem{
				Type: "command", Name: truncateStr(c.name, 14), Desc: truncateStr(c.desc, 30),
			})
		}
	}
	if len(panel.Items) > 0 {
		cfg.Panels = append(cfg.Panels, PanelReq{Scope: "group", TargetType: "all", Panel: panel})
	}
	return cfg
}

// LoadMenuConfig 从 JSON 文件加载发布配置；文件不存在返回 (nil, nil)（用内置默认）。
func LoadMenuConfig(path string) (*MenuConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var cfg MenuConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}
	return &cfg, nil
}

// ApplyMenus 发布菜单与面板，返回可读摘要。
// 菜单是覆盖式（PUT）直接更新；面板是追加式，先删除本程序旧面板（按 AutoPanelRemark
// 识别，不动手动创建的）再重建，避免重启堆积（面板上限 20 个）。
func (b *Bot) ApplyMenus(ctx context.Context, cfg MenuConfig) (string, error) {
	if _, err := b.SetMenu(ctx, cfg.Menu); err != nil {
		return "", fmt.Errorf("菜单设置失败: %w", err)
	}

	scopes := map[string]bool{}
	for _, p := range cfg.Panels {
		if p.Scope != "" {
			scopes[p.Scope] = true
		}
	}
	deleted := 0
	for scope := range scopes {
		entries, err := b.ListPanels(ctx, scope)
		if err != nil {
			return "", fmt.Errorf("查询旧面板失败(scope=%s): %w", scope, err)
		}
		for _, e := range entries {
			if e.Panel.Remark != AutoPanelRemark {
				continue // 手动创建的面板不动
			}
			if err := b.DeletePanel(ctx, e.PanelID); err != nil {
				return "", fmt.Errorf("删除旧面板 %s 失败: %w", e.PanelID, err)
			}
			deleted++
		}
	}

	created := 0
	for _, p := range cfg.Panels {
		if p.Panel.Remark == "" {
			p.Panel.Remark = AutoPanelRemark // 没写 remark 的也打上标记，纳入生命周期管理
		}
		if _, err := b.CreatePanel(ctx, p); err != nil {
			return "", fmt.Errorf("创建面板(scope=%s)失败: %w", p.Scope, err)
		}
		created++
	}
	return fmt.Sprintf("菜单已更新；面板删除旧 %d 个、新建 %d 个", deleted, created), nil
}

// ClearMenus 清空菜单与面板（菜单置空；删除本程序创建的面板，手动的不动）。
func (b *Bot) ClearMenus(ctx context.Context) error {
	if _, err := b.SetMenu(ctx, Menu{Items: []MenuItem{}}); err != nil {
		return fmt.Errorf("清空菜单失败: %w", err)
	}
	for _, scope := range []string{"c2c", "group", "channel", "dm"} {
		entries, err := b.ListPanels(ctx, scope)
		if err != nil {
			return fmt.Errorf("查询面板失败(scope=%s): %w", scope, err)
		}
		for _, e := range entries {
			if e.Panel.Remark == AutoPanelRemark {
				if err := b.DeletePanel(ctx, e.PanelID); err != nil {
					return fmt.Errorf("删除面板 %s 失败: %w", e.PanelID, err)
				}
			}
		}
	}
	return nil
}
