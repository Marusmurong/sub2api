package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type stubReclaudeProbe struct {
	resp *http.Response
	err  error
}

func (s *stubReclaudeProbe) ProbeReclaudeEndpoint(context.Context, *Account, string) (*http.Response, error) {
	return s.resp, s.err
}

// 🔴 非 2xx 时必须带上响应体。
//
// 只报 "status 400" 等于没报：400 意味着对方**明确说了原因**（设备被删、
// SK 失效、客户端版本不受支持…），而这几种的处置完全不同。丢掉正文就
// 只能靠猜，这正是此前排查代理问题时踩过的坑（见 describeProxyFailure）。
func TestReclaudeSelfCheckSurfacesErrorBody(t *testing.T) {
	newChecker := func(status int, body string) *ReclaudeSelfChecker {
		return &ReclaudeSelfChecker{probe: &stubReclaudeProbe{resp: &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
		}}}
	}
	account := &Account{ID: 1, Type: AccountTypeReclaude, Platform: PlatformAnthropic}

	t.Run("400 的响应体要出现在详情里", func(t *testing.T) {
		detail := newChecker(400, `{"error":"device not found"}`).
			checkEndpoint(context.Background(), account, "/client/account", nil)

		require.Contains(t, detail, "400")
		require.Contains(t, detail, "device not found", "丢掉正文就只能靠猜")
	})

	t.Run("超长正文被截断，不把整个页面灌进日志", func(t *testing.T) {
		detail := newChecker(500, strings.Repeat("x", 5000)).
			checkEndpoint(context.Background(), account, "/client/account", nil)

		require.Less(t, len(detail), 1000)
	})

	t.Run("空正文时只报状态码，不产生误导性空引号", func(t *testing.T) {
		detail := newChecker(503, "").
			checkEndpoint(context.Background(), account, "/client/account", nil)

		require.Equal(t, "status 503", detail)
	})

	t.Run("2xx 仍然返回空串", func(t *testing.T) {
		detail := newChecker(200, `{"user_email":"a@b.c"}`).
			checkEndpoint(context.Background(), account, "/client/account", nil)

		require.Empty(t, detail)
	})
}
