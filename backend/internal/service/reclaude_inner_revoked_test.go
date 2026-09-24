//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/reclaude"
	"github.com/stretchr/testify/require"
)

type revokeRecordingEvents struct {
	statuses []int
}

func (e *revokeRecordingEvents) HandleReclaudeEvents(*Account, []reclaude.ReclaudeEvent) {}
func (e *revokeRecordingEvents) HandleReclaudeGatewayError(_ *Account, status int) {
	e.statuses = append(e.statuses, status)
}

// 🔴 2026-09-25 生产事故的回归闸门：设备被解绑时外层是 200，
// 400 在内层，账号因此从未被停止调度，带着作废凭据跑了 30 多分钟。
func TestDetectInnerCredentialRevoked(t *testing.T) {
	const revokedBody = `{"type":"error","error":{"type":"authentication_error","code":"device_revoked","message":"此设备已被解绑"}}`

	newResp := func(status int, body string) *http.Response {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
	}

	t.Run("内层 400 device_revoked 触发停号", func(t *testing.T) {
		events := &revokeRecordingEvents{}
		upstream := &ReclaudeUpstream{events: events}

		upstream.detectInnerCredentialRevoked(newResp(400, revokedBody), &Account{ID: 231})

		require.Equal(t, []int{http.StatusUnauthorized}, events.statuses,
			"必须映射成凭据失效，否则账号继续被调度")
	})

	t.Run("body 在检测后仍然完整", func(t *testing.T) {
		// 预读不还回去的话，下游拿到的是残缺错误体。
		events := &revokeRecordingEvents{}
		resp := newResp(400, revokedBody)

		(&ReclaudeUpstream{events: events}).detectInnerCredentialRevoked(resp, &Account{ID: 1})

		after, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, revokedBody, string(after))
	})

	t.Run("其它 400 不停号", func(t *testing.T) {
		// 状态改错的代价比漏报大：普通的请求级错误不该停整个账号。
		events := &revokeRecordingEvents{}
		upstream := &ReclaudeUpstream{events: events}

		upstream.detectInnerCredentialRevoked(
			newResp(400, `{"error":{"type":"invalid_request_error","message":"bad model"}}`),
			&Account{ID: 1})

		require.Empty(t, events.statuses)
	})

	t.Run("2xx 一个字节都不预读 —— 流式边界不能被打乱", func(t *testing.T) {
		events := &revokeRecordingEvents{}
		resp := newResp(200, "event: message_start\ndata: {}\n\n")

		(&ReclaudeUpstream{events: events}).detectInnerCredentialRevoked(resp, &Account{ID: 1})

		after, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, "event: message_start\ndata: {}\n\n", string(after))
		require.Empty(t, events.statuses)
	})

	t.Run("nil 依赖不 panic", func(t *testing.T) {
		require.NotPanics(t, func() {
			var nilUpstream *ReclaudeUpstream
			nilUpstream.detectInnerCredentialRevoked(nil, nil)
			(&ReclaudeUpstream{}).detectInnerCredentialRevoked(newResp(400, revokedBody), &Account{ID: 1})
		})
	})
}

func TestClassifyReclaudeGatewayBody(t *testing.T) {
	t.Run("400 带 device_revoked 判为凭据失效", func(t *testing.T) {
		// 🔴 对端把它放在 400 里，只看状态码会落进 default（只告警不停号）。
		require.Equal(t, ReclaudeActionCredentialRevoked,
			ClassifyReclaudeGatewayBody(400, []byte(`{"error":{"code":"device_revoked"}}`)))
	})

	t.Run("无 body 时退回状态码分类", func(t *testing.T) {
		require.Equal(t, ReclaudeActionCredentialRevoked, ClassifyReclaudeGatewayBody(401, nil))
		require.Equal(t, ReclaudeActionCooldown, ClassifyReclaudeGatewayBody(429, nil))
		require.Equal(t, ReclaudeActionUnknownEvent, ClassifyReclaudeGatewayBody(400, nil))
	})
}
