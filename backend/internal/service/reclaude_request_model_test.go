//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReclaudeRequestModel(t *testing.T) {
	t.Run("读出 model", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages",
			strings.NewReader(`{"model":"claude-opus-5-5[1m]","max_tokens":10}`))
		require.NoError(t, err)

		require.Equal(t, "claude-opus-5-5[1m]", reclaudeRequestModel(req))
	})

	// 🔴 这是这个函数真正的正确性标准：读完之后 body 必须与读之前完全一致。
	// 不还回去的话，每条推理都会变成空请求体发上去。
	t.Run("body 读后完整可用", func(t *testing.T) {
		payload := `{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}]}`
		req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages",
			strings.NewReader(payload))
		require.NoError(t, err)

		reclaudeRequestModel(req)

		after, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.Equal(t, payload, string(after))
	})

	t.Run("超过缓冲上限时 body 仍然完整", func(t *testing.T) {
		// 超大 prompt：前 64KB 被缓冲，剩余部分必须还能读到。
		big := `{"model":"claude-opus-5","pad":"` +
			strings.Repeat("x", reclaudeRequestModelMaxBytes) + `"}`
		req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages",
			strings.NewReader(big))
		require.NoError(t, err)

		reclaudeRequestModel(req)

		after, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.Equal(t, big, string(after), "截断点之后的字节不能丢")
	})

	t.Run("保留原 Body 的 Close —— 否则每条请求泄漏一个连接", func(t *testing.T) {
		closed := false
		tracked := &closeTrackingBody{Reader: strings.NewReader(`{"model":"m"}`), closed: &closed}
		req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
		require.NoError(t, err)
		req.Body = tracked

		reclaudeRequestModel(req)
		require.NoError(t, req.Body.Close())

		require.True(t, closed, "原始 Body 的 Close 必须被调用")
	})

	t.Run("非 JSON 体不 panic 且 body 不受损", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages",
			strings.NewReader("not json at all"))
		require.NoError(t, err)

		require.Empty(t, reclaudeRequestModel(req))

		after, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.Equal(t, "not json at all", string(after))
	})

	t.Run("nil 请求与空 body 不 panic", func(t *testing.T) {
		require.NotPanics(t, func() {
			require.Empty(t, reclaudeRequestModel(nil))
			require.Empty(t, reclaudeRequestModel(&http.Request{}))
		})
	})
}
