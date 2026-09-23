package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 🔴 metadata.user_id 里的 device_id / account_uuid 必须用**真实客户端的值**。
//
// 2026-09-24 定位到 bad_envelope 的真因：rec 服务端按 metadata 里的 device_id
// 识别设备，我们报的是自己生成的 ClientID，于是被判
// `reclaude state mismatch, please restart reclaude`。
//
// 两个值的来源（都在 login 那台机器上）：
//   device_id    = ~/.claude.json 的 userID（64 hex）
//   account_uuid = /api/cli/auth/poll 响应的 AccountUUID
//
// ⚠️ 注意同名不同物：凭据里的 reclaude_device_id（如 44186）是**设备号**，
// 发在 X-Reclaude-Device-Id 头上；这里的 device_id 是 Claude Code 的 userID，
// 发在 body 的 metadata 里。两者完全无关，早期实现把它们混为一谈。
func TestReclaudeMetadataIdentity(t *testing.T) {
	const (
		realDeviceID = "3c5a8384b29498aeb24ac2717c50dbccbffe818352a8eac8c1a6f29d690b2e31"
		realAccount  = "9c67eb02-4001-4cde-a6e2-e40f1a71649e"
	)

	newAccount := func(creds map[string]any) *Account {
		return &Account{ID: 1, Type: AccountTypeReclaude, Platform: PlatformAnthropic,
			Credentials: creds}
	}

	t.Run("配置了真实 device_id 时用它", func(t *testing.T) {
		account := newAccount(map[string]any{CredKeyReclaudeClaudeUserID: realDeviceID})

		require.Equal(t, realDeviceID, ReclaudeMetadataDeviceID(account))
	})

	t.Run("未配置时返回空，让调用方回落既有 ClientID", func(t *testing.T) {
		// 不能瞎编一个：编出来的值必然与服务端记录不符，反而稳定失败。
		require.Empty(t, ReclaudeMetadataDeviceID(newAccount(map[string]any{})))
	})

	t.Run("device_id 必须是 64 位 hex，否则忽略", func(t *testing.T) {
		// Claude Code 的 userID 是 sha256 hex。形态不对说明填错了字段
		// （比如误填了 reclaude_device_id 那个数字），用它只会稳定失败。
		for _, bad := range []string{"44186", "not-hex", realDeviceID[:32], realDeviceID + "ff"} {
			require.Emptyf(t, ReclaudeMetadataDeviceID(
				newAccount(map[string]any{CredKeyReclaudeClaudeUserID: bad})),
				"不该接受 %q", bad)
		}
	})

	t.Run("account_uuid 优先用 login 拿到的真值", func(t *testing.T) {
		account := newAccount(map[string]any{CredKeyReclaudeAccountUUID: realAccount})

		require.Equal(t, realAccount, account.GetAccountUUID())
	})

	t.Run("account_uuid 缺失时仍回落合成值", func(t *testing.T) {
		// 回落不能去掉：取不到时整段 metadata 重写会被跳过，身份统一直接落空。
		account := newAccount(map[string]any{
			CredKeyReclaudeSyntheticAccountUUID: "11111111-2222-3333-4444-555555555555",
		})

		require.Equal(t, "11111111-2222-3333-4444-555555555555", account.GetAccountUUID())
	})
}

// 建号时就必须校验这两个字段：缺了账号建得出来但一定跑不通，
// 而失败信息是对方那句「请重启 reclaude」，完全看不出是建号时填漏了。
func TestValidateReclaudeAccountInput_DeviceIdentity(t *testing.T) {
	proxyID := int64(1)
	valid := func() ReclaudeAccountInput {
		return ReclaudeAccountInput{
			ProxyID:        &proxyID,
			SK:             ReclaudeSKPrefix + "abcdefghijklmnopqrstuvwxyz",
			SeedEncoded:    validSeedHex(),
			DeviceID:       44186,
			ClientVersion:  "v1.4.0",
			ClientPlatform: "linux/amd64",
			GatewayURL:     "https://" + ReclaudeAllowedGatewayHosts[0],
			PlanTier:       "20x",
			ClaudeUserID:   "c8f2a1b09d3e4f5a6b7c8d9e0f1a2b3c4d5e6f708192a3b4c5d6e7f809a1b2c3",
			AccountUUID:    "9c67eb02-4001-4cde-a6e2-e40f1a71649e",
		}
	}

	t.Run("齐全时通过", func(t *testing.T) {
		_, err := ValidateReclaudeAccountInput(valid())
		require.NoError(t, err)
	})

	t.Run("缺 ClaudeUserID 被拒", func(t *testing.T) {
		input := valid()
		input.ClaudeUserID = ""

		_, err := ValidateReclaudeAccountInput(input)

		require.ErrorIs(t, err, ErrReclaudeDeviceIdentityMissing)
	})

	t.Run("ClaudeUserID 形态不对被拒", func(t *testing.T) {
		// 最常见的填错是把设备号（44186）填进来。
		for _, bad := range []string{"44186", "not-hex", "abc"} {
			input := valid()
			input.ClaudeUserID = bad

			_, err := ValidateReclaudeAccountInput(input)

			require.ErrorIsf(t, err, ErrReclaudeDeviceIdentityMissing, "不该接受 %q", bad)
		}
	})

	t.Run("缺 AccountUUID 被拒", func(t *testing.T) {
		input := valid()
		input.AccountUUID = ""

		_, err := ValidateReclaudeAccountInput(input)

		require.ErrorIs(t, err, ErrReclaudeDeviceIdentityMissing)
	})
}
