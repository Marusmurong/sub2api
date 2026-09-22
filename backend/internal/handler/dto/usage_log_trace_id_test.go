//go:build unit

package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestUsageLogTraceIDIsAdminOnly(t *testing.T) {
	traceID := "trace-abc"
	log := &service.UsageLog{ID: 1, UpstreamTraceID: &traceID}

	t.Run("管理端可见", func(t *testing.T) {
		// 排障入口是「拿着上游报错里的 traceId 反查是谁触发的」，
		// 这个反查发生在管理端。
		admin := UsageLogFromServiceAdmin(log)

		require.NotNil(t, admin.UpstreamTraceID)
		require.Equal(t, traceID, *admin.UpstreamTraceID)
	})

	t.Run("普通用户侧不含该字段", func(t *testing.T) {
		// 🔴 与 upstream_request_id 同级：它是上游侧的内部标识，
		// 泄漏给终端用户没有价值，只会暴露我们走了中转。
		user := UsageLogFromService(log)

		encoded, err := json.Marshal(user)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "upstream_trace_id")
	})
}
