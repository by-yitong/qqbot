package bot

// 自定义菜单与指令面板（官方 2026 新增，botgo v0.2.1 未覆盖，自行 HTTP 调用）。
//
//   - 自定义菜单：C2C 单聊窗口底部按钮栏，PUT /v2/menu 覆盖式设置，对所有用户生效。
//     按钮类型 send_message（点击把文本填入输入框，用户仍需按发送）/ link / menu（折叠）。
//   - 指令面板：按场景（c2c/group/channel/dm）展示指令/链接面板，POST /v2/panels 创建。
//
// 文档：https://bot.q.qq.com/wiki/develop/api-v2/server-inter/menu-panel/

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/tidwall/gjson"
)

// AutoPanelRemark 是本程序自动创建面板的 remark 标记：重新发布时按它识别并删除旧面板
// （面板是追加式的，不删会堆积到 20 个上限）。
const AutoPanelRemark = "qqbot-auto"

// --- 自定义菜单（PUT /v2/menu）---

// Menu 是全局自定义菜单（C2C 场景）。
type Menu struct {
	// 不用 omitempty：清空菜单需要显式发 {"items":[]}（omitempty 会把空列表序列化没，
	// PUT {} 语义不明）。
	Items []MenuItem `json:"items"` // 最多 10 个，按顺序从左到右展示
}

// MenuItem 是一级菜单项。Type：switch / send_message / link / menu（折叠子菜单）。
// 名称最多 10 字符（中文按 2 计）。
type MenuItem struct {
	Name         string        `json:"name,omitempty"`
	Type         string        `json:"type,omitempty"`
	SubMenuItems []SubMenuItem `json:"sub_menu_items,omitempty"` // 仅 type=menu，最多 5 个
	SendMessage  string        `json:"send_message,omitempty"`   // 仅 send_message：点击后填入输入框
	Link         string        `json:"link,omitempty"`           // 仅 link：必须 https
}

// SubMenuItem 是二级菜单项。Type：send_message / link（不支持再折叠）。
type SubMenuItem struct {
	Name        string `json:"name,omitempty"` // 最多 14 字符（约 7 个中文）
	Type        string `json:"type,omitempty"`
	SendMessage string `json:"send_message,omitempty"`
	Link        string `json:"link,omitempty"`
}

// --- 指令面板（POST /v2/panels）---

// PanelReq 是创建指令面板的请求。Scope：c2c/group/channel/dm；
// TargetType：all（全局）/ specific（仅 c2c、group 支持指定用户/群）。
type PanelReq struct {
	Scope        string   `json:"scope"`
	TargetType   string   `json:"target_type,omitempty"`
	UserOpenIDs  []string `json:"user_openids,omitempty"`  // c2c + specific
	GroupOpenIDs []string `json:"group_openids,omitempty"` // group + specific
	Panel        Panel    `json:"panel"`
}

// Panel 是面板配置（元素最多 20 个）。
type Panel struct {
	Items  []PanelItem `json:"items,omitempty"`
	Remark string      `json:"remark,omitempty"` // 开发者备注，不对用户展示
}

// PanelItem 是面板元素。Type：command（点击把 name 填入输入框）/ link。
// Name 最多 14 字符，Desc 最多 30 字符。
type PanelItem struct {
	Name      string `json:"name,omitempty"`
	Desc      string `json:"desc,omitempty"`
	Type      string `json:"type,omitempty"`
	OnlyAdmin bool   `json:"only_admin,omitempty"`
	Link      string `json:"link,omitempty"` // 仅 link：点击在浏览器打开
}

// PanelEntry 是面板列表查询结果（GET /v2/panels 的 records 元素）。
type PanelEntry struct {
	PanelID    string `json:"panel_id"`
	Scope      string `json:"scope"`
	TargetType string `json:"target_type"`
	Panel      Panel  `json:"panel"`
}

// --- API 调用 ---

// qqAPI 调一个 QQ OpenAPI 端点（JSON in/out，QQBot 鉴权）。状态码 >=400 返回错误。
func (b *Bot) qqAPI(ctx context.Context, method, path string, payload any) ([]byte, error) {
	if b.tokenSource == nil {
		return nil, ErrNotConnected
	}
	tok, err := b.tokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("取 token 失败: %w", err)
	}
	var rdr io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, qqAPIBase+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "QQBot "+tok.AccessToken)
	req.Header.Set("X-Union-Appid", b.appID)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 300))
	}
	return body, nil
}

// menuReq 是 /v2/menu 的请求/响应外层：配置包在 menu 字段里（真机验证：直接发
// {"items":[...]} 会报 40030016 必填字段缺失）。
type menuReq struct {
	Menu    Menu `json:"menu"`
	Version int  `json:"version,omitempty"`
}

// SetMenu 覆盖式设置全局自定义菜单，返回新版本号。
func (b *Bot) SetMenu(ctx context.Context, m Menu) (int, error) {
	body, err := b.qqAPI(ctx, http.MethodPut, "/v2/menu", menuReq{Menu: m})
	if err != nil {
		return 0, err
	}
	return int(gjson.GetBytes(body, "version").Int()), nil
}

// GetMenu 查询当前全局自定义菜单。
func (b *Bot) GetMenu(ctx context.Context) (Menu, error) {
	body, err := b.qqAPI(ctx, http.MethodGet, "/v2/menu", nil)
	if err != nil {
		return Menu{}, err
	}
	var req menuReq
	if err := json.Unmarshal(body, &req); err != nil {
		return Menu{}, err
	}
	return req.Menu, nil
}

// CreatePanel 创建指令面板，返回 panel_id。
func (b *Bot) CreatePanel(ctx context.Context, p PanelReq) (string, error) {
	body, err := b.qqAPI(ctx, http.MethodPost, "/v2/panels", p)
	if err != nil {
		return "", err
	}
	id := gjson.GetBytes(body, "panel_id").String()
	if id == "" {
		return "", fmt.Errorf("响应缺少 panel_id: %s", truncateStr(string(body), 300))
	}
	return id, nil
}

// ListPanels 查询某场景的面板列表（自动翻页）。
func (b *Bot) ListPanels(ctx context.Context, scope string) ([]PanelEntry, error) {
	var out []PanelEntry
	cursor := ""
	for {
		path := "/v2/panels?scope=" + scope + "&limit=50"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		body, err := b.qqAPI(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Records    []PanelEntry `json:"records"`
			NextCursor string       `json:"next_cursor"`
			IsEnd      bool         `json:"is_end"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Records...)
		if page.IsEnd || page.NextCursor == "" || len(page.Records) == 0 {
			return out, nil
		}
		cursor = page.NextCursor
	}
}

// DeletePanel 删除一个指令面板。
func (b *Bot) DeletePanel(ctx context.Context, panelID string) error {
	_, err := b.qqAPI(ctx, http.MethodDelete, "/v2/panels/"+panelID, nil)
	return err
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
// OwnerOnly 命令不进面板/菜单——面板对所有用户可见，主人命令不该出现在那里。
// 在发布时（OnStart / .menu apply）调用，保证所有命令都已注册完毕。
func (b *Bot) AutoMenuConfig() MenuConfig {
	b.mu.RLock()
	defer b.mu.RUnlock()
	cfg := MenuConfig{}
	panel := Panel{Remark: AutoPanelRemark}
	for _, c := range b.cmds {
		if c.hidden || c.hideBotMenu || c.requireAt || c.ownerOnly {
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
