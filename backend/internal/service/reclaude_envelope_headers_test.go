package service

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// 🔴 2026-09-24 抓真实客户端信封实测：内层 header 少这两个会被网关拒为
// `{"code":"bad_envelope"}`。
//
// 排查路径值得记下来：先换成主域 www.reclaude.ai 让 device_signature_required
// 消失（证明签名本身是对的，之前是打错端点），才暴露出下一层的 bad_envelope。
// 随后用真实客户端把 gateway 指向本地 sink，抓到 /v1/messages 的完整信封逐字段比对 ——
// 信封二进制结构、六个顶层字段、base64 变体全部一致，差异只在这两个头。
func TestBuildReclaudeEnvelopeIncludesRequiredClientHeaders(t *testing.T) {
	newInner := func() *http.Request {
		req, err := http.NewRequest(http.MethodPost,
			"https://api.anthropic.com/v1/messages?beta=true", nil)
		require.NoError(t, err)
		req.Header.Set("content-type", "application/json")
		return req
	}

	t.Run("补齐 x-claude-code-request-class", func(t *testing.T) {
		envelope, _, err := buildReclaudeEnvelope(newInner(), "https://www.reclaude.ai")

		require.NoError(t, err)
		require.Contains(t, string(envelope), `"x-claude-code-request-class":"main"`)
	})

	t.Run("补齐 x-client-request-id 且是 uuid", func(t *testing.T) {
		envelope, _, err := buildReclaudeEnvelope(newInner(), "https://www.reclaude.ai")

		require.NoError(t, err)
		require.Contains(t, string(envelope), `"x-client-request-id":"`)

		meta := extractReclaudeEnvelopeMetaForTest(t, envelope)
		id, _ := meta["headers"].(map[string]any)["x-client-request-id"].(string)
		require.Len(t, id, 36, "应为 uuid 形态")
	})

	t.Run("每次请求的 x-client-request-id 都不同", func(t *testing.T) {
		// 固定值等于给所有请求盖同一个戳，是最直接的同源特征。
		first, _, err := buildReclaudeEnvelope(newInner(), "https://www.reclaude.ai")
		require.NoError(t, err)
		second, _, err := buildReclaudeEnvelope(newInner(), "https://www.reclaude.ai")
		require.NoError(t, err)

		m1 := extractReclaudeEnvelopeMetaForTest(t, first)
		m2 := extractReclaudeEnvelopeMetaForTest(t, second)
		require.NotEqual(t,
			m1["headers"].(map[string]any)["x-client-request-id"],
			m2["headers"].(map[string]any)["x-client-request-id"])
	})

	t.Run("客户端已带这两个头时不覆盖", func(t *testing.T) {
		// 下游真是 Claude Code 时，它自己的值才是真的。
		inner := newInner()
		inner.Header.Set("x-claude-code-request-class", "background")
		inner.Header.Set("x-client-request-id", "11111111-2222-3333-4444-555555555555")

		envelope, _, err := buildReclaudeEnvelope(inner, "https://www.reclaude.ai")

		require.NoError(t, err)
		meta := extractReclaudeEnvelopeMetaForTest(t, envelope)
		headers := meta["headers"].(map[string]any)
		require.Equal(t, "background", headers["x-claude-code-request-class"])
		require.Equal(t, "11111111-2222-3333-4444-555555555555", headers["x-client-request-id"])
	})
}

func extractReclaudeEnvelopeMetaForTest(t *testing.T, envelope []byte) map[string]any {
	t.Helper()
	require.Greater(t, len(envelope), 4)
	n := int(envelope[0])<<24 | int(envelope[1])<<16 | int(envelope[2])<<8 | int(envelope[3])
	require.LessOrEqual(t, 4+n, len(envelope))

	var meta map[string]any
	require.NoError(t, json.Unmarshal(envelope[4:4+n], &meta))
	return meta
}

// 🔴 edge 必须是**实际发往的网关主机名**，不是常量。
//
// 逆向报告 §7.2 写明 `Edge = 网关节点标签（la.route.reclaude.ai）`，
// 而我们早先抓到的样本恰好是客户端刚启动、尚未选定节点时的 "unknown"，
// 被误当成常量写死。2026-09-24 用 RECLAUDE_GATEWAY_DIAL_ADDR 把 daemon 指向
// 本地 sink 后抓到真实报文：`"edge":"www.reclaude.ai"` —— 与它实际连接的
// 网关一致。
//
// edge 与实际网关不符，正是 `bad_envelope / reclaude state mismatch` 的字面含义。
func TestReclaudeEnvelopeEdgeMatchesGateway(t *testing.T) {
	t.Run("取网关主机名", func(t *testing.T) {
		require.Equal(t, "www.reclaude.ai", reclaudeEnvelopeEdgeFor("https://www.reclaude.ai"))
		require.Equal(t, "la.route.reclaude.ai", reclaudeEnvelopeEdgeFor("https://la.route.reclaude.ai"))
	})

	t.Run("带端口时只取主机名", func(t *testing.T) {
		require.Equal(t, "www.reclaude.ai", reclaudeEnvelopeEdgeFor("https://www.reclaude.ai:443"))
	})

	t.Run("解析失败时回落 unknown", func(t *testing.T) {
		// 客户端未选定节点时确实发 unknown（早期抓包样本），这是合法回落值。
		require.Equal(t, "unknown", reclaudeEnvelopeEdgeFor(""))
		require.Equal(t, "unknown", reclaudeEnvelopeEdgeFor("::not a url::"))
	})
}
