package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func configuredCfg() *config.Config {
	cfg := &config.Config{}
	cfg.Totp.EncryptionKeyConfigured = true
	return cfg
}

func reclaudeCreateInput() *CreateAccountInput {
	proxyID := int64(7)
	return &CreateAccountInput{
		Platform: PlatformAnthropic,
		Type:     AccountTypeReclaude,
		ProxyID:  &proxyID,
		Credentials: map[string]any{
			CredKeyReclaudeSK:             "sk-rec-abcdef123456",
			CredKeyReclaudeSeed:           validSeedHex(),
			CredKeyReclaudeDeviceID:       float64(43448), // JSON 反序列化出来就是 float64
			CredKeyReclaudeFingerprint:    "5c5349acd61f1df2",
			CredKeyReclaudeGateway:        "https://la.route.reclaude.ai",
			CredKeyReclaudeClientVersion:  "v1.4.0",
			CredKeyReclaudeClientPlatform: "linux/amd64",
			CredKeyReclaudeDeviceHostname: "mbp-dev",
			CredKeyReclaudeClaudeUserID:   "c8f2a1b09d3e4f5a6b7c8d9e0f1a2b3c4d5e6f708192a3b4c5d6e7f809a1b2c3",
			CredKeyReclaudeAccountUUID:    "9c67eb02-4001-4cde-a6e2-e40f1a71649e",
		},
		Extra: map[string]any{
			ExtraKeyReclaudePlanTier: "20x",
		},
	}
}

func TestPrepareReclaudeAccountCreate(t *testing.T) {
	cipher := NewReclaudeCredentialCipher(&fakeEncryptor{})

	t.Run("合法输入产出账号名、密文凭据与合成 uuid", func(t *testing.T) {
		// Act
		input := reclaudeCreateInput()

		prepared, err := PrepareReclaudeAccountCreate(configuredCfg(), cipher, input, input.Extra)

		// Assert
		require.NoError(t, err)
		require.Equal(t, "rec/mbp-dev-3448", prepared.Name)
		require.True(t, strings.HasPrefix(prepared.Credentials[CredKeyReclaudeSK].(string), encryptedCredentialPrefix))
		require.True(t, strings.HasPrefix(prepared.Credentials[CredKeyReclaudeSeed].(string), encryptedCredentialPrefix))
		require.NotEmpty(t, prepared.Credentials[CredKeyReclaudeSyntheticAccountUUID])
	})

	// V-8 必须在**任何**凭据处理之前就拦下来。
	t.Run("V-8 密钥未配置时拒绝，且不产出任何凭据", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Totp.EncryptionKeyConfigured = false

		input := reclaudeCreateInput()

		prepared, err := PrepareReclaudeAccountCreate(cfg, cipher, input, input.Extra)

		require.ErrorIs(t, err, ErrCredentialEncryptionKeyNotConfigured)
		require.Nil(t, prepared.Credentials)
	})

	t.Run("不修改入参", func(t *testing.T) {
		input := reclaudeCreateInput()

		_, err := PrepareReclaudeAccountCreate(configuredCfg(), cipher, input, input.Extra)

		require.NoError(t, err)
		require.Equal(t, "sk-rec-abcdef123456", input.Credentials[CredKeyReclaudeSK], "入参被就地改了")
		require.Empty(t, input.Name)
	})

	// 合成 uuid 一旦生成就终身不变：静默换号时更换它，上游会看到
	// 「一台设备突然换了人」。
	t.Run("已有合成 uuid 时保留不重新生成", func(t *testing.T) {
		input := reclaudeCreateInput()
		input.Credentials[CredKeyReclaudeSyntheticAccountUUID] = "fixed-uuid-value"

		prepared, err := PrepareReclaudeAccountCreate(configuredCfg(), cipher, input, input.Extra)

		require.NoError(t, err)
		require.Equal(t, "fixed-uuid-value", prepared.Credentials[CredKeyReclaudeSyntheticAccountUUID])
	})

	t.Run("账号名由建号逻辑生成，用户填的被忽略", func(t *testing.T) {
		input := reclaudeCreateInput()
		input.Name = "我自己起的名字"

		prepared, err := PrepareReclaudeAccountCreate(configuredCfg(), cipher, input, input.Extra)

		require.NoError(t, err)
		require.Equal(t, "rec/mbp-dev-3448", prepared.Name)
	})

	t.Run("平台被固定为 anthropic 以复用全部 Anthropic 链路", func(t *testing.T) {
		input := reclaudeCreateInput()
		input.Platform = PlatformOpenAI

		_, err := PrepareReclaudeAccountCreate(configuredCfg(), cipher, input, input.Extra)

		require.Error(t, err)
	})

	t.Run("硬校验失败原样透出（举例：无代理）", func(t *testing.T) {
		input := reclaudeCreateInput()
		input.ProxyID = nil

		_, err := PrepareReclaudeAccountCreate(configuredCfg(), cipher, input, input.Extra)

		require.ErrorIs(t, err, ErrReclaudeProxyRequired)
	})

	t.Run("套餐档位缺失时拒绝", func(t *testing.T) {
		input := reclaudeCreateInput()
		delete(input.Extra, ExtraKeyReclaudePlanTier)

		_, err := PrepareReclaudeAccountCreate(configuredCfg(), cipher, input, input.Extra)

		require.ErrorIs(t, err, ErrReclaudePlanTierRequired)
	})

	// 档位是限额的唯一入口 —— 落库的必须是换算值，不是运维手填的值。
	t.Run("档位换算出的美元日限额落进 extra", func(t *testing.T) {
		input := reclaudeCreateInput()
		input.Extra[ExtraKeyReclaudePlanTier] = "20x-carpool-4"

		prepared, err := PrepareReclaudeAccountCreate(configuredCfg(), cipher, input, input.Extra)

		require.NoError(t, err)
		require.Equal(t, 150.0, prepared.Extra["quota_daily_limit"])
	})

	t.Run("网关地址被规范化后落库", func(t *testing.T) {
		input := reclaudeCreateInput()
		input.Credentials[CredKeyReclaudeGateway] = "https://la.route.reclaude.ai/"

		prepared, err := PrepareReclaudeAccountCreate(configuredCfg(), cipher, input, input.Extra)

		require.NoError(t, err)
		require.Equal(t, "https://la.route.reclaude.ai", prepared.Credentials[CredKeyReclaudeGateway])
	})

	t.Run("指纹格式不对时以警告返回而不是拒绝", func(t *testing.T) {
		input := reclaudeCreateInput()
		input.Credentials[CredKeyReclaudeFingerprint] = "bad"

		prepared, err := PrepareReclaudeAccountCreate(configuredCfg(), cipher, input, input.Extra)

		require.NoError(t, err)
		require.NotEmpty(t, prepared.Warnings)
	})

	t.Run("非 reclaude 类型不受本流程影响", func(t *testing.T) {
		input := reclaudeCreateInput()
		input.Type = AccountTypeOAuth

		_, err := PrepareReclaudeAccountCreate(configuredCfg(), cipher, input, input.Extra)

		require.Error(t, err, "误把非 rec 账号送进来应当报错而不是静默处理")
	})
}
