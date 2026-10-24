//go:build zhihu_e2e

// 真实知乎知答网关的端到端验证（默认不跑，联网且依赖上游可用）：
//
//	go test ./plugins/ -run TestZhihuE2E -tags zhihu_e2e -v
//
// 密钥从环境变量 ZHIHU_API_KEY 取（本地可写在 .env）；未配置则跳过。
package plugins

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"

	"qqbot/internal/agent"
	"qqbot/internal/utils"
)

func TestZhihuE2E(t *testing.T) {
	key := os.Getenv("ZHIHU_API_KEY")
	if key == "" {
		_ = godotenv.Load() // 测试工作目录在 plugins/，.env 在仓库根
		_ = godotenv.Load("../.env")
		key = os.Getenv("ZHIHU_API_KEY")
	}
	if key == "" {
		t.Skip("未配置 ZHIHU_API_KEY，跳过")
	}
	p := NewZhihuPlugin(
		utils.EnvOr("ZHIHU_BASE_URL", zhihuDefaultBaseURL),
		key,
		utils.EnvOr("ZHIHU_MODEL", zhihuDefaultModel),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cases := []struct {
		name string
		q    string
		want []string
	}{
		{"热榜", "给我看知乎当前的热榜，按你的列表格式来", []string{"zhihu.com/question"}},
		{"搜索", "帮我搜索：Go goroutine GMP 调度，找几个高质量回答", []string{"zhihu.com"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := p.cli.Complete(ctx, zhihuSystemPrompt, []agent.Message{agent.NewUser(tc.q)}, nil)
			if err != nil {
				t.Fatalf("调用失败: %v", err)
			}
			if res.Content == "" {
				t.Fatal("空回复")
			}
			for _, w := range tc.want {
				if !strings.Contains(res.Content, w) {
					t.Errorf("回复应含 %q:\n%s", w, res.Content)
				}
			}
			t.Logf("回复:\n%s", res.Content)
		})
	}
}
