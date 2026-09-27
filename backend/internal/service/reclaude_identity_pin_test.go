//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// E2-L1（RECLAUDE_REVOCATION_VERIFY_PLAN_2026-09-26 §4 L1）：版本与入口同源。
//
// 09-26 sub 实发信封里同时出现三个版本号（引导 UA 1.4.0、推理 UA 2.1.280、
// event_logging env.version 2.1.282）。
// 2026-09-28 翻回 sdk-cli（8x 存活黄金基准：内嵌 claude 跑 headless `claude -p`，
// 发的就是 (external, sdk-cli) + cc_entrypoint=sdk-cli + Agent SDK 身份块）—— 出站
// 身份取 machine_env.cli_version + (external, sdk-cli)，billing cc_entrypoint=sdk-cli。

func reclaudeIdentityAccount(machineEnv string) *Account {
	return &Account{
		ID:       1,
		Platform: PlatformAnthropic,
		Type:     AccountTypeReclaude,
		Credentials: map[string]any{
			CredKeyReclaudeClientPlatform: "linux/amd64",
			CredKeyReclaudeMachineEnv:     machineEnv,
		},
	}
}

func TestReclaudeForcedFingerprintPinsCLIVersion(t *testing.T) {
	menv := `{"node_version":"v26.3.0","arch":"x64","cli_version":"2.1.282"}`

	t.Run("reclaude 账号一律是强制身份", func(t *testing.T) {
		acct := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeReclaude,
			Credentials: map[string]any{CredKeyReclaudeMachineEnv: menv}}
		require.True(t, acct.HasForcedFingerprint())
		require.NotNil(t, acct.resolveForcedFingerprintSpec())
	})

	t.Run("推理 UA 版本取 machine_env.cli_version,后缀 sdk-cli", func(t *testing.T) {
		spec := reclaudeIdentityAccount(menv).resolveForcedFingerprintSpec()
		require.NotNil(t, spec)
		require.Equal(t, "2.1.282", spec.CLIVersion)
		require.Equal(t, "(external, sdk-cli)", spec.UASuffix)
		require.Equal(t, "claude-cli/2.1.282 (external, sdk-cli)", spec.toFingerprint().UserAgent)
	})

	t.Run("缺 cli_version 时版本走基线,后缀仍 sdk-cli", func(t *testing.T) {
		spec := reclaudeIdentityAccount(`{"arch":"x64"}`).resolveForcedFingerprintSpec()
		require.NotNil(t, spec)
		require.NotEmpty(t, spec.CLIVersion)
		require.Equal(t, "(external, sdk-cli)", spec.UASuffix)
	})

	t.Run("显式 fingerprint.cli_version 仍优先", func(t *testing.T) {
		acct := reclaudeIdentityAccount(menv)
		acct.Extra = map[string]any{"fingerprint": map[string]any{"cli_version": "2.1.290"}}
		spec := acct.resolveForcedFingerprintSpec()
		require.Equal(t, "2.1.290", spec.CLIVersion)
	})

	t.Run("非 reclaude 账号不受影响", func(t *testing.T) {
		acct := &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
		require.False(t, acct.HasForcedFingerprint())
		require.Nil(t, acct.resolveForcedFingerprintSpec())
	})
}

func TestResolveClientEntrypointForAccountPinsReclaude(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.282.abc; cc_entrypoint=cli; cch=00000;"}]}`)

	t.Run("reclaude 账号钉 sdk-cli（内嵌 claude headless 真值,Agent SDK 身份块）", func(t *testing.T) {
		acct := reclaudeIdentityAccount(`{"cli_version":"2.1.282"}`)
		// 下游哪怕是交互式 cli,也钉成 sdk-cli —— 内嵌 claude 跑的是 `claude -p`。
		e := resolveClientEntrypointForAccount(acct, "claude-cli/2.1.282 (external, cli)", body)
		require.Equal(t, "sdk-cli", e.Product)
		require.Equal(t, "(external, sdk-cli)", e.UASuffix)
	})

	t.Run("非 reclaude 账号照旧跟随下游", func(t *testing.T) {
		acct := &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
		e := resolveClientEntrypointForAccount(acct, "claude-cli/2.1.282 (external, cli)", body)
		require.Equal(t, defaultClientEntrypoint(), e)
	})
}

func TestNormalizeBillingHeaderBlockAlignsEntrypoint(t *testing.T) {
	body := `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.280.abc; cc_entrypoint=sdk-cli; cch=00000;"}],"messages":[]}`

	t.Run("给定入口时 cc_entrypoint 改成它", func(t *testing.T) {
		out := string(normalizeBillingHeaderBlockWithEntrypoint(
			[]byte(body), "claude-cli/2.1.282 (external, cli)", false, "", "cli"))
		require.Contains(t, out, "cc_version=2.1.282.abc; cc_entrypoint=cli; cch=00000;")
	})

	t.Run("入口为空时不动 cc_entrypoint", func(t *testing.T) {
		out := string(normalizeBillingHeaderBlockWithEntrypoint(
			[]byte(body), "claude-cli/2.1.282 (external, cli)", false, "", ""))
		require.Contains(t, out, "cc_entrypoint=sdk-cli;")
	})
}
