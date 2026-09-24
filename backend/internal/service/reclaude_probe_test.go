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

	// 🔴 语义更正（2026-09-23 实测）：控制面端点**同样需要签名**。
	//
	// 原断言是「签名覆盖信封字节，而这些端点没有信封，所以不签」。实测证伪：
	// GET /client/account 不带签名头会被网关拒为
	//   {"code":"device_signature_required","status":400}
	// 而 /health/ready 不需要 —— 于是自检表现为「第一步网关可达 ✓、第二步
	// 凭据无效 ✗」，把一个缺签名的问题伪装成凭据问题，极具误导性。
	//
	// GET 没有 body，签的是 sha256("")，canonical 串格式与信封路径完全一致。
	t.Run("控制面请求必须带签名头", func(t *testing.T) {
		account, cipher := probeAccount(t)
		upstream := &capturingUpstream{response: &http.Response{StatusCode: 200, Header: http.Header{}}}

		_, err := NewReclaudeGatewayProbe(upstream, cipher).
			ProbeReclaudeEndpoint(ctx, account, ReclaudeClientAccountPath)

		require.NoError(t, err)
		require.NotEmpty(t, upstream.gotRequest.Header.Get("X-Reclaude-Signature"),
			"缺签名会被网关拒为 device_signature_required")
		require.NotEmpty(t, upstream.gotRequest.Header.Get("X-Reclaude-Body-Sha256"))
		require.NotEmpty(t, upstream.gotRequest.Header.Get("X-Reclaude-Ts"))
		require.NotEmpty(t, upstream.gotRequest.Header.Get("X-Reclaude-Nonce"))
		require.Equal(t, "43448", upstream.gotRequest.Header.Get("X-Reclaude-Device-Id"))
	})

	// 探活端点不需要签名，但带上也无害 —— 统一签名路径比按端点分叉更不容易漏。
	t.Run("探活端点也走同一条签名路径", func(t *testing.T) {
		account, cipher := probeAccount(t)
		upstream := &capturingUpstream{response: &http.Response{StatusCode: 200, Header: http.Header{}}}

		_, err := NewReclaudeGatewayProbe(upstream, cipher).
			ProbeReclaudeEndpoint(ctx, account, ReclaudeHealthReadyPath)

		require.NoError(t, err)
		require.NotEmpty(t, upstream.gotRequest.Header.Get("X-Reclaude-Signature"))
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
		// 🔴 必须是 Reclaude profile（强制 HTTP/1.1）。真客户端走 H1，
		// 用 LongStream 会走 H2 并周期性发 h2 PING —— ALPN 在握手阶段就暴露。
		require.Equal(t, HTTPUpstreamProfileReclaude, HTTPUpstreamProfileFromContext(reqCtx))
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
