package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func probeAccount(t *testing.T) (*Account, *ReclaudeCredentialCipher) {
	t.Helper()
	account, cipher := reclaudeTestAccount(t)
	account.Proxy = &Proxy{Protocol: "http", Host: "residential.example.com", Port: 8080}
	return account, cipher
}

func TestReclaudeGatewayProbe(t *testing.T) {
	ctx := context.Background()

	t.Run("打到网关的非推理端点且带 Bearer", func(t *testing.T) {
		// Arrange
		account, cipher := probeAccount(t)
		upstream := &capturingUpstream{response: &http.Response{StatusCode: 200, Header: http.Header{}}}

		// Act
		_, err := NewReclaudeGatewayProbe(upstream, cipher).
			ProbeReclaudeEndpoint(ctx, account, ReclaudeClientAccountPath)

		// Assert
		require.NoError(t, err)
		require.Equal(t, "https://la.route.reclaude.ai/client/account", upstream.gotRequest.URL.String())
		require.Equal(t, http.MethodGet, upstream.gotRequest.Method)
		require.Equal(t, "Bearer sk-rec-abcdef", upstream.gotRequest.Header.Get("Authorization"))
	})

	// 签名覆盖的是信封字节，而这些端点没有信封。
	t.Run("不带签名头", func(t *testing.T) {
		account, cipher := probeAccount(t)
		upstream := &capturingUpstream{response: &http.Response{StatusCode: 200, Header: http.Header{}}}

		_, err := NewReclaudeGatewayProbe(upstream, cipher).
			ProbeReclaudeEndpoint(ctx, account, ReclaudeHealthReadyPath)

		require.NoError(t, err)
		require.Empty(t, upstream.gotRequest.Header.Get("X-Reclaude-Signature"))
		require.Empty(t, upstream.gotRequest.Header.Get("X-Reclaude-Body-Sha256"))
	})

	// 心跳从另一个出口发出去 = 这台设备同时出现在两个地方。
	t.Run("走账号绑定的那条代理", func(t *testing.T) {
		account, cipher := probeAccount(t)
		upstream := &capturingUpstream{response: &http.Response{StatusCode: 200, Header: http.Header{}}}

		_, err := NewReclaudeGatewayProbe(upstream, cipher).
			ProbeReclaudeEndpoint(ctx, account, ReclaudeClientAccountPath)

		require.NoError(t, err)
		require.Contains(t, upstream.gotProxyURL, "residential.example.com")
	})

	// 🔴 宁可这台设备暂时没有心跳，也不能让「最近 IP」从住宅跳到机房。
	t.Run("没有代理时拒绝发送而不是直连", func(t *testing.T) {
		account, cipher := probeAccount(t)
		account.Proxy = nil
		upstream := &capturingUpstream{response: &http.Response{StatusCode: 200, Header: http.Header{}}}

		_, err := NewReclaudeGatewayProbe(upstream, cipher).
			ProbeReclaudeEndpoint(ctx, account, ReclaudeClientAccountPath)

		require.Error(t, err)
		require.Nil(t, upstream.gotRequest, "没有代理却把请求发出去了")
	})

	t.Run("挂 long_stream profile 与 public-hosts-only", func(t *testing.T) {
		account, cipher := probeAccount(t)
		upstream := &capturingUpstream{response: &http.Response{StatusCode: 200, Header: http.Header{}}}

		_, err := NewReclaudeGatewayProbe(upstream, cipher).
			ProbeReclaudeEndpoint(ctx, account, ReclaudeClientAccountPath)

		require.NoError(t, err)
		reqCtx := upstream.gotRequest.Context()
		require.Equal(t, HTTPUpstreamProfileLongStream, HTTPUpstreamProfileFromContext(reqCtx))
		require.True(t, HTTPUpstreamPublicHostsOnly(reqCtx))
	})

	t.Run("网关地址不在白名单时拒绝", func(t *testing.T) {
		account, cipher := probeAccount(t)
		account.Credentials[CredKeyReclaudeGateway] = "https://evil.example.com"
		upstream := &capturingUpstream{}

		_, err := NewReclaudeGatewayProbe(upstream, cipher).
			ProbeReclaudeEndpoint(ctx, account, ReclaudeClientAccountPath)

		require.ErrorIs(t, err, ErrReclaudeGatewayNotAllowed)
		require.Nil(t, upstream.gotRequest)
	})

	t.Run("非 reclaude 账号被拒绝", func(t *testing.T) {
		_, cipher := probeAccount(t)
		upstream := &capturingUpstream{}

		_, err := NewReclaudeGatewayProbe(upstream, cipher).ProbeReclaudeEndpoint(
			ctx, &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth},
			ReclaudeClientAccountPath)

		require.Error(t, err)
		require.Nil(t, upstream.gotRequest)
	})
}
