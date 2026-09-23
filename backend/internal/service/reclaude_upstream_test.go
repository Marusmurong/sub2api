package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/reclaude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// capturingUpstream 记录出站请求并返回预设响应。
type capturingUpstream struct {
	gotRequest     *http.Request
	gotBody        []byte
	gotProxyURL    string
	gotConcurrency int
	gotTLSProfile  *tlsfingerprint.Profile
	response       *http.Response
	err            error
	bodyClosed     *bool
}

func (u *capturingUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return nil, errors.New("Do must not be used by ReclaudeUpstream")
}

func (u *capturingUpstream) DoWithTLS(
	req *http.Request, proxyURL string, _ int64, concurrency int, profile *tlsfingerprint.Profile,
) (*http.Response, error) {
	u.gotRequest = req
	u.gotProxyURL = proxyURL
	u.gotConcurrency = concurrency
	u.gotTLSProfile = profile
	if req != nil && req.Body != nil {
		u.gotBody, _ = io.ReadAll(req.Body)
	}
	return u.response, u.err
}

func envelopeResponse(t *testing.T, status int, meta reclaude.GatewayResponseMetadata, body string) []byte {
	t.Helper()
	meta.Status = status
	metaJSON, err := json.Marshal(meta)
	require.NoError(t, err)
	out := make([]byte, reclaude.EnvelopeLengthPrefixBytes)
	binary.BigEndian.PutUint32(out, uint32(len(metaJSON)))
	out = append(out, metaJSON...)
	return append(out, body...)
}

type closeTrackingBody struct {
	io.Reader
	closed *bool
}

func (b *closeTrackingBody) Close() error {
	*b.closed = true
	return nil
}

func reclaudeTestAccount(t *testing.T) (*Account, *ReclaudeCredentialCipher) {
	t.Helper()
	cipher := NewReclaudeCredentialCipher(&fakeEncryptor{})
	credentials, err := cipher.EncryptForStorage(map[string]any{
		CredKeyReclaudeSK:   "sk-rec-abcdef",
		CredKeyReclaudeSeed: validSeedHex(),
	})
	require.NoError(t, err)
	credentials[CredKeyReclaudeDeviceID] = float64(43448)
	credentials[CredKeyReclaudeGateway] = "https://la.route.reclaude.ai"
	credentials[CredKeyReclaudeClientVersion] = "v1.4.0"
	credentials[CredKeyReclaudeClientPlatform] = "linux/amd64"

	return &Account{
		ID:          9,
		Platform:    PlatformAnthropic,
		Type:        AccountTypeReclaude,
		Concurrency: 6,
		Credentials: credentials,
	}, cipher
}

func innerRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodPost,
		"https://api.anthropic.com/v1/messages", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-cli/2.1.263 (external, cli)")
	return req
}

