//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReclaudeEdgeLabel(t *testing.T) {
	t.Run("主域不在表里，回 unknown", func(t *testing.T) {
		// ✅ 26/26 条真实信封的取值。这是我们当前实际打的 host。
		require.Equal(t, "unknown", reclaudeEdgeLabel("https://www.reclaude.ai"))
	})

	t.Run("route 节点命中，回 host", func(t *testing.T) {
		// ✅ 真客户端 telemetry-pending.json 的实测取值。
		require.Equal(t, "asia.route.reclaude.ai",
			reclaudeEdgeLabel("https://asia.route.reclaude.ai"))
	})

	t.Run("带端口时比较的是含端口的 Host", func(t *testing.T) {
		// 真实现比的是 u.Host 不是 u.Hostname()，带端口就不该命中。
		require.Equal(t, "unknown", reclaudeEdgeLabel("https://asia.route.reclaude.ai:8443"))
	})

	t.Run("空串与非法 URL 回 unknown 而不是 panic", func(t *testing.T) {
		require.Equal(t, "unknown", reclaudeEdgeLabel(""))
		require.Equal(t, "unknown", reclaudeEdgeLabel("://"))
		require.Equal(t, "unknown", reclaudeEdgeLabel("not-a-url"))
	})

	t.Run("本地 sink 回 unknown", func(t *testing.T) {
		// 抓包时 daemon 指向 sink，这解释了 26/26 的 unknown 从何而来。
		require.Equal(t, "unknown", reclaudeEdgeLabel("https://127.0.0.1:9443"))
	})
}
