package service

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/reclaude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// recordingDispatchUpstream 记录 DoWithTLS 的调用次数与入参，用于断言
// 「reclaude 账号绝不落到裸 DoWithTLS」。
type recordingDispatchUpstream struct {
	calls       int
	gotAccount  int64
	gotConcur   int
	gotProxyURL string
	gotProfile  *tlsfingerprint.Profile
}

func (u *recordingDispatchUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return nil, errors.New("Do should not be used by dispatchUpstream")
}

func (u *recordingDispatchUpstream) DoWithTLS(
	_ *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile,
) (*http.Response, error) {
	u.calls++
	u.gotProxyURL = proxyURL
	u.gotAccount = accountID
	u.gotConcur = accountConcurrency
	u.gotProfile = profile
	return &http.Response{StatusCode: http.StatusOK}, nil
}

func TestDispatchUpstream(t *testing.T) {
	newRequest := func(t *testing.T) *http.Request {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
		require.NoError(t, err)
		return req
	}

	t.Run("非 reclaude 账号原样委托给 DoWithTLS", func(t *testing.T) {
		// Arrange
		upstream := &recordingDispatchUpstream{}
		account := &Account{ID: 42, Concurrency: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
		profile := &tlsfingerprint.Profile{}

		// Act
		resp, err := dispatchUpstream(upstream, nil, newRequest(t), "http://proxy:1080", account, profile)

		// Assert
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, 1, upstream.calls)
		require.Equal(t, int64(42), upstream.gotAccount)
		require.Equal(t, 7, upstream.gotConcur)
		require.Equal(t, "http://proxy:1080", upstream.gotProxyURL)
		require.Same(t, profile, upstream.gotProfile)
	})

	// 这是最重要的一条断言：漏改一个调用点的后果是「带空 Bearer 把原始
	// Anthropic 请求直接打到 api.anthropic.com」——既泄漏内容又必然 401。
	t.Run("未接线时 reclaude 账号报错而不是退回 DoWithTLS", func(t *testing.T) {
		// Arrange
		upstream := &recordingDispatchUpstream{}
		account := &Account{ID: 43448, Platform: PlatformAnthropic, Type: AccountTypeReclaude}

		// Act
		resp, err := dispatchUpstream(upstream, nil, newRequest(t), "http://proxy:1080", account, nil)

		// Assert
		require.Error(t, err)
		require.ErrorIs(t, err, ErrReclaudeUpstreamNotWired)
		require.Nil(t, resp)
		require.Zero(t, upstream.calls, "reclaude 账号不得触达 httpUpstream.DoWithTLS")
	})

	// 接线之后同样不许落到裸 DoWithTLS —— 它必须走信封转发。
	t.Run("已接线时 reclaude 账号走信封转发", func(t *testing.T) {
		// Arrange
		account, cipher := reclaudeTestAccount(t)
		envelopeUpstream := &capturingUpstream{response: &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body: io.NopCloser(bytes.NewReader(
				envelopeResponse(t, 200, reclaude.GatewayResponseMetadata{}, "ok"))),
		}}
		bareUpstream := &recordingDispatchUpstream{}

		// Act
		resp, err := dispatchUpstream(
			bareUpstream, NewReclaudeUpstream(envelopeUpstream, cipher, nil),
			newRequest(t), "http://proxy:1080", account, &tlsfingerprint.Profile{},
		)

		// Assert
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Zero(t, bareUpstream.calls, "reclaude 账号不得触达裸 DoWithTLS")
		require.NotNil(t, envelopeUpstream.gotRequest)
		require.Equal(t, "https://la.route.reclaude.ai/proxy", envelopeUpstream.gotRequest.URL.String())
		require.Nil(t, envelopeUpstream.gotTLSProfile, "传进来的 Anthropic TLS profile 应当被丢弃")
	})

	t.Run("account 为空时报错且不触达上游", func(t *testing.T) {
		upstream := &recordingDispatchUpstream{}

		resp, err := dispatchUpstream(upstream, nil, newRequest(t), "", nil, nil)

		require.Error(t, err)
		require.Nil(t, resp)
		require.Zero(t, upstream.calls)
	})

	t.Run("上游未配置时报错而非 panic", func(t *testing.T) {
		account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

		resp, err := dispatchUpstream(nil, nil, newRequest(t), "", account, nil)

		require.Error(t, err)
		require.Nil(t, resp)
	})
}
