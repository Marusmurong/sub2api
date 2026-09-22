package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// V-4 的 DB 兜底。应用层的 SELECT-then-INSERT 在并发建号时必漏，
// 漏了的后果是同一台设备被当成两个池子调度，并发翻倍打穿包络。
func TestReclaudeDeviceIDUniqueMigration(t *testing.T) {
	content, err := FS.ReadFile("239_reclaude_device_id_unique.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")

	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS accounts_reclaude_device_id_unique_active")
	require.Contains(t, sql, "ON accounts ((credentials ->> 'reclaude_device_id'))")

	t.Run("只约束 reclaude 类型", func(t *testing.T) {
		require.Contains(t, sql, "WHERE type = 'reclaude'")
	})

	t.Run("软删除不占用唯一位置", func(t *testing.T) {
		require.Contains(t, sql, "deleted_at IS NULL")
	})

	t.Run("忽略没有该键的行", func(t *testing.T) {
		require.Contains(t, sql, "credentials ->> 'reclaude_device_id' IS NOT NULL")
	})
}
