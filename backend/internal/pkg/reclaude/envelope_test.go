package reclaude

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncodeEnvelope(t *testing.T) {
	t.Run("长度前缀是 4 字节大端", func(t *testing.T) {
		// Arrange
		meta := ClientRequestMetadata{
			URL:     "https://api.anthropic.com/v1/messages",
			Method:  "POST",
			Headers: map[string]string{"content-type": "application/json"},
		}

		// Act
		envelope, err := EncodeEnvelope(meta, []byte(`{"model":"claude"}`))

		// Assert
		require.NoError(t, err)
		metaJSON, err := json.Marshal(meta)
		require.NoError(t, err)
		require.Equal(t, uint32(len(metaJSON)), binary.BigEndian.Uint32(envelope[:EnvelopeLengthPrefixBytes]))
		require.Equal(t, metaJSON, envelope[EnvelopeLengthPrefixBytes:EnvelopeLengthPrefixBytes+len(metaJSON)])
		require.Equal(t, []byte(`{"model":"claude"}`), envelope[EnvelopeLengthPrefixBytes+len(metaJSON):])
	})

	t.Run("空 body 只有前缀与元数据", func(t *testing.T) {
		meta := ClientRequestMetadata{URL: "https://example.com", Method: "GET", Headers: map[string]string{}}

		envelope, err := EncodeEnvelope(meta, nil)

		require.NoError(t, err)
		metaLen := binary.BigEndian.Uint32(envelope[:EnvelopeLengthPrefixBytes])
		require.Len(t, envelope, EnvelopeLengthPrefixBytes+int(metaLen))
	})

	t.Run("元数据含 unicode 不破坏长度前缀", func(t *testing.T) {
		meta := ClientRequestMetadata{
			URL:     "https://api.anthropic.com/v1/messages?q=中文",
			Method:  "POST",
			Headers: map[string]string{"x-note": "你好 🌏"},
		}

		envelope, err := EncodeEnvelope(meta, []byte("body"))

		require.NoError(t, err)
		metaLen := int(binary.BigEndian.Uint32(envelope[:EnvelopeLengthPrefixBytes]))
		var decoded ClientRequestMetadata
		require.NoError(t, json.Unmarshal(envelope[EnvelopeLengthPrefixBytes:EnvelopeLengthPrefixBytes+metaLen], &decoded))
		require.Equal(t, meta.Headers["x-note"], decoded.Headers["x-note"])
	})

	t.Run("大 body 原样附在尾部", func(t *testing.T) {
		body := bytes.Repeat([]byte("x"), 1<<20)
		meta := ClientRequestMetadata{URL: "https://example.com", Method: "POST", Headers: map[string]string{}}

		envelope, err := EncodeEnvelope(meta, body)

		require.NoError(t, err)
		metaLen := int(binary.BigEndian.Uint32(envelope[:EnvelopeLengthPrefixBytes]))
		require.Equal(t, body, envelope[EnvelopeLengthPrefixBytes+metaLen:])
	})

	t.Run("headers 必须全小写", func(t *testing.T) {
		meta := ClientRequestMetadata{
			URL:     "https://example.com",
			Method:  "POST",
			Headers: map[string]string{"Content-Type": "application/json"},
		}

		_, err := EncodeEnvelope(meta, nil)

		require.Error(t, err)
		require.Contains(t, err.Error(), "lowercase")
	})
}