func TestReclaudeUpstream_Do_Request(t *testing.T) {
	account, cipher := reclaudeTestAccount(t)

	t.Run("装箱后打到 /proxy，签名头齐全", func(t *testing.T) {
		// Arrange
		upstream := &capturingUpstream{response: &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(bytes.NewReader(envelopeResponse(t, 200, reclaude.GatewayResponseMetadata{}, "ok"))),
		}}
		sut := NewReclaudeUpstream(upstream, cipher, nil)

		// Act
		resp, err := sut.Do(innerRequest(t, `{"model":"claude"}`), account, "http://proxy:1080")

		// Assert
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, "https://la.route.reclaude.ai/proxy", upstream.gotRequest.URL.String())
		require.Equal(t, http.MethodPost, upstream.gotRequest.Method)
		require.Equal(t, "Bearer sk-rec-abcdef", upstream.gotRequest.Header.Get("Authorization"))
		require.Equal(t, "application/octet-stream", upstream.gotRequest.Header.Get("Content-Type"))
		require.Equal(t, "v1.4.0", upstream.gotRequest.Header.Get(reclaude.HeaderClientVersion))
		require.Equal(t, "linux/amd64", upstream.gotRequest.Header.Get(reclaude.HeaderClientPlatform))
		require.Equal(t, "43448", upstream.gotRequest.Header.Get(reclaude.HeaderDeviceID))
		require.Len(t, upstream.gotRequest.Header.Get(reclaude.HeaderSignature), 86,
			"base64url 无填充的 64 字节签名是 86 字符（真实客户端抓包为准）")
	})

	t.Run("信封里的 headers 全小写且带上原始 URL 与方法", func(t *testing.T) {
		upstream := &capturingUpstream{response: &http.Response{
			StatusCode: 200, Header: http.Header{},
			Body: io.NopCloser(bytes.NewReader(envelopeResponse(t, 200, reclaude.GatewayResponseMetadata{}, ""))),
		}}
		sut := NewReclaudeUpstream(upstream, cipher, nil)

		resp, err := sut.Do(innerRequest(t, `{"model":"claude"}`), account, "")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		metaLen := binary.BigEndian.Uint32(upstream.gotBody[:reclaude.EnvelopeLengthPrefixBytes])
		var meta reclaude.ClientRequestMetadata
		require.NoError(t, json.Unmarshal(
			upstream.gotBody[reclaude.EnvelopeLengthPrefixBytes:reclaude.EnvelopeLengthPrefixBytes+metaLen], &meta))

		require.Equal(t, "https://api.anthropic.com/v1/messages", meta.URL)
		require.Equal(t, http.MethodPost, meta.Method)
		require.NotEmpty(t, meta.TraceID)
		for name := range meta.Headers {
			require.Equal(t, strings.ToLower(name), name, "header %q 没有小写化", name)
		}
		require.Equal(t, "claude-cli/2.1.263 (external, cli)", meta.Headers["user-agent"])

		body := upstream.gotBody[reclaude.EnvelopeLengthPrefixBytes+metaLen:]
		require.JSONEq(t, `{"model":"claude"}`, string(body))
	})

	// 传 1 会让传输层串行化（MaxConnsPerHost），把推导出来的并发值一行作废。
	t.Run("并发参数透传 account.Concurrency 而不是 1", func(t *testing.T) {
		upstream := &capturingUpstream{response: &http.Response{
			StatusCode: 200, Header: http.Header{},
			Body: io.NopCloser(bytes.NewReader(envelopeResponse(t, 200, reclaude.GatewayResponseMetadata{}, ""))),
		}}
		sut := NewReclaudeUpstream(upstream, cipher, nil)

		resp, err := sut.Do(innerRequest(t, "{}"), account, "http://proxy:1080")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, 6, upstream.gotConcurrency)
		require.Equal(t, "http://proxy:1080", upstream.gotProxyURL)
		require.Nil(t, upstream.gotTLSProfile, "外层是 reclaude 网关，不套 Anthropic 的 TLS 指纹")
	})

	// 默认 profile 不设 ForceAttemptHTTP2 ⇒ 外层退成 HTTP/1.1，而真客户端走 H2。
	// 一个 h1-only 的「v1.4.0 客户端」正是最该避免的一眼假。
	t.Run("挂 long_stream profile 与 public-hosts-only 标记", func(t *testing.T) {
		upstream := &capturingUpstream{response: &http.Response{
			StatusCode: 200, Header: http.Header{},
			Body: io.NopCloser(bytes.NewReader(envelopeResponse(t, 200, reclaude.GatewayResponseMetadata{}, ""))),
		}}
		sut := NewReclaudeUpstream(upstream, cipher, nil)

		resp, err := sut.Do(innerRequest(t, "{}"), account, "")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		ctx := upstream.gotRequest.Context()
		require.Equal(t, HTTPUpstreamProfileLongStream, HTTPUpstreamProfileFromContext(ctx))
		require.True(t, HTTPUpstreamPublicHostsOnly(ctx))
	})

	// detachStreamUpstreamContext 把 WithoutCancel 挂在 inner req 上；
	// 另取 ctx 等于把那层 detach 白做，下游一断就拿不到 usage。
	t.Run("沿用内层请求的 context", func(t *testing.T) {
		type ctxKey struct{}
		upstream := &capturingUpstream{response: &http.Response{
			StatusCode: 200, Header: http.Header{},
			Body: io.NopCloser(bytes.NewReader(envelopeResponse(t, 200, reclaude.GatewayResponseMetadata{}, ""))),
		}}
		sut := NewReclaudeUpstream(upstream, cipher, nil)

		inner := innerRequest(t, "{}")
		inner = inner.WithContext(context.WithValue(inner.Context(), ctxKey{}, "carried"))

		resp, err := sut.Do(inner, account, "")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, "carried", upstream.gotRequest.Context().Value(ctxKey{}))
	})
}

