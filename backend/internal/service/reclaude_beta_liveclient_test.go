//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 🔴 第八次撤销排查（2026-09-26）：同机真客户端存活抓包 12/12 真 /v1/messages 复验。
//
// reclaudeInnerBetaHeader 曾按 2.1.280 抓包校准，客户端升到 2.1.282 后漂移：
//   - 真客户端 2.1.282 恒发 advisor-tool-2026-03-01，我们缺
//   - 真客户端 2.1.282 非 1M 请求恒不发 context-1m-2025-08-07，我们多发
//
// 逐字段 diff（真客户端存活信封 vs sub2api 出站）确认这是唯一的 beta 差异。
// 本测试把 beta 钉死为**抓包实测的 2.1.282 逐序真值**，防止再漂。
func TestReclaudeInnerBetaMatchesLiveClient282(t *testing.T) {
	// ✅ 真值：reclaude-lab/sink/dump 2026-09-26 抓包，12/12 真 /v1/messages 逐字逐序一致。
	// fallback 相关 2 token 已按红线剔除（body.fallbacks 被 strip，见 §2.7）。
	wantOrder := []string{
		"claude-code-20250219",
		"oauth-2025-04-20",
		"interleaved-thinking-2025-05-14",
		"thinking-token-count-2026-05-13",
		"context-management-2025-06-27",
		"prompt-caching-scope-2026-01-05",
		"mid-conversation-system-2026-04-07",
		"per-turn-control-2026-07-01",
		"mid-conversation-tool-changes-2026-07-01",
		"advisor-tool-2026-03-01",
		"advanced-tool-use-2025-11-20",
		"mid-conversation-system-clear-at-2026-08-21",
		"effort-2025-11-24",
		"thinking-binding-controls-2026-08-01",
		"extended-cache-ttl-2025-04-11",
		"cache-diagnosis-2026-04-07",
	}

	got := strings.Split(reclaudeInnerBetaHeader, ",")

	require.Equal(t, wantOrder, got, "reclaude beta 必须逐序等于真客户端 2.1.282 抓包")
	require.Contains(t, got, "advisor-tool-2026-03-01", "真客户端 12/12 都发,不能缺")
	require.NotContains(t, got, "context-1m-2025-08-07", "真客户端非 1M 请求 0/12 发,不能多")
	require.NotContains(t, reclaudeInnerBetaHeader, "server-side-fallback", "红线:body 无 fallbacks")
	require.NotContains(t, reclaudeInnerBetaHeader, "fallback-credit", "红线:信用消费")
}
