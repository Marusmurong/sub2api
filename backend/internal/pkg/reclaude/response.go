package reclaude

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// CanonicalizeHeaders 把信封元数据里的**全小写** header map 还原成标准 http.Header。
//
// 不做这一步的话，下面这些读取全部返回空值，而且都是静默失败：
//   - upstreamRequestID(resp.Header) → RequestID 恒空，cc_prev_req 链断
//   - X-Should-Retry → 恒 false，**瞬时 429 被误判为配额耗尽**
//   - anthropic-ratelimit-unified-5h-status → 5h 窗口永不更新
//   - Content-Type → 非流式响应类型丢失
func CanonicalizeHeaders(headers map[string]string) http.Header {
	out := make(http.Header, len(headers))
	for name, value := range headers {
		canonical := textproto.CanonicalMIMEHeaderKey(name)
		if canonical == "Set-Cookie" {
			for _, cookie := range splitSetCookieHeader(value) {
				out.Add(canonical, cookie)
			}
			continue
		}
		out.Add(canonical, value)
	}
	return out
}

// splitSetCookieHeader 把被压成单个 string 的多条 Set-Cookie 拆回去。
//
// 不能简单按逗号切：Expires 属性里本来就带逗号（"Wed, 21 Oct 2026 ..."）。
// 判据是逗号后面要出现 "<token>=" 才算新的一条。
func splitSetCookieHeader(value string) []string {
	if value == "" {
		return nil
	}

	var cookies []string
	start := 0
	for i := 0; i < len(value); i++ {
		if value[i] != ',' {
			continue
		}
		if !startsNewCookie(value[i+1:]) {
			continue
		}
		cookies = append(cookies, strings.TrimSpace(value[start:i]))
		start = i + 1
	}
	cookies = append(cookies, strings.TrimSpace(value[start:]))
	return cookies
}

// startsNewCookie 判断逗号之后是不是一条新 cookie（形如 " name=value"）。
func startsNewCookie(rest string) bool {
	rest = strings.TrimLeft(rest, " \t")
	equals := strings.IndexByte(rest, '=')
	if equals <= 0 {
		return false
	}
	name := rest[:equals]
	if strings.ContainsAny(name, ";, ") {
		return false
	}
	return true
}

// DecompressInnerBody 解开**内层**响应压缩。
//
// 为什么需要单独做一遍：sub2api 的 CC 伪装会发
// Accept-Encoding: gzip, deflate, br, zstd ⇒ Anthropic 一定压缩；reclaude 把响应
// 原样装箱；而 httpUpstream 自己的解压看的是**外层** Content-Encoding —— 外层是
// application/octet-stream 的信封，不带内层编码标记。结果就是合成 Response 的
// header 写着 content-encoding: br，却没有任何一层解它。
//
// 不解的后果不是报错而是静默劣化：SSE 扫到二进制 ⇒ usage 解析不到（计费这里
// 没有第二道防线）+ 下游客户端收到乱码。所以未知编码宁可报错。
func DecompressInnerBody(resp *http.Response) error {
	if resp == nil || resp.Body == nil {
		return nil
	}

	encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	if encoding == "" {
		return nil
	}

	original := resp.Body
	var reader io.Reader

	switch encoding {
	case "gzip":
		gz, err := gzip.NewReader(original)
		if err != nil {
			return fmt.Errorf("decompress inner body (gzip): %w", err)
		}
		reader = gz
	case "br":
		reader = brotli.NewReader(original)
	case "deflate":
		reader = flate.NewReader(original)
	case "zstd":
		buffered := bufio.NewReader(original)
		zr, err := zstd.NewReader(buffered)
		if err != nil {
			return fmt.Errorf("decompress inner body (zstd): %w", err)
		}
		reader = zr.IOReadCloser()
	default:
		return fmt.Errorf("decompress inner body: unsupported content-encoding %q", encoding)
	}

	resp.Body = &decompressedInnerBody{reader: reader, closer: original}
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Content-Length")
	resp.ContentLength = -1
	return nil
}

// decompressedInnerBody 把解压 reader 与原始 body 的 Close 绑在一起。
//
// Close 必须透传：原始 body 不关 ⇒ httpUpstream 的 inFlight 计数泄漏，
// 而 inFlight > 0 的 client entry 永不被淘汰，最终撞上游连接数上限。
type decompressedInnerBody struct {
	reader io.Reader
	closer io.Closer
}

func (b *decompressedInnerBody) Read(p []byte) (int, error) {
	return b.reader.Read(p)
}

func (b *decompressedInnerBody) Close() error {
	if closer, ok := b.reader.(io.Closer); ok {
		_ = closer.Close()
	}
	return b.closer.Close()
}
