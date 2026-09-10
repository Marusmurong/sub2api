package tlsfingerprint

import (
	"bytes"
	"net"
	"strconv"
	"strings"
)

// HTTP/1.1 请求头块的顺序与大小写重写。
//
// 为什么必须做：我们的 ClientHello 与真实 Claude Code 逐字节相同（profile 6，已用
// 2.1.263 Windows 抓包验证），但请求头块是 Go 的 net/http 写出来的——它把
// Host / User-Agent / Content-Length 特殊处理提到最前面，其余按 ASCII 排序。
// 于是上游看到的是「TLS 握手说 Bun/BoringSSL 的 Claude Code，紧接着的 HTTP 头块说
// Go net/http」，真实客户端产生不出这个组合。更要命的是它在**每一个账号上完全一致**，
// 是一把现成的跨账号关联键。
//
// 2026-09-10 逐行比对（左=真实抓包，右=我们）：
//
//	Accept                     |  Host
//	Authorization              |  User-Agent
//	Content-Type               |  Content-Length
//	User-Agent                 |  Accept
//	X-Claude-Code-Session-Id   |  Accept-Encoding
//	…                          |  Connection
//	x-app                      |  …
//	Connection / Host /        |  authorization / content-type   ← 大小写也不对
//	Accept-Encoding /          |  x-app
//	Content-Length             |  x-stainless-helper-method
//
// 做法：包一层 net.Conn，在**首次写入**时把请求头块按抓包顺序重排、修正大小写，
// 其余字节原样透传。不动 http.Transport，连接池、流式、超时、压缩行为全部不变。
//
// 包装点在 performTLSHandshake 的返回值上——即 TLS 握手**之后**，所以 HTTP 代理的
// CONNECT 前奏与 SOCKS5 协商都在包装之外，不会被误改。

// canonicalHeaderOrder 是真实 Claude Code 2.1.263 的请求头顺序与大小写。
// 来源：docs/captures/cc-2.1.263-windows-x64/headers.txt（客户本机抓样）。
//
// 末四项（Connection / Host / Accept-Encoding / Content-Length）是 Bun 的 HTTP 层
// 追加的，真实客户端把它们排在最后；Go 恰好把其中三项提到了最前面。
var canonicalHeaderOrder = []string{
	"Accept",
	"Authorization",
	"Content-Type",
	"User-Agent",
	"X-Claude-Code-Session-Id",
	"X-Stainless-Arch",
	"X-Stainless-Lang",
	"X-Stainless-OS",
	"X-Stainless-Package-Version",
	"X-Stainless-Retry-Count",
	"X-Stainless-Runtime",
	"X-Stainless-Runtime-Version",
	"X-Stainless-Timeout",
	"anthropic-beta",
	"anthropic-dangerous-direct-browser-access",
	"anthropic-version",
	"x-app",
	// 抓样是非流式的，没有这一项；流式时 SDK 会加。放在 x-app 之后是与其余
	// 小写 x-* 相邻的最保守猜测——重抓一次流式样本即可确认。
	"x-stainless-helper-method",
	"Connection",
	"Host",
	"Accept-Encoding",
	"Content-Length",
}

// canonicalIndex: 小写头名 → 在 canonicalHeaderOrder 中的位置。
var canonicalIndex = func() map[string]int {
	m := make(map[string]int, len(canonicalHeaderOrder))
	for i, k := range canonicalHeaderOrder {
		m[strings.ToLower(k)] = i
	}
	return m
}()

// trailingGroupStart 是「运行时追加组」在 canonicalHeaderOrder 中的起点。
// 名单外的头插在它之前，好让这一组始终收尾——真实客户端就是这个形状。
var trailingGroupStart = canonicalIndex["connection"]

const (
	stateHead = iota // 正在收集请求头块
	stateBody        // 头已写出，正在透传固定长度的 body
	statePass        // 放弃改写，此后完全透传
)

// maxHeadBytes 是请求头块的收集上限。超过就放弃改写转透传：
// 宁可让这条连接保持 Go 的原始顺序，也不要把一个畸形巨大的头缓冲在内存里。
const maxHeadBytes = 256 * 1024

// headerOrderConn 在写入方向重排 HTTP/1.1 请求头块；读取方向完全不碰。
type headerOrderConn struct {
	net.Conn
	state      int
	head       []byte
	bodyRemain int64
}

// newHeaderOrderConn 包装一条已完成 TLS 握手的连接。
func newHeaderOrderConn(c net.Conn) net.Conn {
	return &headerOrderConn{Conn: c, state: stateHead}
}

