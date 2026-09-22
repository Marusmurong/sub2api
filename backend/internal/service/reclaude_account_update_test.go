package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateReclaudeCredentialUpdate(t *testing.T) {
	existing := map[string]any{
		CredKeyReclaudeDeviceID: float64(43448),
		CredKeyReclaudeGateway:  "https://la.route.reclaude.ai",
		CredKeyReclaudeSK:       "enc:sk",
		CredKeyReclaudeSeed:     "enc:seed",
	}

	t.Run("只改网关节点时通过", func(t *testing.T) {
		merged := map[string]any{
			CredKeyReclaudeDeviceID: float64(43448),
			CredKeyReclaudeGateway:  "https://asia.route.reclaude.ai",
			CredKeyReclaudeSK:       "enc:sk",
			CredKeyReclaudeSeed:     "enc:seed",
		}

		require.NoError(t, ValidateReclaudeCredentialUpdate(existing, merged))
	})

	t.Run("网关改到白名单外时拒绝", func(t *testing.T) {
		// 🔴 默认配置下 SSRF 校验不生效，白名单是唯一防线；而 gateway_url
		// 决定我们把**全量明文 prompt** 发到哪台机器上。
		merged := map[string]any{
			CredKeyReclaudeDeviceID: float64(43448),
			CredKeyReclaudeGateway:  "https://evil.example.com",
			CredKeyReclaudeSK:       "enc:sk",
			CredKeyReclaudeSeed:     "enc:seed",
		}

		require.ErrorIs(t, ValidateReclaudeCredentialUpdate(existing, merged), ErrReclaudeGatewayNotAllowed)
	})

	t.Run("改 device_id 时拒绝", func(t *testing.T) {
		// device_id 是设备身份本身，改它等于把这行账号指向另一台设备 ——
		// 而 V-4 的唯一索引只拦「两行同号」，拦不住「一行换号」。
		merged := map[string]any{
			CredKeyReclaudeDeviceID: float64(99999),
			CredKeyReclaudeGateway:  "https://la.route.reclaude.ai",
			CredKeyReclaudeSK:       "enc:sk",
			CredKeyReclaudeSeed:     "enc:seed",
		}

		require.ErrorIs(t, ValidateReclaudeCredentialUpdate(existing, merged), ErrReclaudeImmutableCredential)
	})

	t.Run("改 sk 或 seed 时拒绝", func(t *testing.T) {
		// R-4 禁止二次 login ⇒ 凭据终身不可更换。能改就意味着能把一行账号
		// 悄悄换成另一份订阅，账号名、设备页面对账、日闸水位全部对不上。
		for _, key := range []string{CredKeyReclaudeSK, CredKeyReclaudeSeed} {
			merged := map[string]any{
				CredKeyReclaudeDeviceID: float64(43448),
				CredKeyReclaudeGateway:  "https://la.route.reclaude.ai",
				CredKeyReclaudeSK:       "enc:sk",
				CredKeyReclaudeSeed:     "enc:seed",
			}
			merged[key] = "something-else"

			require.ErrorIs(t, ValidateReclaudeCredentialUpdate(existing, merged), ErrReclaudeImmutableCredential)
		}
	})

	t.Run("device_id 被前端丢掉也算改动", func(t *testing.T) {
		// device_id 不在 SensitiveCredentialKeys 里 ⇒ 前端不回传就真的没了。
		// 放行等于让一次编辑把设备身份抹掉。
		merged := map[string]any{
			CredKeyReclaudeGateway: "https://la.route.reclaude.ai",
			CredKeyReclaudeSK:      "enc:sk",
			CredKeyReclaudeSeed:    "enc:seed",
		}

		require.ErrorIs(t, ValidateReclaudeCredentialUpdate(existing, merged), ErrReclaudeImmutableCredential)
	})

	t.Run("敏感键原样带回（合并保留）时通过", func(t *testing.T) {
		// 前端响应已脱敏，全对象 PUT 时后端会把密文合并回来 —— 这不是「改」。
		merged := map[string]any{
			CredKeyReclaudeDeviceID: float64(43448),
			CredKeyReclaudeGateway:  "https://la.route.reclaude.ai",
			CredKeyReclaudeSK:       "enc:sk",
			CredKeyReclaudeSeed:     "enc:seed",
		}

		require.NoError(t, ValidateReclaudeCredentialUpdate(existing, merged))
	})
}
