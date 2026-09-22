package reclaude

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/stretchr/testify/require"
)

// meta.Headers 是全小写的 map[string]string，而 http.Header.Get 走
// textproto.CanonicalMIMEHeaderKey。直接塞进去的话下面这些读取**全部返回空**：
// upstreamRequestID / X-Should-Retry / anthropic-ratelimit-* / Content-Type。
//
// 其中 X-Should-Retry 恒 false 最凶：瞬时 429 会被误判为配额耗尽。
func TestCanonicalizeHeaders(t *testing.T) {
	t.Run("小写键可被 Get 读到", func(t *testing.T) {
		header := CanonicalizeHeaders(map[string]string{
			"request-id":                            "req_abc",
			"x-should-retry":                        "true",
			"anthropic-ratelimit-unified-5h-status": "allowed",
			"content-type":                          "text/event-stream",
		})

		require.Equal(t, "req_abc", header.Get("request-id"))
		require.Equal(t, "req_abc", header.Get("Request-Id"))
		require.Equal(t, "true", header.Get("X-Should-Retry"))
		require.Equal(t, "allowed", header.Get("anthropic-ratelimit-unified-5h-status"))
		require.Equal(t, "text/event-stream", header.Get("Content-Type"))
	})

	t.Run("空输入返回空 header 而不是 nil", func(t *testing.T) {
		require.NotNil(t, CanonicalizeHeaders(nil))
	})

	// Set-Cookie 在 meta.Headers 里被压成单个 string，必须还原成多条，
	// 否则下游只会看到一条拼接后的畸形 cookie。
	t.Run("Set-Cookie 按原版语义拆回多条", func(t *testing.T) {
		header := CanonicalizeHeaders(map[string]string{
			"set-cookie": "a=1; Path=/, b=2; Path=/; Expires=Wed, 21 Oct 2026 07:28:00 GMT",
		})

		cookies := header.Values("Set-Cookie")
		require.Len(t, cookies, 2)
		require.Equal(t, "a=1; Path=/", cookies[0])
		require.Contains(t, cookies[1], "b=2")
		require.Contains(t, cookies[1], "Expires=Wed, 21 Oct 2026 07:28:00 GMT", "Expires 里的逗号不能被当成分隔符")
	})

	t.Run("单条 Set-Cookie 原样保留", func(t *testing.T) {
		header := CanonicalizeHeaders(map[string]string{"set-cookie": "a=1; Path=/"})

		require.Equal(t, []string{"a=1; Path=/"}, header.Values("Set-Cookie"))
	})
}

// 链路事实：CC 伪装发 Accept-Encoding: gzip, deflate, br, zstd ⇒ Anthropic 一定压缩；
// reclaude 原样装箱；而外层 Content-Encoding 是 octet-stream 的信封，
// 没有任何一层会解这个内层压缩。
//
// 不解的后果：SSE 扫到二进制 ⇒ usage 解析不到（计费没有第二道防线）+ 下游收到乱码。
func TestDecompressInnerBody(t *testing.T) {
	gzipped := func(t *testing.T, payload string) []byte {
		t.Helper()
		var buf bytes.Buffer
		writer := gzip.NewWriter(&buf)
		_, err := writer.Write([]byte(payload))
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		return buf.Bytes()
	}

	brotlied := func(t *testing.T, payload string) []byte {
		t.Helper()
		var buf bytes.Buffer
		writer := brotli.NewWriter(&buf)
		_, err := writer.Write([]byte(payload))
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		return buf.Bytes()
	}

	const payload = "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n"

	t.Run("gzip 被解开且编码头被清掉", func(t *testing.T) {
		// Arrange
		resp := &http.Response{
			StatusCode:    200,
			Header:        http.Header{"Content-Encoding": []string{"gzip"}, "Content-Length": []string{"123"}},
			Body:          io.NopCloser(bytes.NewReader(gzipped(t, payload))),
			ContentLength: 123,
		}

		// Act
		require.NoError(t, DecompressInnerBody(resp))

		// Assert
		got, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, payload, string(got))
		require.Empty(t, resp.Header.Get("Content-Encoding"))
		require.Empty(t, resp.Header.Get("Content-Length"))
		require.EqualValues(t, -1, resp.ContentLength)
	})

	t.Run("br 被解开", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Encoding": []string{"br"}},
			Body:       io.NopCloser(bytes.NewReader(brotlied(t, payload))),
		}

		require.NoError(t, DecompressInnerBody(resp))

		got, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, payload, string(got))
	})

	t.Run("无 Content-Encoding 时原样放行", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(bytes.NewReader([]byte(payload))),
		}

		require.NoError(t, DecompressInnerBody(resp))

		got, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, payload, string(got))
	})

	t.Run("未知编码报错而不是把二进制喂给下游", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Encoding": []string{"exotic"}},
			Body:       io.NopCloser(bytes.NewReader([]byte("binary"))),
		}

		require.Error(t, DecompressInnerBody(resp))
	})

	// 解压包装必须把原始 body 也关掉，否则 httpUpstream 的 inFlight 计数泄漏，
	// 而 inFlight > 0 的 client entry 永不被淘汰，最终撞上游连接数上限。
	t.Run("Close 透传到原始 body", func(t *testing.T) {
		tracked := &trackedCloser{Reader: bytes.NewReader(gzipped(t, payload))}
		resp := &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Encoding": []string{"gzip"}},
			Body:       tracked,
		}
		require.NoError(t, DecompressInnerBody(resp))

		require.NoError(t, resp.Body.Close())

		require.True(t, tracked.closed, "原始 body 没有被关闭 ⇒ inFlight 泄漏")
	})
}

type trackedCloser struct {
	io.Reader
	closed bool
}

func (t *trackedCloser) Close() error {
	t.closed = true
	return nil
}
