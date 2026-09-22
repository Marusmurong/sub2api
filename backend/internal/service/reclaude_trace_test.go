package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/reclaude"
)

// traceId 在 §6.3 第 2 步生成后原本就被扔掉了，与「唯一排障锚点」完全没有衔接。
// 这组测试锁住它能从转发层传回上层落库。
func TestReclaudeTraceCapture(t *testing.T) {
	t.Run("转发时把 traceId 写进捕获器", func(t *testing.T) {
		// Arrange
		account, cipher := reclaudeTestAccount(t)
		upstream := &capturingUpstream{response: &http.Response{
			StatusCode: 200, Header: http.Header{},
			Body: io.NopCloser(bytes.NewReader(
				envelopeResponse(t, 200, reclaude.GatewayResponseMetadata{}, "ok"))),
		}}
		sut := NewReclaudeUpstream(upstream, cipher, nil)

		ctx, capture := WithReclaudeTraceCapture(context.Background())
		inner := innerRequest(t, "{}")

		// Act
		resp, err := sut.Do(inner.WithContext(ctx), account, "")

		// Assert
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.NotEmpty(t, capture.TraceID())
	})

	t.Run("捕获到的 traceId 与信封里的一致", func(t *testing.T) {
		account, cipher := reclaudeTestAccount(t)
		upstream := &capturingUpstream{response: &http.Response{
			StatusCode: 200, Header: http.Header{},
			Body: io.NopCloser(bytes.NewReader(
				envelopeResponse(t, 200, reclaude.GatewayResponseMetadata{}, ""))),
		}}
		sut := NewReclaudeUpstream(upstream, cipher, nil)
		ctx, capture := WithReclaudeTraceCapture(context.Background())

		resp, err := sut.Do(innerRequest(t, "{}").WithContext(ctx), account, "")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		_, meta := decodeSentEnvelope(t, upstream.gotBody)
		require.Equal(t, meta.TraceID, capture.TraceID())
	})

	// 非 reclaude 链路不该被这个机制影响。
	t.Run("未挂捕获器时转发照常", func(t *testing.T) {
		account, cipher := reclaudeTestAccount(t)
		upstream := &capturingUpstream{response: &http.Response{
			StatusCode: 200, Header: http.Header{},
			Body: io.NopCloser(bytes.NewReader(
				envelopeResponse(t, 200, reclaude.GatewayResponseMetadata{}, ""))),
		}}
		sut := NewReclaudeUpstream(upstream, cipher, nil)

		resp, err := sut.Do(innerRequest(t, "{}"), account, "")

		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	})

	t.Run("零值捕获器读取安全", func(t *testing.T) {
		var capture *ReclaudeTraceCapture

		require.Empty(t, capture.TraceID())
	})

	// 一个 gin 请求可能触发多次上游调用（重试链路），捕获器会被并发写。
	t.Run("并发写入不竞争", func(t *testing.T) {
		_, capture := WithReclaudeTraceCapture(context.Background())

		var wg sync.WaitGroup
		for i := 0; i < 32; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				capture.set("trace")
				_ = capture.TraceID()
			}()
		}
		wg.Wait()

		require.Equal(t, "trace", capture.TraceID())
	})
}

// ForwardResult 是落库的载体；没有这个字段，traceId 到不了 usage_logs。
func TestForwardResultCarriesUpstreamTraceID(t *testing.T) {
	result := ForwardResult{UpstreamTraceID: "trace-abc"}

	require.Equal(t, "trace-abc", result.UpstreamTraceID)
}

// decodeSentEnvelope 解出出站信封的元数据段。
// 请求信封与响应信封共用线格式，但元数据结构不同，所以按请求结构解。
func decodeSentEnvelope(t *testing.T, sent []byte) ([]byte, reclaude.ClientRequestMetadata) {
	t.Helper()
	require.GreaterOrEqual(t, len(sent), reclaude.EnvelopeLengthPrefixBytes)

	metaLen := int(binary.BigEndian.Uint32(sent[:reclaude.EnvelopeLengthPrefixBytes]))
	metaEnd := reclaude.EnvelopeLengthPrefixBytes + metaLen
	require.LessOrEqual(t, metaEnd, len(sent))

	var request reclaude.ClientRequestMetadata
	require.NoError(t, json.Unmarshal(sent[reclaude.EnvelopeLengthPrefixBytes:metaEnd], &request))
	return sent[metaEnd:], request
}
