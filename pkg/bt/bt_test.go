package bt

import (
	"os"
	"testing"
)

// 真机测试：需要环境变量 BT_PANEL_URL / BT_PANEL_KEY（.env，不入库），未配置则跳过。
func TestGetSystemTotal(t *testing.T) {
	u, k := os.Getenv("BT_PANEL_URL"), os.Getenv("BT_PANEL_KEY")
	if u == "" || k == "" {
		t.Skip("未配置 BT_PANEL_URL/BT_PANEL_KEY，跳过真机测试")
	}
	res, err := NewBtPanel(u, k).GetSystemTotal()
	if err != nil {
		t.Fatal(err.Error())
	}
	t.Log(res)
}