func TestDecodeResponseEnvelope(t *testing.T) {
	buildResponse := func(t *testing.T, meta GatewayResponseMetadata, body string) []byte {
		t.Helper()
		metaJSON, err := json.Marshal(meta)
		require.NoError(t, err)
		out := make([]byte, EnvelopeLengthPrefixBytes)
		binary.BigEndian.PutUint32(out, uint32(len(metaJSON)))
		out = append(out, metaJSON...)
		return append(out, body...)
	}

	t.Run("正常响应：元数据解出、body 保持流式", func(t *testing.T) {
		// Arrange
		raw := buildResponse(t, GatewayResponseMetadata{
			Status:     200,
			StatusText: "OK",
			Headers:    map[string]string{"content-type": "text/event-stream"},
		}, "data: {\"type\":\"message_start\"}\n\n")

		// Act
		meta, body, err := DecodeResponseEnvelope(bytes.NewReader(raw))

		// Assert
		require.NoError(t, err)
		require.Equal(t, 200, meta.Status)
		require.Equal(t, "text/event-stream", meta.Headers["content-type"])
		rest, err := io.ReadAll(body)
		require.NoError(t, err)
		require.Equal(t, "data: {\"type\":\"message_start\"}\n\n", string(rest))
	})

	t.Run("metaLen 超过 16MiB 上限报错", func(t *testing.T) {
		raw := make([]byte, EnvelopeLengthPrefixBytes)
		binary.BigEndian.PutUint32(raw, EnvelopeMaxMetadataBytes+1)

		_, _, err := DecodeResponseEnvelope(bytes.NewReader(raw))

		require.Error(t, err)
		require.Contains(t, err.Error(), "exceeds limit")
	})

	t.Run("metaLen 为 0 报错", func(t *testing.T) {
		raw := make([]byte, EnvelopeLengthPrefixBytes)

		_, _, err := DecodeResponseEnvelope(bytes.NewReader(raw))

		require.Error(t, err)
	})

	t.Run("长度前缀被截断报错", func(t *testing.T) {
		_, _, err := DecodeResponseEnvelope(bytes.NewReader([]byte{0x00, 0x01}))

		require.Error(t, err)
	})

	t.Run("元数据被截断报错", func(t *testing.T) {
		raw := make([]byte, EnvelopeLengthPrefixBytes)
		binary.BigEndian.PutUint32(raw, 128)
		raw = append(raw, []byte(`{"status":200`)...)

		_, _, err := DecodeResponseEnvelope(bytes.NewReader(raw))

		require.Error(t, err)
	})

	t.Run("元数据不是合法 JSON 报错", func(t *testing.T) {
		raw := make([]byte, EnvelopeLengthPrefixBytes)
		payload := []byte("not-json")
		binary.BigEndian.PutUint32(raw, uint32(len(payload)))
		raw = append(raw, payload...)

		_, _, err := DecodeResponseEnvelope(bytes.NewReader(raw))

		require.Error(t, err)
	})

	// 响应侧必须保持流式：SSE 首字节不能等到整个响应读完才到达下游。
	t.Run("body 是流式的而非全量缓冲", func(t *testing.T) {
		meta := GatewayResponseMetadata{Status: 200, Headers: map[string]string{}}
		metaJSON, err := json.Marshal(meta)
		require.NoError(t, err)
		head := make([]byte, EnvelopeLengthPrefixBytes)
		binary.BigEndian.PutUint32(head, uint32(len(metaJSON)))
		head = append(head, metaJSON...)

		// 元数据后面接一个永不关闭的 reader：若实现是全量缓冲，这里会卡死
		pr, pw := io.Pipe()
		go func() {
			_, _ = pw.Write([]byte("data: first\n\n"))
		}()

		_, body, err := DecodeResponseEnvelope(io.MultiReader(bytes.NewReader(head), pr))
		require.NoError(t, err)

		chunk := make([]byte, len("data: first\n\n"))
		n, err := io.ReadFull(body, chunk)
		require.NoError(t, err)
		require.Equal(t, "data: first\n\n", string(chunk[:n]))
	})
}

func TestEnvelopeRoundTrip(t *testing.T) {
	// 请求信封与响应信封共用线格式，往返等价性用同一段字节验证。
	meta := GatewayResponseMetadata{
		Status:     429,
		StatusText: "Too Many Requests",
		Headers:    map[string]string{"retry-after": "30"},
		Events:     []ReclaudeEvent{{Kind: EventKindRateLimited, RetryAfterSec: 30}},
	}
	metaJSON, err := json.Marshal(meta)
	require.NoError(t, err)

	raw := make([]byte, EnvelopeLengthPrefixBytes)
	binary.BigEndian.PutUint32(raw, uint32(len(metaJSON)))
	raw = append(raw, metaJSON...)
	raw = append(raw, []byte("payload")...)

	decoded, body, err := DecodeResponseEnvelope(bytes.NewReader(raw))
	require.NoError(t, err)
	require.Equal(t, meta.Status, decoded.Status)
	require.Len(t, decoded.Events, 1)
	require.Equal(t, EventKindRateLimited, decoded.Events[0].Kind)

	rest, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, "payload", string(rest))
	require.False(t, strings.Contains(string(rest), "retry-after"), "events 不得混进 body")
}
