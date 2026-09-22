package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/reclaude"
)

func TestReclaudeTraceCapture_ReachesForwardResult(t *testing.T) {
	t.Run("转发时把 traceId 写进捕获器", func(t *testing.T) {
		account, cipher := reclaudeTestAccount(t)
		upstream := NewReclaudeUpstream(
			&capturingUpstream{response: envelopeOKResponse(t)}, cipher, nil)

		ctx, capture := WithReclaudeTraceCapture(context.Background())
		req := innerRequest(t, "{}").WithContext(ctx)

		resp, err := upstream.Do(req, account, "")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		require.NotEmpty(t, capture.TraceID())
	})

	t.Run("非 reclaude 链路捕获器为空", func(t *testing.T) {
		_, capture := WithReclaudeTraceCapture(context.Background())

		require.Empty(t, capture.TraceID())
	})
}

func TestBuildUsageLogCarriesUpstreamTraceID(t *testing.T) {
	t.Run("有 traceId 时落到 usage log", func(t *testing.T) {
		// 🔴 少了这一步，traceId 生成了却没人存 —— 身份收敛之后唯一的排障锚点
		// 就是个内存里转瞬即逝的字符串。
		got := reclaudeUsageTraceIDPtr(&ForwardResult{UpstreamTraceID: "trace-abc"})

		require.NotNil(t, got)
		require.Equal(t, "trace-abc", *got)
	})

	t.Run("非 reclaude 链路落 NULL 而不是空串", func(t *testing.T) {
		// 空串会让 partial index 收下每一行，而那个索引的前提正是绝大多数行为 NULL。
		require.Nil(t, reclaudeUsageTraceIDPtr(&ForwardResult{}))
		require.Nil(t, reclaudeUsageTraceIDPtr(&ForwardResult{UpstreamTraceID: "   "}))
		require.Nil(t, reclaudeUsageTraceIDPtr(nil))
	})
}

func envelopeOKResponse(t *testing.T) *http.Response {
	t.Helper()
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body: io.NopCloser(bytes.NewReader(
			envelopeResponse(t, 200, reclaude.GatewayResponseMetadata{}, "{}"))),
	}
}

// 结构性护栏：Forward 必须在 ctx 上装 traceId 捕获器，并把结果写回 ForwardResult。
//
// 这两步是对称的：只装捕获器 = traceId 采到了没人取；只写回 = 永远写空值。
// 漏掉任何一半都**不会报错**，只会让排障锚点静默失效 —— 而它失效的时候，
// 恰恰是上游报错、最需要它的时候。
func TestForwardInstallsReclaudeTraceCapture(t *testing.T) {
	source, err := os.ReadFile(filepath.Clean("gateway_forward.go"))
	require.NoError(t, err)

	body := string(source)
	require.Contains(t, body, "WithReclaudeTraceCapture(ctx)",
		"Forward 必须在 ctx 上装 traceId 捕获器")
	require.Contains(t, body, "UpstreamTraceID = ",
		"捕获到的 traceId 必须写回 ForwardResult")
}
