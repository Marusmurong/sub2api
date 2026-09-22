//go:build unit

package repository

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// upstream_trace_id 是身份收敛之后唯一的排障锚点：统一 user_id / session_id 以后，
// 拿着上游报错里的 traceId 反查「是哪个下游客户触发的」只剩这一条路。
//
// 它挂在 usage_logs 上（每请求一条，天然一对一），因此必须与手写 INSERT 的
// 参数表、8 条列清单、静态占位符、扫描列顺序全部对齐 —— 错位的后果不是报错，
// 而是整行计费数据串位。
func TestPrepareUsageLogInsert_UpstreamTraceIDArgWiring(t *testing.T) {
	traceID := "9f2c1e40-2b6a-4d1e-9a77-2c9a0b1d3e4f"
	prepared := prepareUsageLogInsert(&service.UsageLog{
		UserID:          1,
		APIKeyID:        2,
		RequestID:       "client:trace",
		Model:           "claude-sonnet-4-5",
		UpstreamTraceID: &traceID,
		CreatedAt:       time.Now().UTC(),
	})
	require.Len(t, prepared.args, len(usageLogInsertArgTypes))

	idx := usageLogInsertArgIndex(t, "upstream_trace_id")
	arg, ok := prepared.args[idx].(sql.NullString)
	require.True(t, ok, "upstream_trace_id arg should be sql.NullString, got %T", prepared.args[idx])
	require.True(t, arg.Valid)
	require.Equal(t, traceID, arg.String)
	require.Equal(t, "text", usageLogInsertArgTypes[idx])
}

func TestPrepareUsageLogInsert_UpstreamTraceIDAbsentIsNull(t *testing.T) {
	// 非 reclaude 链路恒为空。落空串而不是 NULL 会让 partial index 收下每一行，
	// 而那个索引的前提正是「绝大多数行为 NULL」。
	prepared := prepareUsageLogInsert(&service.UsageLog{
		UserID: 1, APIKeyID: 2, RequestID: "client:absent", Model: "gpt-5",
		CreatedAt: time.Now().UTC(),
	})

	arg, ok := prepared.args[usageLogInsertArgIndex(t, "upstream_trace_id")].(sql.NullString)
	require.True(t, ok)
	require.False(t, arg.Valid, "absent upstream trace id must be NULL")
}

func TestUsageLogSelectColumnsIncludesUpstreamTraceID(t *testing.T) {
	require.Contains(t, usageLogSelectColumns, "upstream_trace_id")
}

// 结构性护栏：每一条手写的列清单都必须与列表逐条一致。
//
// 这个文件里有 8 处列清单（4 条 INSERT + CTE 的 input/SELECT 段），它们是手写的
// SQL 字符串，编译器看不见。加列时漏掉任何一处，参数就会整体错位一格 ——
// 后果不是报错，而是**每一行计费数据串位**，且只在集成测试或生产才暴露。
func TestUsageLogInsertColumnListsMatchArgTypes(t *testing.T) {
	source, err := os.ReadFile(filepath.Clean("usage_log_repo_insert.go"))
	require.NoError(t, err)

	// 每条清单都以 user_id 起、created_at 止。
	blocks := regexp.MustCompile(`(?s)\n\t+user_id,\n.*?\n\t+created_at\n`).FindAllString(string(source), -1)
	require.Len(t, blocks, 8, "column list count changed; update this guard together with the new call site")

	for i, block := range blocks {
		var columns []string
		for _, line := range strings.Split(block, "\n") {
			name := strings.TrimSuffix(strings.TrimSpace(line), ",")
			if name != "" {
				columns = append(columns, name)
			}
		}
		require.Equalf(t, usageLogInsertArgNames, columns,
			"column list #%d drifted from usageLogInsertColumns", i+1)
	}
}

// 静态占位符条数必须等于列数。CTE 路径的占位符是生成的，这两处是手写的。
func TestUsageLogStaticPlaceholderCountMatchesColumns(t *testing.T) {
	source, err := os.ReadFile(filepath.Clean("usage_log_repo_insert.go"))
	require.NoError(t, err)

	blocks := regexp.MustCompile(`(?s)\) VALUES \(\n(.*?)\n\t+\)`).FindAllStringSubmatch(string(source), -1)
	require.Len(t, blocks, 2)

	for i, block := range blocks {
		placeholders := regexp.MustCompile(`\$\d+`).FindAllString(block[1], -1)
		require.Lenf(t, placeholders, len(usageLogInsertColumns),
			"static INSERT #%d placeholder count drifted from usageLogInsertColumns", i+1)
	}
}
