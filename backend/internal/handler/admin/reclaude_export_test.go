package admin

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// 导出通道刻意返回 credentials 原文（管理员备份的显式行为）。
// 但 reclaude 的两个秘密字段必须剥离：
//
//   - 它们导出去也没用 —— V-10 禁止 reclaude 走导入通道重建
//   - 落库虽是密文，但密文 + 泄漏的 AES 密钥 = 订阅被白嫖 + 设备被顶
//   - 真正的离线备份按设计是**明文、手工、带外**保管的，不靠这条通道
func TestExportStripsReclaudeSecrets(t *testing.T) {
	t.Run("剥离两个秘密字段", func(t *testing.T) {
		credentials := map[string]any{
			service.CredKeyReclaudeSK:        "enc:v1:ciphertext",
			service.CredKeyReclaudeSeed:      "enc:v1:ciphertext",
			service.CredKeyReclaudeDeviceID:  float64(43448),
			service.CredKeyReclaudeGateway:   "https://la.route.reclaude.ai",
			service.CredKeyReclaudeUserEmail: "a@b.com",
		}

		out := stripReclaudeExportSecrets(service.AccountTypeReclaude, credentials)

		require.NotContains(t, out, service.CredKeyReclaudeSK)
		require.NotContains(t, out, service.CredKeyReclaudeSeed)
	})

	t.Run("保留运维要看的展示字段", func(t *testing.T) {
		credentials := map[string]any{
			service.CredKeyReclaudeSK:       "enc:v1:ciphertext",
			service.CredKeyReclaudeDeviceID: float64(43448),
			service.CredKeyReclaudeGateway:  "https://la.route.reclaude.ai",
		}

		out := stripReclaudeExportSecrets(service.AccountTypeReclaude, credentials)

		require.Equal(t, float64(43448), out[service.CredKeyReclaudeDeviceID])
		require.Equal(t, "https://la.route.reclaude.ai", out[service.CredKeyReclaudeGateway])
	})

	// 其它账号类型的导出行为**一个字不能变** —— 那是现有备份/恢复流程的契约。
	t.Run("非 reclaude 账号原样返回", func(t *testing.T) {
		credentials := map[string]any{"access_token": "secret", "refresh_token": "secret"}

		out := stripReclaudeExportSecrets(service.AccountTypeOAuth, credentials)

		require.Equal(t, credentials, out)
	})

	t.Run("不修改入参", func(t *testing.T) {
		credentials := map[string]any{service.CredKeyReclaudeSK: "enc:v1:ciphertext"}

		_ = stripReclaudeExportSecrets(service.AccountTypeReclaude, credentials)

		require.Contains(t, credentials, service.CredKeyReclaudeSK)
	})

	t.Run("空 credentials 安全", func(t *testing.T) {
		require.Nil(t, stripReclaudeExportSecrets(service.AccountTypeReclaude, nil))
	})
}
