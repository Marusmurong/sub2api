package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// 🔴 daemon 模式：把内层请求交给**本机运行的真 reclaude 客户端**去转发，
// 由它封信封、签名、打 rec 网关。我们只做 CC 伪装（billing block / 指纹 /
// beta 模板），那本来就是 sub2api 的本职。
//
// 存在的理由：自研信封实现至今过不了 rec 的 bad_envelope 校验 ——
// 签名、sha256、base64、二进制结构逐项验证都正确，同一串字节离线重放能拿 200，
// 经 sub2api 发出却被拒（2026-09-24 排查记录）。daemon 模式绕开这一整层，
// 且不会因对方改协议而再次失效。
//
// 代价：每个 reclaude 账号需要一个常驻客户端进程。
func TestReclaudeDaemonEndpoint(t *testing.T) {
	newAccount := func(extra map[string]any) *Account {
		return &Account{ID: 1, Type: AccountTypeReclaude, Platform: PlatformAnthropic, Extra: extra}
	}

	t.Run("配置了 daemon 代理时返回它", func(t *testing.T) {
		account := newAccount(map[string]any{
			ExtraKeyReclaudeDaemonProxy: "http://127.0.0.1:36159",
		})

		proxy, ok := ReclaudeDaemonProxy(account)

		require.True(t, ok)
		require.Equal(t, "http://127.0.0.1:36159", proxy)
	})

	t.Run("未配置时走自研信封路径", func(t *testing.T) {
		_, ok := ReclaudeDaemonProxy(newAccount(map[string]any{}))

		require.False(t, ok, "缺省必须保持既有行为，不能静默改变已有账号的链路")
	})

	t.Run("只接受回环地址", func(t *testing.T) {
		// daemon 是本机进程。允许任意地址 = 把带完整凭据的请求发给任意主机，
		// 而这条链路上的 body 是全量明文 prompt。
		for _, bad := range []string{
			"http://10.0.0.5:36159",
			"http://evil.example.com:36159",
			"https://api.anthropic.com",
		} {
			_, ok := ReclaudeDaemonProxy(newAccount(map[string]any{
				ExtraKeyReclaudeDaemonProxy: bad,
			}))

			require.Falsef(t, ok, "非回环地址 %s 不该被接受", bad)
		}
	})

	t.Run("localhost 与 127.0.0.1 都接受", func(t *testing.T) {
		for _, good := range []string{
			"http://127.0.0.1:36159",
			"http://localhost:36159",
			"http://[::1]:36159",
		} {
			_, ok := ReclaudeDaemonProxy(newAccount(map[string]any{
				ExtraKeyReclaudeDaemonProxy: good,
			}))

			require.Truef(t, ok, "回环地址 %s 该被接受", good)
		}
	})

	t.Run("非 reclaude 账号一律不启用", func(t *testing.T) {
		oauth := &Account{ID: 2, Type: AccountTypeOAuth, Platform: PlatformAnthropic,
			Extra: map[string]any{ExtraKeyReclaudeDaemonProxy: "http://127.0.0.1:36159"}}

		_, ok := ReclaudeDaemonProxy(oauth)

		require.False(t, ok)
	})
}

// 🔴 daemon 模式要求内层请求的 URL 带 scheme。
//
// 2026-09-24 实测：sub2api 构造的内层请求 URL 是 `//api.anthropic.com/v1/messages`
// （无 scheme）。信封路径不受影响 —— 它只把 URL 当字符串塞进 meta；但把 daemon
// 当正向代理时，Go 会据此发出 `CONNECT //api.anthropic.com:443`（两个斜杠），
// daemon 收到畸形目标后封装失败，回 400 bad_envelope。
//
// 这个错误极具误导性：它长得像信封格式问题，实际是 URL 少了 "https:"。
func TestNormalizeInnerRequestURLForDaemon(t *testing.T) {
	t.Run("补齐缺失的 scheme", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, "//api.anthropic.com/v1/messages?beta=true", nil)
		require.NoError(t, err)
		require.Empty(t, req.URL.Scheme, "前提：这正是 sub2api 构造出的形态")

		normalizeInnerRequestURLForDaemon(req)

		require.Equal(t, "https", req.URL.Scheme)
		require.Equal(t, "https://api.anthropic.com/v1/messages?beta=true", req.URL.String())
	})

	t.Run("已有 scheme 时不改动", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)

		normalizeInnerRequestURLForDaemon(req)

		require.Equal(t, "https://api.anthropic.com/v1/messages", req.URL.String())
	})

	t.Run("http 不被升级（保持调用方意图）", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:1234/x", nil)

		normalizeInnerRequestURLForDaemon(req)

		require.Equal(t, "http", req.URL.Scheme)
	})

	t.Run("nil 安全", func(t *testing.T) {
		require.NotPanics(t, func() { normalizeInnerRequestURLForDaemon(nil) })
	})
}

// daemon 有两个口，用途不同（~/.reclaude/state.json）：
//
//	port             正向代理口，走 CONNECT —— 实测经它转发会被回 bad_envelope
//	transparent_port TLS 直连口，Claude Code 本体就是打这个（其二进制里硬编码）
//
// 所以 daemon 模式必须走 transparent 口：把请求直接发到它，SNI/Host 仍是
// api.anthropic.com，由 daemon 终止 TLS 后封信封转发。
func TestReclaudeDaemonEndpointIsDirectTLS(t *testing.T) {
	account := &Account{ID: 1, Type: AccountTypeReclaude, Platform: PlatformAnthropic,
		Extra: map[string]any{ExtraKeyReclaudeDaemonEndpoint: "https://127.0.0.1:40997"}}

	t.Run("返回 transparent 端点", func(t *testing.T) {
		endpoint, ok := ReclaudeDaemonEndpoint(account)

		require.True(t, ok)
		require.Equal(t, "https://127.0.0.1:40997", endpoint)
	})

	t.Run("把内层请求改写到该端点，但保留 Host", func(t *testing.T) {
		// 🔴 Host 必须留 api.anthropic.com：daemon 靠它判断转发目标，
		// 改成 127.0.0.1 会让它不知道这个请求该发去哪。
		req, err := http.NewRequest(http.MethodPost,
			"//api.anthropic.com/v1/messages?beta=true", nil)
		require.NoError(t, err)

		require.NoError(t, retargetToReclaudeDaemon(req, "https://127.0.0.1:40997"))

		require.Equal(t, "https", req.URL.Scheme)
		require.Equal(t, "127.0.0.1:40997", req.URL.Host)
		require.Equal(t, "/v1/messages", req.URL.Path)
		require.Equal(t, "beta=true", req.URL.RawQuery)
		require.Equal(t, "api.anthropic.com", req.Host, "Host 头必须仍指向真实目标")
	})

	t.Run("端点非回环时拒绝", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "//api.anthropic.com/v1/messages", nil)

		require.Error(t, retargetToReclaudeDaemon(req, "https://10.0.0.5:40997"))
	})
}
