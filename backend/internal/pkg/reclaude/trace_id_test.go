package reclaude

import (
	"regexp"
	"strings"
	"testing"
)

// 🔴 traceId 必须是**纯随机 16 字节 hex**，不能用 UUIDv4。
//
// 真客户端（📄 反编译 tunnel.newTraceID + ✅ 26 条真值抓包）：
//
//	crand.Read(16B) → hex → 按 8-4-4-4-12 拼接
//
// 它**不设 version/variant 位**。而 uuid.NewString() 产出的 v4 第 14 位恒为 '4'、
// 第 19 位恒 ∈ {8,9,a,b}。
//
// 可观测性是决定性的：26 条真实样本里只有 1 条偶然符合 v4 形态
// （纯随机期望 26/64 ≈ 0.4，完全吻合），而我们是 100%。服务端一条正则
// `t[14]=='4' && t[19] in [89ab]` 就能把我们从真客户端里筛出来。
//
// 2026-09-24 排查四台设备连续被撤销时找到的最强候选成因之一。
func TestNewTraceID(t *testing.T) {
	shape := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

	t.Run("形状是 8-4-4-4-12 的小写 hex", func(t *testing.T) {
		for range 50 {
			if got := NewTraceID(); !shape.MatchString(got) {
				t.Fatalf("形状不对: %q", got)
			}
		}
	})

	t.Run("不设 version/variant 位", func(t *testing.T) {
		const n = 500
		versionChars := map[byte]int{}
		v4 := 0
		for range n {
			id := NewTraceID()
			versionChars[id[14]]++
			if id[14] == '4' && strings.ContainsRune("89ab", rune(id[19])) {
				v4++
			}
		}
		if len(versionChars) < 8 {
			t.Fatalf("第 14 位只出现 %d 种字符（%v）—— 像是设了 version 位", len(versionChars), versionChars)
		}
		if v4 > n/16 {
			t.Fatalf("v4 形态命中 %d/%d，远高于纯随机期望 %d —— 没有用纯随机", v4, n, n/64)
		}
	})

	t.Run("每次都不同", func(t *testing.T) {
		seen := map[string]bool{}
		for range 200 {
			id := NewTraceID()
			if seen[id] {
				t.Fatalf("重复的 traceId: %s", id)
			}
			seen[id] = true
		}
	})
}
