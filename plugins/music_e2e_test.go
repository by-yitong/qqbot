//go:build music_e2e

// 真实 itapi 接口的端到端验证（默认不跑，联网且依赖上游可用）：
//
//	go test ./plugins/ -run TestMusicE2E -tags music_e2e -v
//
// 用于上游接口漂移的排查；CI/常规 go test 不受影响。
package plugins

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMusicE2E(t *testing.T) {
	p := NewMusic()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	src := func(key string) *musicSource {
		for _, s := range p.sources {
			if s.Key == key {
				return s
			}
		}
		t.Fatalf("音源 %s 未注册", key)
		return nil
	}

	for _, key := range []string{"5sign", "qq", "kuwo", "kuwohq", "qishui"} {
		s := src(key)
		entries, err := s.search(ctx, "天下")
		if err != nil {
			t.Errorf("[%s] 搜索失败: %v", key, err)
			continue
		}
		if len(entries) == 0 {
			t.Errorf("[%s] 搜索无结果", key)
			continue
		}
		t.Logf("[%s] 搜索 %d 条: 首条 %s - %s", key, len(entries), entries[0].Title, entries[0].Singer)
		// QQ 音乐首条常为付费曲（flac），取链仍应成功；其余源取第 1 条。
		track, err := s.pick(ctx, "天下", entries[0].N)
		if err != nil {
			t.Errorf("[%s] 取链失败: %v", key, err)
			continue
		}
		t.Logf("[%s] 取链: %s - %s %s %s", key, track.Title, track.Singer, track.Format, musicTrunc(track.AudioURL, 60))
	}

	// netease 2026-09 上游空响应：允许失败，但错误须可读（引导换音源）。
	if _, err := src("netease").search(ctx, "天下"); err != nil {
		if !strings.Contains(err.Error(), "无响应") {
			t.Errorf("[netease] 错误信息不含「无响应」: %v", err)
		} else {
			t.Log("[netease] 上游仍空响应（预期内）")
		}
	} else {
		t.Log("[netease] 上游已恢复")
	}
}
