//go:build unit

package tlsfingerprint

import (
	"context"
	"encoding/binary"
	"net"
	"reflect"
	"testing"
	"time"
)

// 真实 Claude Code 2.1.220 的 ClientHello 基准值。
//
// 采集方式（2026-08-02，无需 root）：起一个普通 TCP 监听读取首包，用
// `claude --settings <临时json> -p hi` 把 ANTHROPIC_BASE_URL 指过去。
// ClientHello 是明文首条记录，证书校验之前就已发出，握手失败不影响取值。
//
// 被测客户端是 Bun 1.4.0 编译的 macOS arm64 原生二进制
// （/Users/<u>/.local/share/claude/versions/2.1.220，Mach-O，非 Node 脚本），
// TLS 走 BoringSSL。注意 npm 安装的同版本仍是 Node 跑的 JS，指纹不同——
// 本基准对应原生二进制，也就是官方默认安装路径。
var (
	claudeCode2_1_220Ciphers = []uint16{
		4865, 4866, 4867, 49195, 49199, 49196, 49200, 52393, 52392,
		49161, 49171, 49162, 49172, 156, 157, 47, 53,
	}
	claudeCode2_1_220Extensions = []uint16{0, 23, 65281, 10, 11, 35, 16, 5, 13, 18, 51, 45, 43, 21}
	claudeCode2_1_220Curves     = []uint16{29, 23, 24}
)

// claudeCodeProfile 是与上述基准对应的 profile 配置。
// 生产库里的 id=2 应与之保持一致。
func claudeCodeProfile() *Profile {
	return &Profile{
		Name:              "ClaudeCode_2.1.220_macOS_arm64_Bun",
		CipherSuites:      append([]uint16(nil), claudeCode2_1_220Ciphers...),
		Curves:            append([]uint16(nil), claudeCode2_1_220Curves...),
		PointFormats:      []uint16{0},
		EnableGREASE:      false,
		ALPNProtocols:     []string{"http/1.1"},
		SupportedVersions: []uint16{0x0304, 0x0303},
		KeyShareGroups:    []uint16{29},
		Extensions:        append([]uint16(nil), claudeCode2_1_220Extensions...),
	}
}

// captureClientHello 起一个本地监听，让 dialer 连过来，返回它真实发出的首包。
func captureClientHello(t *testing.T, profile *Profile) []byte {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer func() { _ = ln.Close() }()

	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			ch <- result{err: aerr}
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 16384)
		n, rerr := conn.Read(buf)
		ch <- result{data: buf[:n], err: rerr}
	}()

	// baseDialer 把连接重定向到本地监听，但 DialTLSContext 仍收到真实主机名——
	// SNI 与 ClientHello 长度因此都贴近生产实际。这一点很关键：padding(21) 是否
	// 出现由长度决定，用 IP 拨号会得到一个短到不触发 padding 的假阴性。
	base := func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, ln.Addr().String())
	}
	dialer := NewDialer(profile, base)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// 握手必然失败（对端不是 TLS 服务端），我们只要它发出的 ClientHello。
	if conn, derr := dialer.DialTLSContext(ctx, "tcp", "api.anthropic.com:443"); derr == nil {
		_ = conn.Close()
	}

	select {
	case r := <-ch:
		if r.err != nil && len(r.data) == 0 {
			t.Fatalf("读取 ClientHello 失败: %v", r.err)
		}
		return r.data
	case <-time.After(6 * time.Second):
		t.Fatal("等待 ClientHello 超时")
		return nil
	}
}

type parsedHello struct {
	ciphers    []uint16
	extensions []uint16
	curves     []uint16
	alpn       []string
}

// parseHello 解析 ClientHello 的记录层与握手层，取出指纹相关字段。
func parseHello(t *testing.T, payload []byte) parsedHello {
	t.Helper()
	if len(payload) < 6 || payload[0] != 0x16 || payload[5] != 0x01 {
		t.Fatalf("不是 ClientHello：前 6 字节 %x", payload[:min(6, len(payload))])
	}
	b := payload[5:]
	p := 6 + 32        // handshake 头 + version + random
	p += 1 + int(b[p]) // session id
	csLen := int(binary.BigEndian.Uint16(b[p : p+2]))
	p += 2
	out := parsedHello{}
	for i := 0; i < csLen; i += 2 {
		out.ciphers = append(out.ciphers, binary.BigEndian.Uint16(b[p+i:p+i+2]))
	}
	p += csLen
	p += 1 + int(b[p]) // compression methods
	extTotal := int(binary.BigEndian.Uint16(b[p : p+2]))
	p += 2
	end := p + extTotal
	for p+4 <= end && p+4 <= len(b) {
		etype := binary.BigEndian.Uint16(b[p : p+2])
		elen := int(binary.BigEndian.Uint16(b[p+2 : p+4]))
		p += 4
		data := b[p:min(p+elen, len(b))]
		p += elen
		out.extensions = append(out.extensions, etype)
		switch etype {
		case 10: // supported_groups
			n := int(binary.BigEndian.Uint16(data[:2]))
			for i := 0; i < n; i += 2 {
				out.curves = append(out.curves, binary.BigEndian.Uint16(data[2+i:4+i]))
			}
		case 16: // alpn
			q := 2
			for q < len(data) {
				ln := int(data[q])
				out.alpn = append(out.alpn, string(data[q+1:q+1+ln]))
				q += 1 + ln
			}
		}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// dialer 真实发出的 ClientHello 必须与真实 Claude Code 2.1.220 逐项一致。
//
// 这条测的是**实际字节**而不是 profile 字段，因为两者会脱节：padding(21) 就是
// 一个例子——profile 里写上 21，若 dialer 没有对应 case，发出去的是长度为 0 的
// 空扩展，字段看着对、字节其实错。
func TestClientHelloMatchesRealClaudeCode(t *testing.T) {
	hello := parseHello(t, captureClientHello(t, claudeCodeProfile()))

	if !reflect.DeepEqual(hello.ciphers, claudeCode2_1_220Ciphers) {
		t.Errorf("密码套件不一致\n实际 %v\n期望 %v", hello.ciphers, claudeCode2_1_220Ciphers)
	}
	if !reflect.DeepEqual(hello.extensions, claudeCode2_1_220Extensions) {
		t.Errorf("扩展序列不一致（JA3 对顺序敏感）\n实际 %v\n期望 %v", hello.extensions, claudeCode2_1_220Extensions)
	}
	if !reflect.DeepEqual(hello.curves, claudeCode2_1_220Curves) {
		t.Errorf("曲线不一致\n实际 %v\n期望 %v", hello.curves, claudeCode2_1_220Curves)
	}
	if !reflect.DeepEqual(hello.alpn, []string{"http/1.1"}) {
		t.Errorf("ALPN 不一致：实际 %v，期望 [http/1.1]（Claude Code 未开启 allowH2）", hello.alpn)
	}
}
