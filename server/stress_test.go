package main

import (
	"math/rand"
	"testing"
)

// TestMakeAgentUptimeHasRandomHours 验证：
// 不管 UptimeMin/Max 怎么设置（0=随机、固定值、范围值、min>max 自动翻转），
// makeAgent 生成的 stressAgent.uptime 都必须在 [天数]*86400 上额外叠加 [0,23]*3600 的随机小时，
// 而不是死死卡在整数天边界。
//
// 同时通过统计 300 次出现的小时种类数 ≥ 2 来证明小时是随机的
// （旧实现小时永远是 0 → 1 种 → 该断言会失败，从而拦截回归）。
func TestMakeAgentUptimeHasRandomHours(t *testing.T) {
	cases := []struct {
		name        string
		min, max    int
		expectDayLo int
		expectDayHi int
	}{
		{"随机默认(0,0)→1-400天", 0, 0, 1, 400},
		{"固定30天", 30, 30, 30, 30},
		{"范围10-50天", 10, 50, 10, 50},
		{"min>max自动翻转", 50, 10, 10, 50},
		{"单边界1天", 1, 1, 1, 1},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			seenHours := map[int64]struct{}{}
			for i := 0; i < 300; i++ {
				r := rand.New(rand.NewSource(int64(i + 1)))
				a := makeAgent(r, StressParams{
					UptimeMin: tc.min,
					UptimeMax: tc.max,
				})
				up := a.uptime
				if up <= 0 {
					t.Fatalf("uptime 非正: %d", up)
				}
				// 必须整小时对齐（3600 的倍数）
				if up%3600 != 0 {
					t.Fatalf("uptime 非整小时: %d (mod3600=%d)", up, up%3600)
				}
				days := up / 86400
				hours := (up % 86400) / 3600
				if days < int64(tc.expectDayLo) || days > int64(tc.expectDayHi) {
					t.Fatalf("天数 %d ∉ [%d,%d]", days, tc.expectDayLo, tc.expectDayHi)
				}
				if hours < 0 || hours > 23 {
					t.Fatalf("叠加小时 %d 越界 [0,23]", hours)
				}
				seenHours[hours] = struct{}{}
			}
			if len(seenHours) < 2 {
				t.Fatalf("300 次仅出现 %d 种小时值，未随机化（预期≥2，证明 0-23 随机生效），实际=%v",
					len(seenHours), seenHours)
			}
		})
	}
}
