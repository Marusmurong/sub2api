package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// 🔴 控制面请求**只带 5 个签名头**，不带 Client-Version / Client-Platform。
//
// ✅ 真值抓包（reclaude-lab/sink/dump/*client_*.txt）：
//
//	GET /client/intercept-domains HTTP/1.1
//	Accept-Encoding / Authorization / User-Agent
//	X-Reclaude-Body-Sha256 / -Device-Id / -Nonce / -Signature / -Ts
//
// `X-Reclaude-Client-Version` 与 `X-Reclaude-Client-Platform` **只出现在 /proxy**。
// 在控制面带上它们，是一个「实现者照着 /proxy 抄的」直接特征 ——
// 真客户端的两条代码路径本来就不同。
func TestProbeControlPlaneHeaderSet(t *testing.T) {
	account, cipher := probeAccount(t)
	upstream := &capturingUpstream{response: &http.Response{StatusCode: 200, Header: http.Header{}}}

	_, err := NewReclaudeGatewayProbe(upstream, cipher).
		ProbeReclaudeEndpoint(context.Background(), account, ReclaudeClientAccountPath)
	require.NoError(t, err)

	got := upstream.gotRequest.Header

	t.Run("五个签名头齐全", func(t *testing.T) {
		for _, name := range []string{
			"X-Reclaude-Device-Id", "X-Reclaude-Ts", "X-Reclaude-Nonce",
			"X-Reclaude-Body-Sha256", "X-Reclaude-Signature",
		} {
			require.NotEmptyf(t, got.Get(name), "缺签名头 %s", name)
		}
		require.NotEmpty(t, got.Get("Authorization"))
	})

	t.Run("不带 /proxy 专有的两个头", func(t *testing.T) {
		require.Empty(t, got.Get("X-Reclaude-Client-Version"),
			"控制面不该带 Client-Version（真值抓包里没有）")
		require.Empty(t, got.Get("X-Reclaude-Client-Platform"),
			"控制面不该带 Client-Platform（真值抓包里没有）")
	})
}
