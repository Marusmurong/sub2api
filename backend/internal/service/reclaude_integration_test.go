package service

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/reclaude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// fakeGateway 是一个假 reclaude 网关：验签、解信封、按脚本回包。
//
// ⚠️ 验签这一步是**自我一致性检查**（它用的是我们自己的解析），
// 不构成对真实网关的验证。真值只能来自本地 sink 抓到的出站样本。
// 它能防的是回归，防不了「我们整体理解错了」。
type fakeGateway struct {
	t             *testing.T
	publicKey     ed25519.PublicKey
	status        int
	respMeta      reclaude.GatewayResponseMetadata
	respBody      []byte
	rawBody       []byte // 非 200 时直接返回的原始体
	seenRequest   reclaude.ClientRequestMetadata
	seenInnerBody []byte
}

func (g *fakeGateway) DoWithTLS(
	req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile,
) (*http.Response, error) {
	envelope, err := io.ReadAll(req.Body)
	require.NoError(g.t, err)

	g.verifySignature(req.Header, envelope)

	metaLen := int(binary.BigEndian.Uint32(envelope[:reclaude.EnvelopeLengthPrefixBytes]))
	require.NoError(g.t, json.Unmarshal(
		envelope[reclaude.EnvelopeLengthPrefixBytes:reclaude.EnvelopeLengthPrefixBytes+metaLen], &g.seenRequest))
	g.seenInnerBody = envelope[reclaude.EnvelopeLengthPrefixBytes+metaLen:]

	if g.status != http.StatusOK {
		return &http.Response{
			StatusCode: g.status,
			Header:     http.Header{},
			Body:       io.NopCloser(bytes.NewReader(g.rawBody)),
		}, nil
	}

	metaJSON, err := json.Marshal(g.respMeta)
	require.NoError(g.t, err)
	out := make([]byte, reclaude.EnvelopeLengthPrefixBytes)
	binary.BigEndian.PutUint32(out, uint32(len(metaJSON)))
	out = append(out, metaJSON...)
	out = append(out, g.respBody...)

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/octet-stream"}},
		Body:       io.NopCloser(bytes.NewReader(out)),
	}, nil
}

func (g *fakeGateway) Do(*http.Request, string, int64, int) (*http.Response, error) {
	g.t.Fatal("fake gateway must be reached through DoWithTLS")
	return nil, nil
}

func (g *fakeGateway) verifySignature(header http.Header, envelope []byte) {
	g.t.Helper()

	ts, err := strconv.ParseInt(header.Get(reclaude.HeaderTimestamp), 10, 64)
	require.NoError(g.t, err)
	require.InDelta(g.t, time.Now().UnixMilli(), ts, float64(time.Minute.Milliseconds()),
		"时间戳偏离太远会撞上重放窗口")

	nonce, err := base64.RawURLEncoding.DecodeString(header.Get(reclaude.HeaderNonce))
	require.NoError(g.t, err)
	require.Len(g.t, nonce, reclaude.SignatureNonceBytes)

	sum := sha256.Sum256(envelope)
	require.Equal(g.t, base64.RawURLEncoding.EncodeToString(sum[:]), header.Get(reclaude.HeaderBodySHA256),
		"哈希的必须是整个信封，不是原始请求体")

	canonical := reclaude.SignatureVersion + "\n" +
		strconv.FormatInt(ts, 10) + "\n" +
		header.Get(reclaude.HeaderNonce) + "\n" +
		header.Get(reclaude.HeaderBodySHA256)
	signature, err := base64.RawURLEncoding.DecodeString(header.Get(reclaude.HeaderSignature))
	require.NoError(g.t, err)
	require.True(g.t, ed25519.Verify(g.publicKey, []byte(canonical), signature), "验签不过")
}

func newFakeGateway(t *testing.T) *fakeGateway {
	t.Helper()
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	return &fakeGateway{
		t:         t,
		publicKey: ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey),
		status:    http.StatusOK,
	}
}

const sseUsagePayload = "event: message_delta\n" +
	"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":42}}\n\n"

