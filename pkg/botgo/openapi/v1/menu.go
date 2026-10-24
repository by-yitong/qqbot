package v1

// 自定义菜单与指令面板接口（本地 fork 补充，官方 2026 新增）。
//
//   - 自定义菜单：C2C 单聊窗口底部按钮栏，PUT /v2/menu 覆盖式设置，对所有用户生效。
//   - 指令面板：按场景展示指令/链接面板，POST /v2/panels 创建（追加式，上限 20 个）。
//
// 文档：https://bot.q.qq.com/wiki/develop/api-v2/server-inter/menu-panel/

import (
	"context"
	"errors"

	"qqbot/pkg/botgo/dto"
)

// menuWrap 是 /v2/menu 的请求/响应外层：配置包在 menu 字段里（真机验证：直接发
// {"items":[...]} 会报 40030016 必填字段缺失）。
type menuWrap struct {
	Menu    dto.Menu `json:"menu"`
	Version int      `json:"version,omitempty"`
}

// SetMenu 覆盖式设置全局自定义菜单，返回新版本号。
func (o *openAPI) SetMenu(ctx context.Context, m dto.Menu) (int, error) {
	rsp, err := o.request(ctx).
		SetBody(menuWrap{Menu: m}).
		SetResult(menuWrap{}).
		Put(o.getURL(menuURI))
	if err != nil {
		return 0, err
	}
	return rsp.Result().(*menuWrap).Version, nil
}

// GetMenu 查询当前全局自定义菜单。
func (o *openAPI) GetMenu(ctx context.Context) (*dto.Menu, error) {
	rsp, err := o.request(ctx).
		SetResult(menuWrap{}).
		Get(o.getURL(menuURI))
	if err != nil {
		return nil, err
	}
	menu := rsp.Result().(*menuWrap).Menu
	return &menu, nil
}

// CreatePanel 创建指令面板，返回 panel_id。
func (o *openAPI) CreatePanel(ctx context.Context, p dto.PanelReq) (string, error) {
	rsp, err := o.request(ctx).
		SetBody(p).
		SetResult(dto.CreatePanelResult{}).
		Post(o.getURL(panelsURI))
	if err != nil {
		return "", err
	}
	id := rsp.Result().(*dto.CreatePanelResult).PanelID
	if id == "" {
		return "", errors.New("创建面板响应缺少 panel_id: " + truncateForLog(rsp.Body(), 300))
	}
	return id, nil
}

// ListPanels 查询某场景的面板列表（自动翻页聚合）。scope：c2c/group/channel/dm。
func (o *openAPI) ListPanels(ctx context.Context, scope string) ([]dto.PanelEntry, error) {
	var out []dto.PanelEntry
	cursor := ""
	for i := 0; i < maxGroupListPages; i++ {
		req := o.request(ctx).
			SetQueryParam("scope", scope).
			SetQueryParam("limit", "50").
			SetResult(dto.PanelPage{})
		if cursor != "" {
			req.SetQueryParam("cursor", cursor)
		}
		rsp, err := req.Get(o.getURL(panelsURI))
		if err != nil {
			return nil, err
		}
		page := rsp.Result().(*dto.PanelPage)
		out = append(out, page.Records...)
		if page.IsEnd || page.NextCursor == "" || len(page.Records) == 0 {
			return out, nil
		}
		cursor = page.NextCursor
	}
	return out, nil
}

// DeletePanel 删除一个指令面板。
func (o *openAPI) DeletePanel(ctx context.Context, panelID string) error {
	_, err := o.request(ctx).
		SetPathParam("panel_id", panelID).
		Delete(o.getURL(panelURI))
	return err
}