func TestReclaudeUpstream_Do_Response(t *testing.T) {
	account, cipher := reclaudeTestAccount(t)

	t.Run("合成的 Response 带 canonical header 与内层状态码", func(t *testing.T) {
		upstream := &capturingUpstream{response: &http.Response{
			StatusCode: 200, Header: http.Header{},
			Body: io.NopCloser(bytes.NewReader(envelopeResponse(t, 429, reclaude.GatewayResponseMetadata{
				Headers: map[string]string{
					"x-should-retry":                        "true",
					"request-id":                            "req_abc",
					"anthropic-ratelimit-unified-5h-status": "allowed",
				},
			}, `{"type":"error"}`))),
		}}
		sut := NewReclaudeUpstream(upstream, cipher, nil)

		resp, err := sut.Do(innerRequest(t, "{}"), account, "")

		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, 429, resp.StatusCode, "内层状态码，不是外层的 200")
		require.Equal(t, "true", resp.Header.Get("X-Should-Retry"))
		require.Equal(t, "req_abc", resp.Header.Get("Request-Id"))
		require.EqualValues(t, -1, resp.ContentLength)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"type":"error"}`, string(body))
	})

	// 外层非 200 不是信封，是他们自己的错误结构。
	// 直接读 4 字节当长度：`{"er` ≈ 2.06 GB → 报「超 16MiB」，掩盖真实原因。
	t.Run("外层非 200 绝不当信封解", func(t *testing.T) {
		for _, status := range []int{401, 403, 429, 500, 502} {
			closed := false
			upstream := &capturingUpstream{response: &http.Response{
				StatusCode: status,
				Header:     http.Header{},
				Body:       &closeTrackingBody{Reader: strings.NewReader(`{"error":"bad sk"}`), closed: &closed},
			}}
			sut := NewReclaudeUpstream(upstream, cipher, nil)

			resp, err := sut.Do(innerRequest(t, "{}"), account, "")

			require.Errorf(t, err, "status=%d", status)
			require.Nil(t, resp)

			var gatewayErr *ReclaudeGatewayError
			require.ErrorAs(t, err, &gatewayErr)
			require.Equal(t, status, gatewayErr.StatusCode)
			require.NotContains(t, err.Error(), "exceeds limit", "不该表现成信封解码失败")
			require.NotContains(t, err.Error(), "bad sk", "绝不透传他们的错误体")
			require.True(t, closed, "外层错误路径必须关闭 body，否则 inFlight 泄漏")
		}
	})

	// 任何 error 返回路径都必须先关内层 body：调用方拿到的是 (nil, err)，
	// 它的 `if resp != nil` 清理救不了。inFlight > 0 的 client entry 永不被淘汰。
	t.Run("信封解码失败也要关闭 body", func(t *testing.T) {
		closed := false
		upstream := &capturingUpstream{response: &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       &closeTrackingBody{Reader: strings.NewReader("tiny"), closed: &closed},
		}}
		sut := NewReclaudeUpstream(upstream, cipher, nil)

		resp, err := sut.Do(innerRequest(t, "{}"), account, "")

		require.Error(t, err)
		require.Nil(t, resp)
		require.True(t, closed)
	})

	t.Run("关闭合成 Response 会关掉原始 body", func(t *testing.T) {
		closed := false
		upstream := &capturingUpstream{response: &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body: &closeTrackingBody{
				Reader: bytes.NewReader(envelopeResponse(t, 200, reclaude.GatewayResponseMetadata{}, "ok")),
				closed: &closed,
			},
		}}
		sut := NewReclaudeUpstream(upstream, cipher, nil)

		resp, err := sut.Do(innerRequest(t, "{}"), account, "")
		require.NoError(t, err)

		require.NoError(t, resp.Body.Close())
		require.True(t, closed)
	})

	// events 是带外控制信道，绝不能进 body —— 原版会把它伪装成 Anthropic 错误
	// 吐给用户，内容里带着别人的掩码邮箱。
	t.Run("events 旁路给 handler，不进 body", func(t *testing.T) {
		var handled []reclaude.ReclaudeEvent
		upstream := &capturingUpstream{response: &http.Response{
			StatusCode: 200, Header: http.Header{},
			Body: io.NopCloser(bytes.NewReader(envelopeResponse(t, 200, reclaude.GatewayResponseMetadata{
				Events: []reclaude.ReclaudeEvent{
					{Kind: reclaude.EventKindAccountSwitched, NewAccountMaskedEmail: "a***@b.com"},
				},
			}, `{"type":"message_start"}`))),
		}}
		sut := NewReclaudeUpstream(upstream, cipher, ReclaudeEventHandlerFunc(
			func(_ *Account, events []reclaude.ReclaudeEvent) { handled = events },
		))

		resp, err := sut.Do(innerRequest(t, "{}"), account, "")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		require.Len(t, handled, 1)
		require.Equal(t, reclaude.EventKindAccountSwitched, handled[0].Kind)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NotContains(t, string(body), "a***@b.com", "掩码邮箱泄漏到下游了")
		require.NotContains(t, string(body), "account_switched")
	})

	t.Run("凭据无法解密时报错且不发出任何请求", func(t *testing.T) {
		brokenCipher := NewReclaudeCredentialCipher(&fakeEncryptor{decryptErr: errFakeNotCipher})
		upstream := &capturingUpstream{}
		sut := NewReclaudeUpstream(upstream, brokenCipher, nil)

		resp, err := sut.Do(innerRequest(t, "{}"), account, "")

		require.Error(t, err)
		require.Nil(t, resp)
		require.Nil(t, upstream.gotRequest, "凭据都解不开就不该有出站请求")
	})

	t.Run("网关地址不在白名单时拒绝出站", func(t *testing.T) {
		tampered, _ := reclaudeTestAccount(t)
		tampered.Credentials[CredKeyReclaudeGateway] = "https://evil.example.com"
		upstream := &capturingUpstream{}
		sut := NewReclaudeUpstream(upstream, cipher, nil)

		resp, err := sut.Do(innerRequest(t, "{}"), tampered, "")

		require.ErrorIs(t, err, ErrReclaudeGatewayNotAllowed)
		require.Nil(t, resp)
		require.Nil(t, upstream.gotRequest)
	})
}
