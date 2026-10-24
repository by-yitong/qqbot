package dto

// 自定义菜单与指令面板 dto（官方 2026 新增，本地 fork 补充）。
//
//   - 自定义菜单：C2C 单聊窗口底部按钮栏，PUT /v2/menu 覆盖式设置，对所有用户生效。
//   - 指令面板：按场景（c2c/group/channel/dm）展示指令/链接面板，POST /v2/panels 创建。
//
// 文档：https://bot.q.qq.com/wiki/develop/api-v2/server-inter/menu-panel/

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

// SetMenuResult 设置菜单的响应。
type SetMenuResult struct {
	Version int `json:"version"`
}

// PanelReq 创建指令面板的请求。Scope：c2c/group/channel/dm；
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

// CreatePanelResult 创建面板的响应。
type CreatePanelResult struct {
	PanelID string `json:"panel_id"`
}

// PanelEntry 是面板列表查询结果（GET /v2/panels 的 records 元素）。
type PanelEntry struct {
	PanelID    string `json:"panel_id"`
	Scope      string `json:"scope"`
	TargetType string `json:"target_type"`
	Panel      Panel  `json:"panel"`
}

// PanelPage 面板列表单页。
type PanelPage struct {
	Records    []PanelEntry `json:"records"`
	NextCursor string       `json:"next_cursor"`
	IsEnd      bool         `json:"is_end"`
}
