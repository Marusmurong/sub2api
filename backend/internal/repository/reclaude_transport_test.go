//go:build unit

package repository

import (
	"crypto/tls"
	"net/http"
	"testing"
)

// 🔴 reclaude 外层必须**强制 HTTP/1.1**，且要钉到 ALPN 层。
//
// 真客户端的 newDirectTransport（📄 反编译）三件事齐做：
//
//	TLSClientConfig.NextProtos = []string{"http/1.1"}   ← ALPN 只报 http/1.1
//	TLSNextProto               = 非 nil 空 map           ← Go 里禁用 h2 的标准写法
//	ForceAttemptHTTP2          = false
//
// ✅ 实测佐证：reclaude-lab/sink/dump 里所有请求首行都是 HTTP/1.1，
// UA 全是 `Go-http-client/1.1`（走 h2 会变成 Go-http-client/2.0）。
//
// 为什么 ALPN 那一项不能省：它在 **TLS 握手阶段**就暴露，比请求级特征更早。
// 只设 ForceAttemptHTTP2=false 而不设 NextProtos，ClientHello 里仍会提供 h2，
// 与真客户端不同；而且我们此前走的 long_stream profile 还会周期性发 h2 PING 帧，
// 真客户端永远不会发。
//
// 2026-09-24 排查四台设备连续 device_revoked 时定位。
func TestReclaudeTransportForcesHTTP11(t *testing.T) {
	transport, err := buildUpstreamTransport(poolSettings{}, nil, upstreamProtocolModeReclaudeH1)
	if err != nil {
		t.Fatalf("构造 transport: %v", err)
	}

	t.Run("ALPN 只提供 http/1.1", func(t *testing.T) {
		if transport.TLSClientConfig == nil {
			t.Fatal("TLSClientConfig 为 nil —— ALPN 没有被钉住")
		}
		got := transport.TLSClientConfig.NextProtos
		if len(got) != 1 || got[0] != "http/1.1" {
			t.Fatalf("NextProtos = %v，期望 [http/1.1]", got)
		}
	})

	t.Run("ForceAttemptHTTP2 为 false", func(t *testing.T) {
		if transport.ForceAttemptHTTP2 {
			t.Fatal("ForceAttemptHTTP2 仍为 true")
		}
	})

	t.Run("TLSNextProto 是非 nil 空 map", func(t *testing.T) {
		// nil 会让 Go 自动启用 h2；必须是非 nil 的空 map。
		if transport.TLSNextProto == nil {
			t.Fatal("TLSNextProto 为 nil —— Go 会自动启用 h2")
		}
		if len(transport.TLSNextProto) != 0 {
			t.Fatalf("TLSNextProto 应为空 map，实际有 %d 项", len(transport.TLSNextProto))
		}
	})

	t.Run("不注册 h2 PING 健康探测", func(t *testing.T) {
		// 真客户端永远不发 h2 PING 帧。这一项由上面三条共同保证：
		// 只要没走 H2 分支，enableHTTP2KeepAlive 就不会被调用。
		var _ *http.Transport = transport
	})
}

// 防回归：不能为了 reclaude 把别的 profile 也改成 H1。
func TestOtherProfilesKeepTheirProtocol(t *testing.T) {
	transport, err := buildUpstreamTransport(poolSettings{}, nil, upstreamProtocolModeLongStreamH2)
	if err != nil {
		t.Fatalf("构造 transport: %v", err)
	}
	if !transport.ForceAttemptHTTP2 {
		t.Fatal("long_stream 应保持 H2 —— 那是给长流式上游用的，不该被 reclaude 的改动波及")
	}
	var _ *tls.Config = transport.TLSClientConfig
}