func (c *headerOrderConn) Write(p []byte) (int, error) {
	// 返回值恒为 len(p)：我们改写了字节数，但调用方（bufio.Writer）按"写了多少"
	// 判断是否成功，返回真实字节数会被当成短写。
	if err := c.consume(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *headerOrderConn) consume(p []byte) error {
	for len(p) > 0 {
		switch c.state {
		case statePass:
			return writeAll(c.Conn, p)

		case stateBody:
			n := int64(len(p))
			if n > c.bodyRemain {
				n = c.bodyRemain
			}
			if err := writeAll(c.Conn, p[:n]); err != nil {
				return err
			}
			p = p[n:]
			c.bodyRemain -= n
			if c.bodyRemain == 0 {
				// 一个请求收尾，重新等下一个头块（keep-alive 复用同一条连接）。
				c.state = stateHead
				c.head = c.head[:0]
			}

		case stateHead:
			c.head = append(c.head, p...)
			p = nil
			idx := bytes.Index(c.head, []byte("\r\n\r\n"))
			if idx < 0 {
				if len(c.head) > maxHeadBytes {
					buf := c.head
					c.head, c.state = nil, statePass
					return writeAll(c.Conn, buf)
				}
				return nil
			}
			head := c.head[:idx+4]
			rest := append([]byte(nil), c.head[idx+4:]...)
			c.head = c.head[:0]

			rewritten, contentLen, ok := rewriteRequestHead(head)
			if !ok {
				// 解析不了就原样发，并对这条连接彻底放弃改写：我们已经无法可靠地
				// 找到下一个头块的边界了。
				c.state = statePass
				if err := writeAll(c.Conn, head); err != nil {
					return err
				}
				p = rest
				continue
			}
			if err := writeAll(c.Conn, rewritten); err != nil {
				return err
			}
			if contentLen < 0 {
				// 没有 Content-Length（chunked 或无 body 语义未知）：头已按顺序发出，
				// 但后续 body 边界不可知，转透传。
				c.state = statePass
			} else {
				c.state = stateBody
				c.bodyRemain = contentLen
				if c.bodyRemain == 0 {
					c.state = stateHead
				}
			}
			p = rest
		}
	}
	return nil
}

func writeAll(w net.Conn, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}

// rewriteRequestHead 把一个完整的请求头块（含结尾 CRLFCRLF）按真实客户端顺序重排。
//
// 返回 ok=false 表示放弃改写（请求行缺失、出现 Transfer-Encoding、有折行续行等）；
// contentLen 为 -1 表示头里没有 Content-Length。
func rewriteRequestHead(head []byte) (out []byte, contentLen int64, ok bool) {
	lines := strings.Split(strings.TrimSuffix(string(head), "\r\n\r\n"), "\r\n")
	if len(lines) < 1 || lines[0] == "" {
		return nil, 0, false
	}
	requestLine := lines[0]

	type kv struct{ name, value string }
	fields := make([]kv, 0, len(lines))
	contentLen = -1
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			// obs-fold 续行：重排会打乱它与上一行的归属，放弃。
			return nil, 0, false
		}
		i := strings.IndexByte(line, ':')
		if i <= 0 {
			return nil, 0, false
		}
		name := line[:i]
		value := strings.TrimLeft(line[i+1:], " ")
		lower := strings.ToLower(name)
		if lower == "transfer-encoding" {
			// 分块传输时 body 长度不可知，本连接不改写。
			return nil, 0, false
		}
		if lower == "content-length" {
			n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil || n < 0 {
				return nil, 0, false
			}
			contentLen = n
		}
		fields = append(fields, kv{name: name, value: value})
	}

	// 稳定排序：名单内按抓包位置，名单外整体插在"运行时追加组"之前并保持原有相对顺序。
	pos := func(name string) int {
		if i, hit := canonicalIndex[strings.ToLower(name)]; hit {
			return i
		}
		return trailingGroupStart
	}
	ordered := make([]kv, 0, len(fields))
	for want := 0; want < len(canonicalHeaderOrder); want++ {
		for _, f := range fields {
			if pos(f.name) != want {
				continue
			}
			name := f.name
			if i, hit := canonicalIndex[strings.ToLower(name)]; hit {
				name = canonicalHeaderOrder[i] // 修正大小写
			}
			ordered = append(ordered, kv{name: name, value: f.value})
		}
	}
	if len(ordered) != len(fields) {
		return nil, 0, false // 不该发生；真发生就别改
	}

	var b strings.Builder
	b.Grow(len(head) + 32)
	b.WriteString(requestLine)
	b.WriteString("\r\n")
	for _, f := range ordered {
		b.WriteString(f.name)
		b.WriteString(": ")
		b.WriteString(f.value)
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	return []byte(b.String()), contentLen, true
}
