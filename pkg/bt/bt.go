package bt

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"qqbot/internal/utils"
	"strconv"
	"time"

	"github.com/tidwall/gjson"
	"resty.dev/v3"
)

type BtPanel struct {
	baseApi string
	apiKey  string

	client *resty.Client
}

func NewBtPanel(baseApi, apiKey string) *BtPanel {
	c := resty.New()
	c.SetTLSClientConfig(&tls.Config{
		InsecureSkipVerify: true,
	})
	return &BtPanel{
		baseApi: baseApi,
		apiKey:  apiKey,
		client:  c,
	}
}

// GetSystemTotal 获取系统基础统计
// {"memTotal": 7684, "memFree": 235, "memBuffers": 376, "memCached": 4290, "memAvailable": 4502, "memShared": 105, "memRealUsed": 2783, "memNewTotalList": ["7.5", "GB"], "memNewRealUsedList": ["2.7", "GB"], "memNewRealUsed": "2.7", "memNewTotal": "7.5GB", "cpuNum": 4, "cpuRealUsed": 4.4, "time": "14\u5c0f\u65f6", "system": "Debian GNU/Linux 12 (bookworm) x86_64(Py3.7.16)", "isuser": 0, "isport": false, "version": "11.8.1"}
func (b *BtPanel) GetSystemTotal() (*SystemTotalRes, error) {
	var res SystemTotalRes
	if _, err := b.fetch("/system?action=GetSystemTotal", nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (b *BtPanel) fetch(path string, data map[string]string, result any) (*resty.Response, error) {
	rtime, rtoken := generateRequestToken(b.apiKey)
	if data == nil {
		data = map[string]string{}
	}
	data["request_time"] = strconv.FormatInt(rtime, 10)
	data["request_token"] = string(rtoken)

	res, err := b.client.R().SetFormData(data).Post(b.baseApi + path)
	log.Println(path, "返回: ", res.String())
	if err != nil {
		return nil, err
	}

	j := gjson.Parse(res.String())
	// 宝塔接口成功时多数不带 status 字段（如 GetSystemTotal），只有失败才返回 status:false + msg。
	if status := j.Get("status"); status.Exists() && !status.Bool() {
		if msg := j.Get("msg"); msg.Exists() {
			return nil, fmt.Errorf("%s", msg.String())
		}
		return nil, fmt.Errorf("%s", res.String())
	}
	// 宝塔返回的 Content-Type 不是标准 application/json，resty 不会自动反序列化，必须手动解。
	if result != nil {
		if err := json.Unmarshal([]byte(res.String()), result); err != nil {
			return nil, fmt.Errorf("解析响应失败: %w", err)
		}
	}
	return res, nil
}

func generateRequestToken(apiSk string) (requestTime int64, requestToken string) {
	// 当前 Unix 时间戳（秒）
	requestTime = time.Now().Unix()
	// md5(api_sk)
	apiSkMD5 := utils.Md5String(apiSk)

	// md5(string(request_time) + md5(api_sk))
	requestToken = utils.Md5String(
		strconv.FormatInt(requestTime, 10) + apiSkMD5,
	)

	return
}
