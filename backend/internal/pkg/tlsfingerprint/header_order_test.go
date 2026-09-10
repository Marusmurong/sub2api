package tlsfingerprint

import (
	"bytes"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeConn 记录所有写入的字节。
type fakeConn struct {
	net.Conn
	buf bytes.Buffer
}

func (f *fakeConn) Write(p []byte) (int, error)      { return f.buf.Write(p) }
func (f *fakeConn) Close() error                     { return nil }
func (f *fakeConn) LocalAddr() net.Addr              { return nil }
func (f *fakeConn) RemoteAddr() net.Addr             { return nil }
func (f *fakeConn) SetDeadline(time.Time) error      { return nil }
func (f *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (f *fakeConn) SetWriteDeadline(time.Time) error { return nil }

func headerNames(wire string) []string {
	head := strings.SplitN(wire, "\r\n\r\n", 2)[0]
	lines := strings.Split(head, "\r\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines[1:] {
		if i := strings.IndexByte(l, ':'); i > 0 {
			out = append(out, l[:i])
		}
	}
	return out
}

// Go 写出来的头(Host/User-Agent/Content-Length 提前 + 其余 ASCII 排序)
// 必须被重排成真实 Claude Code 2.1.263 的顺序与大小写。
func TestHeaderOrderRewrite(t *testing.T) {
	goStyle := "POST /v1/messages?beta=true HTTP/1.1\r\n" +
		"Host: api.anthropic.com\r\n" +
		"User-Agent: claude-cli/2.1.263 (external, cli)\r\n" +
		"Content-Length: 2\r\n" +
		"Accept: application/json\r\n" +
		"Accept-Encoding: gzip, deflate, br, zstd\r\n" +
		"Connection: keep-alive\r\n" +
		"X-Claude-Code-Session-Id: s\r\n" +
		"X-Stainless-Arch: x64\r\n" +
		"X-Stainless-Lang: js\r\n" +
		"X-Stainless-OS: Windows\r\n" +
		"X-Stainless-Package-Version: 0.112.1\r\n" +
		"X-Stainless-Retry-Count: 0\r\n" +
		"X-Stainless-Runtime: node\r\n" +
		"X-Stainless-Runtime-Version: v26.3.0\r\n" +
		"X-Stainless-Timeout: 600\r\n" +
		"anthropic-beta: claude-code-20250219\r\n" +
		"anthropic-dangerous-direct-browser-access: true\r\n" +
		"anthropic-version: 2023-06-01\r\n" +
		"authorization: Bearer x\r\n" +
		"content-type: application/json\r\n" +
		"x-app: cli\r\n" +
		"\r\nhi"

	f := &fakeConn{}
	c := newHeaderOrderConn(f)
	if _, err := c.Write([]byte(goStyle)); err != nil {
		t.Fatal(err)
	}
	got := headerNames(f.buf.String())
	want := []string{
		"Accept", "Authorization", "Content-Type", "User-Agent",
		"X-Claude-Code-Session-Id",
		"X-Stainless-Arch", "X-Stainless-Lang", "X-Stainless-OS",
		"X-Stainless-Package-Version", "X-Stainless-Retry-Count",
		"X-Stainless-Runtime", "X-Stainless-Runtime-Version", "X-Stainless-Timeout",
		"anthropic-beta", "anthropic-dangerous-direct-browser-access", "anthropic-version",
		"x-app",
		"Connection", "Host", "Accept-Encoding", "Content-Length",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("头顺序不符\n实得: %v\n期望: %v", got, want)
	}
	if !strings.HasSuffix(f.buf.String(), "\r\n\r\nhi") {
		t.Fatalf("body 必须原样跟在头块之后, got tail %q", f.buf.String()[len(f.buf.String())-12:])
	}
	if !strings.HasPrefix(f.buf.String(), "POST /v1/messages?beta=true HTTP/1.1\r\n") {
		t.Fatal("请求行不得改动")
	}
}

// keep-alive:同一条连接上的第二个请求同样要被重排。
func TestHeaderOrderRewriteAcrossKeepAlive(t *testing.T) {
	one := "POST /a HTTP/1.1\r\nHost: h\r\nContent-Length: 3\r\nAccept: application/json\r\n\r\nabc"
	f := &fakeConn{}
	c := newHeaderOrderConn(f)
	for i := 0; i < 2; i++ {
		if _, err := c.Write([]byte(one)); err != nil {
			t.Fatal(err)
		}
	}
	// 重排后应为 Accept → Host → Content-Length(运行时追加组收尾)
	if n := strings.Count(f.buf.String(), "Accept: application/json\r\nHost: h\r\nContent-Length: 3"); n != 2 {
		t.Fatalf("两个请求都应被重排, got %d\n%q", n, f.buf.String())
	}
}

// 头块跨多次 Write 到达时也要正确拼接。
func TestHeaderOrderRewriteSplitWrites(t *testing.T) {
	full := "POST /a HTTP/1.1\r\nHost: h\r\nContent-Length: 2\r\nAccept: application/json\r\n\r\nhi"
	f := &fakeConn{}
	c := newHeaderOrderConn(f)
	for _, part := range []string{full[:20], full[20:40], full[40:]} {
		if _, err := c.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.HasSuffix(f.buf.String(), "\r\n\r\nhi") {
		t.Fatalf("分片写入后 body 丢失: %q", f.buf.String())
	}
	if !strings.Contains(f.buf.String(), "Accept: application/json\r\nHost: h\r\n") {
		t.Fatalf("分片写入未被正确重排: %q", f.buf.String())
	}
}

// Transfer-Encoding: chunked 时 body 边界不可知,必须原样透传、不改写。
func TestHeaderOrderPassthroughOnChunked(t *testing.T) {
	in := "POST /a HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\nAccept: application/json\r\n\r\n0\r\n\r\n"
	f := &fakeConn{}
	c := newHeaderOrderConn(f)
	if _, err := c.Write([]byte(in)); err != nil {
		t.Fatal(err)
	}
	if f.buf.String() != in {
		t.Fatalf("chunked 必须逐字透传\n实得 %q", f.buf.String())
	}
}

// Write 的返回值必须是入参长度,否则 bufio.Writer 会当成短写。
func TestHeaderOrderWriteReturnsInputLength(t *testing.T) {
	in := []byte("POST /a HTTP/1.1\r\nHost: h\r\nContent-Length: 0\r\nAccept: application/json\r\n\r\n")
	f := &fakeConn{}
	c := newHeaderOrderConn(f)
	n, err := c.Write(in)
	if err != nil || n != len(in) {
		t.Fatalf("n=%d err=%v, want n=%d", n, err, len(in))
	}
}

// 名单外的头保持相对顺序,并排在"运行时追加组"之前。
func TestHeaderOrderKeepsUnknownHeadersBeforeTrailingGroup(t *testing.T) {
	in := "POST /a HTTP/1.1\r\nHost: h\r\nContent-Length: 0\r\nX-Custom-A: 1\r\nX-Custom-B: 2\r\nAccept: application/json\r\n\r\n"
	f := &fakeConn{}
	c := newHeaderOrderConn(f)
	if _, err := c.Write([]byte(in)); err != nil {
		t.Fatal(err)
	}
	got := headerNames(f.buf.String())
	want := []string{"Accept", "X-Custom-A", "X-Custom-B", "Host", "Content-Length"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("实得 %v 期望 %v", got, want)
	}
}
