//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func gatewayInput(url string) ReclaudeAccountInput {
	input := validReclaudeInput()
	input.GatewayURL = url
	return input
}

// 白名单必须恰好是真客户端的候选表，www 是兜底值不是候选。
func TestReclaudeGatewayCandidates(t *testing.T) {
	t.Run("白名单恰好是四个 route 候选", func(t *testing.T) {
		require.ElementsMatch(t, []string{
			"asia.route.reclaude.ai",
			"la.route.reclaude.ai",
			"misaka.route.reclaude.ai",
			"cloudfront.reclaude.ai",
		}, ReclaudeAllowedGatewayHosts)
	})

	t.Run("主域被拒 —— 它是 auto-pick 失败时的兜底值", func(t *testing.T) {
		// 🔴 回归闸门：把 www 加回白名单会让整个设备群停在一个
		// 「所有节点都连不通」才该出现的形态上。见白名单处的长注释。
		_, err := ValidateReclaudeAccountInput(gatewayInput("https://www.reclaude.ai"))
		require.ErrorIs(t, err, ErrReclaudeGatewayNotAllowed)
	})

	t.Run("route 候选可用", func(t *testing.T) {
		for _, host := range ReclaudeAllowedGatewayHosts {
			result, err := ValidateReclaudeAccountInput(gatewayInput("https://" + host))
			require.NoError(t, err, "候选 %s 应当可用", host)
			require.Equal(t, "https://"+host, result.NormalizedGateway)
		}
	})
}
