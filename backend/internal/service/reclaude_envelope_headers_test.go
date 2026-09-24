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

// 🔴 edge 恒为 "unknown"（撤回 2026-09-24 一次错误改动后的结论）。
//
// 真客户端的 computeEdgeLabel（📄 反编译）是一次 map 查表：命中返回 u.Host，
// 未命中返回 "unknown"。那张表装的是 route 节点，而 login 拿到的默认 gateway
// （www.reclaude.ai）不在其中。
//
// ✅ 实测：reclaude-lab/sink/dump 的 26/26 条真实信封 edge 全是 "unknown"。
//
// 当天我据一条 daemon 指向本地 sink 时的样本把它改成了动态主机名 ——
// 那不是常态，方向是错的。这条测试钉住撤回后的行为。
// 信封 edge 跟随网关 host 查表，不是常量。
//
// 这个测试此前叫 ...IsAlwaysUnknown 并断言「三种 gateway 下恒为 unknown」——
// 那是把**一个输出**当成了规则。真客户端自己落盘的遥测里 edge 是
// "asia.route.reclaude.ai"，证明查表会命中；26/26 条 unknown 只是因为那批
// 样本打的是主域/本地 sink。
func TestReclaudeEnvelopeEdgeFollowsGatewayHost(t *testing.T) {
	for _, tc := range []struct {
		name    string
		gateway string
		want    string
	}{
		{"主域不在表里", "https://www.reclaude.ai", `"edge":"unknown"`},
		{"route 节点命中", "https://la.route.reclaude.ai", `"edge":"la.route.reclaude.ai"`},
		{"空网关", "", `"edge":"unknown"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope, _, err := buildReclaudeEnvelope(newInnerForEdgeTest(t), tc.gateway)
			require.NoError(t, err)
			require.Contains(t, string(envelope), tc.want)
		})
	}
}

func newInnerForEdgeTest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		"https://api.anthropic.com/v1/messages?beta=true", nil)
	require.NoError(t, err)
	req.Header.Set("content-type", "application/json")
	return req
}
