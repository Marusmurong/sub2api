//go:build unit

package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// 🔴 第七次撤销（2026-09-25，设备 4395 / 账号 233）根因回归。
//
// buildReclaudeEnvelope 是**所有** reclaude 出站信封的公共路径。里面的
// overrideReclaudeStainlessHeaders（含 anthropic-beta 冲刷 + x-stainless 注入）、
// 以及 x-claude-code-request-class / x-client-request-id 注入，都是给
// /v1/messages 推理路径写的（第六次撤销修复）。但 D3 合成的生命周期流量走同一条
// 路径，被无差别污染。
//
// ✅ 真值（reclaude-lab/sink/dump 26 条抓包）：只有 /v1/messages 带 x-stainless /
// request-class / client-request-id / 长 beta；mcp_servers / oauth / grove /
// event_logging 一个都不带，且 mcp_servers 的 anthropic-beta 恒为
// mcp-servers-2025-12-04（单 token，端点硬要求）。
//
// 症状：合成的 mcp_servers 每 60s 被上游 400（beta 被冲成 messages 集后缺
// mcp-servers-2025-12-04）。真客户端同一请求是 200 —— 一条每请求成立、跨设备一致
// 的可判别信号。
//
// 修复：合成请求（IsReclaudeSynthetic）跳过这三处推理专用改写，逐字透传
// reclaude_lifecycle_requests.go 里已对齐真值的头。
func TestBuildReclaudeEnvelopeSyntheticPreservesLifecycleHeaders(t *testing.T) {
	menv := `{"node_version":"v26.3.0","arch":"x64"}`
	// 有 machine_env + client_platform：非合成路径下必触发 override，用来证明
	// 「合成路径不触发」不是因为账号缺数据，而是被 synthetic 闸门挡住。
	acct := stainlessAccount("linux/amd64", menv)

	newSyntheticMCP := func() *http.Request {
		req, err := http.NewRequest(http.MethodGet,
			"https://api.anthropic.com/v1/mcp_servers?limit=1000&include_additional_installs=true", nil)
		require.NoError(t, err)
		// 逐字对齐真客户端 mcp_servers（reclaude-lab/sink/dump 抓包）。
		req.Header.Set("anthropic-beta", "mcp-servers-2025-12-04")
		req.Header.Set("mcp-protocol-version", "2025-11-25")
		req.Header.Set("user-agent", "axios/1.15.2")
		return req.WithContext(WithReclaudeSynthetic(req.Context()))
	}

	t.Run("anthropic-beta 保留 mcp-servers 专用值,不被 messages 集冲刷", func(t *testing.T) {
		envelope, _, err := buildReclaudeEnvelope(newSyntheticMCP(), "https://www.reclaude.ai", acct)
		require.NoError(t, err)

		headers := extractReclaudeEnvelopeMetaForTest(t, envelope)["headers"].(map[string]any)
		require.Equal(t, "mcp-servers-2025-12-04", headers["anthropic-beta"],
			"mcp_servers 端点硬要求这个 beta,被冲成 messages 集就 400")
	})

	t.Run("不注入 /v1/messages 专用头", func(t *testing.T) {
		envelope, _, err := buildReclaudeEnvelope(newSyntheticMCP(), "https://www.reclaude.ai", acct)
		require.NoError(t, err)

		headers := extractReclaudeEnvelopeMetaForTest(t, envelope)["headers"].(map[string]any)
		for _, k := range []string{
			"x-stainless-os", "x-stainless-arch", "x-stainless-runtime-version",
			"x-claude-code-request-class", "x-client-request-id",
		} {
			_, present := headers[k]
			require.Falsef(t, present,
				"合成生命周期请求不应带 %s（真客户端 mcp_servers 无此头）", k)
		}
	})
}

// 🔴 反向回归:非合成的 /v1/messages 推理路径必须保留第六次撤销的全部修复。
// synthetic 闸门只能挡住合成流量,绝不能顺手把推理路径的头也关掉。
func TestBuildReclaudeEnvelopeInferenceStillGetsInferenceHeaders(t *testing.T) {
	menv := `{"node_version":"v26.3.0","arch":"x64"}`
	acct := stainlessAccount("linux/amd64", menv)

	req, err := http.NewRequest(http.MethodPost,
		"https://api.anthropic.com/v1/messages?beta=true", nil)
	require.NoError(t, err)
	req.Header.Set("content-type", "application/json")
	// CC 伪装塞的 messages beta + 给 Anthropic 的 MacOS/arm64。
	req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
	req.Header.Set("x-stainless-os", "MacOS")
	req.Header.Set("x-stainless-arch", "arm64")

	envelope, _, err := buildReclaudeEnvelope(req, "https://www.reclaude.ai", acct)
	require.NoError(t, err)

	headers := extractReclaudeEnvelopeMetaForTest(t, envelope)["headers"].(map[string]any)
	require.Equal(t, "Linux", headers["x-stainless-os"], "推理路径仍覆盖成真机 OS")
	require.Equal(t, "x64", headers["x-stainless-arch"])
	require.Contains(t, headers["anthropic-beta"].(string), "cache-diagnosis-2026-04-07",
		"推理 beta 仍补齐真客户端 token")
	require.Equal(t, "main", headers["x-claude-code-request-class"])
	require.NotEmpty(t, headers["x-client-request-id"])
}
