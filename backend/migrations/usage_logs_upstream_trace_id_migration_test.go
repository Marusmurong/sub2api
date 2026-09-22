package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 统一身份之后 traceId 是唯一的排障锚点：拿着上游报错里的 traceId
// 反查是哪个下游客户触发的。没有它就只能对客户说「不知道」。
func TestUsageLogsUpstreamTraceIDMigration(t *testing.T) {
	content, err := FS.ReadFile("240_usage_logs_upstream_trace_id.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")

	require.Contains(t, sql, "ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS upstream_trace_id")

	t.Run("按 traceId 反查建索引", func(t *testing.T) {
		require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_usage_logs_upstream_trace_id")
		require.Contains(t, sql, "ON usage_logs (upstream_trace_id)")
	})

	// 绝大多数行该列为 NULL（只有 reclaude 链路会写），不必进索引。
	t.Run("只索引非空行", func(t *testing.T) {
		require.Contains(t, sql, "WHERE upstream_trace_id IS NOT NULL")
	})

	// 必须可空：其它账号类型恒为 NULL，加 NOT NULL 会让迁移在存量数据上失败。
	t.Run("列可空", func(t *testing.T) {
		require.NotContains(t, sql, "upstream_trace_id VARCHAR(64) NOT NULL")
	})
}