func TestReclaudeEndToEnd(t *testing.T) {
	account, cipher := reclaudeTestAccount(t)

	t.Run("压缩的 SSE 一路解到明文且 usage 可见", func(t *testing.T) {
		// Arrange：Anthropic 一定压缩（CC 伪装发的 Accept-Encoding 决定了这点），
		// reclaude 原样装箱，没有任何一层会解这个内层压缩。
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		_, err := writer.Write([]byte(sseUsagePayload))
		require.NoError(t, err)
		require.NoError(t, writer.Close())

		gateway := newFakeGateway(t)
		gateway.respMeta = reclaude.GatewayResponseMetadata{
			Status: 200,
			Headers: map[string]string{
				"content-type":     "text/event-stream",
				"content-encoding": "gzip",
				"request-id":       "req_e2e",
			},
		}
		gateway.respBody = compressed.Bytes()

		// Act
		resp, err := NewReclaudeUpstream(gateway, cipher, nil).Do(innerRequest(t, `{"model":"claude"}`), account, "")

		// Assert
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, sseUsagePayload, string(body), "SSE 没被解压，usage 解析会直接归零")
		require.Contains(t, string(body), "output_tokens")
		require.Equal(t, "req_e2e", resp.Header.Get("Request-Id"))
		require.Empty(t, resp.Header.Get("Content-Encoding"))
	})

	t.Run("内层请求原样送达，headers 取值一个字不改", func(t *testing.T) {
		gateway := newFakeGateway(t)
		gateway.respMeta = reclaude.GatewayResponseMetadata{Status: 200, Headers: map[string]string{}}

		resp, err := NewReclaudeUpstream(gateway, cipher, nil).Do(innerRequest(t, `{"model":"claude"}`), account, "")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, "https://api.anthropic.com/v1/messages", gateway.seenRequest.URL)
		require.Equal(t, "claude-cli/2.1.263 (external, cli)", gateway.seenRequest.Headers["user-agent"],
			"内层 header 的取值是 CC 伪装的产物，不能被改动")
		require.JSONEq(t, `{"model":"claude"}`, string(gateway.seenInnerBody))
	})

	// 瞬时 429 的判据是 X-Should-Retry；小写键读不到 ⇒ 恒 false ⇒
	// 被误判为配额耗尽，触发「整池限流中」那类故障。
	t.Run("内层 429 的限流头可被既有逻辑读到", func(t *testing.T) {
		gateway := newFakeGateway(t)
		gateway.respMeta = reclaude.GatewayResponseMetadata{
			Status: 429,
			Headers: map[string]string{
				"x-should-retry":                        "true",
				"anthropic-ratelimit-unified-5h-status": "allowed",
				"retry-after":                           "12",
			},
		}
		gateway.respBody = []byte(`{"type":"error","error":{"type":"rate_limit_error"}}`)

		resp, err := NewReclaudeUpstream(gateway, cipher, nil).Do(innerRequest(t, "{}"), account, "")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, 429, resp.StatusCode)
		require.True(t, upstreamSaysRetryable(resp.Header),
			"X-Should-Retry 读不到 ⇒ 瞬时 429 会被当成配额耗尽")
		require.Equal(t, "allowed", resp.Header.Get("anthropic-ratelimit-unified-5h-status"))
	})

	// 外层 401 的 body 是他们自己的错误结构，不是信封。
	t.Run("网关拒绝时给出干净的错误而不是信封解码失败", func(t *testing.T) {
		gateway := newFakeGateway(t)
		gateway.status = http.StatusUnauthorized
		gateway.rawBody = []byte(`{"error":"invalid sk","contact":"someone@else.com"}`)

		resp, err := NewReclaudeUpstream(gateway, cipher, nil).Do(innerRequest(t, "{}"), account, "")

		require.Error(t, err)
		require.Nil(t, resp)

		var gatewayErr *ReclaudeGatewayError
		require.ErrorAs(t, err, &gatewayErr)
		require.Equal(t, http.StatusUnauthorized, gatewayErr.StatusCode)
		require.NotContains(t, err.Error(), "someone@else.com", "他们的错误体里可能有别人的信息")
		require.False(t, strings.Contains(err.Error(), "metadata length"),
			"不该表现成信封解码失败，那会把真实原因盖掉")
	})

	// 带外事件走旁路；status=200 的正常响应里出现 events 也要处理。
	t.Run("200 响应里的 events 被旁路且不污染 body", func(t *testing.T) {
		actuator := &recordingActuator{}
		gateway := newFakeGateway(t)
		gateway.respMeta = reclaude.GatewayResponseMetadata{
			Status:  200,
			Headers: map[string]string{"content-type": "text/event-stream"},
			Events: []reclaude.ReclaudeEvent{
				{Kind: reclaude.EventKindAccountSwitched, NewAccountMaskedEmail: "v***@x.com"},
			},
		}
		gateway.respBody = []byte(sseUsagePayload)

		dispatcher := NewReclaudeEventDispatcher(actuator, time.Minute)
		resp, err := NewReclaudeUpstream(gateway, cipher, dispatcher).Do(innerRequest(t, "{}"), account, "")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, sseUsagePayload, string(body))
		require.NotContains(t, string(body), "v***@x.com")

		require.Equal(t, []ReclaudeAccountActionKind{ReclaudeActionAccountSwitched}, actuator.kinds())
	})
}
