//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/reclaude"
	"github.com/stretchr/testify/require"
)

// E2-L2（RECLAUDE_REVOCATION_VERIFY_PLAN_2026-09-26 §4 L2）：头形状逐字对齐 09-26 真值。
//
// 真值（docs/captures/reclaude-live-2026-09-26/reccap）：
//   - mcp-registry 内层**没有** authorization，也没有 content-type
//   - 所有 GET 都没有 content-length
//   - profile 带 cache-control: no-cache
//
// sub 09-26 实发（sub2api-env）：mcp-registry 带了 auth 与 content-type，所有 GET
// 带 content-length: 0，profile 没有 cache-control。这些是每条请求都能看出来的差异。

func doSyntheticThroughUpstream(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	account, cipher := reclaudeTestAccount(t)
	upstream := &capturingUpstream{response: &http.Response{
		StatusCode: 200, Header: http.Header{},
		Body: io.NopCloser(bytes.NewReader(envelopeResponse(t, 200, reclaude.GatewayResponseMetadata{}, "{}"))),
	}}
	sut := NewReclaudeUpstream(upstream, cipher, nil)

	resp, err := sut.Do(req, account, "http://proxy:1080")
	require.NoError(t, err)
	_ = resp.Body.Close()

	meta := extractReclaudeEnvelopeMetaForTest(t, upstream.gotBody)
	return meta["headers"].(map[string]any)
}

func TestReclaudeUpstreamDoRespectsSyntheticNoAuth(t *testing.T) {
	t.Run("合成请求未带 authorization 时 Do 不补", func(t *testing.T) {
		req, err := http.NewRequestWithContext(WithReclaudeSynthetic(context.Background()),
			http.MethodGet, "https://api.anthropic.com/mcp-registry/v0/servers?version=latest", nil)
		require.NoError(t, err)
		setHeaderRaw(req.Header, "user-agent", "claude-cli/2.1.282 (external, cli)")

		headers := doSyntheticThroughUpstream(t, req)

		require.NotContains(t, headers, "authorization",
			"真值 mcp-registry 无 auth;Do 无条件覆写把 NeedsAuthorization=false 盖掉了")
	})

	t.Run("合成请求已带 authorization 时保留", func(t *testing.T) {
		req, err := http.NewRequestWithContext(WithReclaudeSynthetic(context.Background()),
			http.MethodGet, "https://api.anthropic.com/api/claude_code_grove", nil)
		require.NoError(t, err)
		setHeaderRaw(req.Header, "authorization", "Bearer sk-rec-abcdef")

		headers := doSyntheticThroughUpstream(t, req)

		require.Equal(t, "Bearer sk-rec-abcdef", headers["authorization"])
	})

	t.Run("推理请求缺 authorization 时照常补 SK", func(t *testing.T) {
		account, cipher := reclaudeTestAccount(t)
		upstream := &capturingUpstream{response: &http.Response{
			StatusCode: 200, Header: http.Header{},
			Body: io.NopCloser(bytes.NewReader(envelopeResponse(t, 200, reclaude.GatewayResponseMetadata{}, "{}"))),
		}}
		sut := NewReclaudeUpstream(upstream, cipher, nil)

		resp, err := sut.Do(innerRequest(t, `{"model":"claude"}`), account, "http://proxy:1080")
		require.NoError(t, err)
		_ = resp.Body.Close()

		headers := extractReclaudeEnvelopeMetaForTest(t, upstream.gotBody)["headers"].(map[string]any)
		require.Equal(t, "Bearer sk-rec-abcdef", headers["authorization"])
	})
}

func TestBuildReclaudeEnvelopeContentLengthOnlyWithBody(t *testing.T) {
	t.Run("GET 无 body 不写 content-length", func(t *testing.T) {
		req, err := http.NewRequestWithContext(WithReclaudeSynthetic(context.Background()),
			http.MethodGet, "https://api.anthropic.com/api/claude_code_penguin_mode", nil)
		require.NoError(t, err)

		envelope, _, err := buildReclaudeEnvelope(req, "https://www.reclaude.ai", nil)
		require.NoError(t, err)

		headers := extractReclaudeEnvelopeMetaForTest(t, envelope)["headers"].(map[string]any)
		require.NotContains(t, headers, "content-length",
			"真值 penguin / grove / settings / mcp-registry 全部没有 content-length")
	})

	t.Run("POST 有 body 写实际长度", func(t *testing.T) {
		req := innerRequest(t, `{"model":"claude"}`)

		envelope, _, err := buildReclaudeEnvelope(req, "https://www.reclaude.ai", nil)
		require.NoError(t, err)

		headers := extractReclaudeEnvelopeMetaForTest(t, envelope)["headers"].(map[string]any)
		require.Equal(t, "18", headers["content-length"])
	})
}
