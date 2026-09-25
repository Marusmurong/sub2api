//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func stainlessAccount(platform, machineEnv string) *Account {
	return &Account{
		ID:       1,
		Platform: PlatformAnthropic,
		Type:     AccountTypeReclaude,
		Credentials: map[string]any{
			CredKeyReclaudeClientPlatform: platform,
			CredKeyReclaudeMachineEnv:     machineEnv,
		},
	}
}

// 🔴 第六次撤销根因回归：内层 x-stainless-os/arch 必须是真机值,不是给 Anthropic 的 MacOS。
func TestOverrideReclaudeStainlessHeaders(t *testing.T) {
	menvLinuxX64 := `{"node_version":"v26.3.0","arch":"x64","linux_distro_id":"ubuntu","linux_kernel":"6.17","shell":"bash"}`

	t.Run("Linux 服务器覆盖掉 MacOS 伪装", func(t *testing.T) {
		// CC 伪装先塞了 MacOS/arm64（给 Anthropic 的值）。
		headers := map[string]string{
			"x-stainless-os":              "MacOS",
			"x-stainless-arch":            "arm64",
			"x-stainless-runtime-version": "v26.3.0",
		}
		overrideReclaudeStainlessHeaders(headers, stainlessAccount("linux/amd64", menvLinuxX64))

		require.Equal(t, "Linux", headers["x-stainless-os"], "必须覆盖成真机 OS,不能是 MacOS")
		require.Equal(t, "x64", headers["x-stainless-arch"])
	})

	t.Run("流式 helper-method 被删除", func(t *testing.T) {
		// 真客户端流式也不发这个头。
		headers := map[string]string{
			"x-stainless-os":            "MacOS",
			"x-stainless-helper-method": "stream",
		}
		overrideReclaudeStainlessHeaders(headers, stainlessAccount("linux/amd64", menvLinuxX64))

		_, present := headers["x-stainless-helper-method"]
		require.False(t, present, "真客户端内层不发 helper-method")
	})

	t.Run("UA 后缀改成 sdk-cli", func(t *testing.T) {
		headers := map[string]string{
			"user-agent": "claude-cli/2.1.280 (external, cli)",
		}
		overrideReclaudeStainlessHeaders(headers, stainlessAccount("linux/amd64", menvLinuxX64))

		require.Equal(t, "claude-cli/2.1.280 (external, sdk-cli)", headers["user-agent"])
	})

	t.Run("darwin 账号保持 MacOS（不强改）", func(t *testing.T) {
		// 如果这台真机就是 mac,那 MacOS 是对的。
		headers := map[string]string{"x-stainless-os": "MacOS"}
		menvMac := `{"arch":"arm64"}`
		overrideReclaudeStainlessHeaders(headers, stainlessAccount("darwin/arm64", menvMac))

		require.Equal(t, "MacOS", headers["x-stainless-os"])
		require.Equal(t, "arm64", headers["x-stainless-arch"])
	})

	t.Run("缺快照时保留伪装值,不凭空改", func(t *testing.T) {
		// 降级:没有真机 env 就别把一个对不上的值改成另一个对不上的。
		headers := map[string]string{"x-stainless-os": "MacOS", "x-stainless-arch": "arm64"}
		acct := &Account{ID: 1, Type: AccountTypeReclaude, Credentials: map[string]any{
			CredKeyReclaudeClientPlatform: "linux/amd64", // 有平台但无 machine_env
		}}
		overrideReclaudeStainlessHeaders(headers, acct)

		// os 能从 platform 推(linux→Linux),arch 无快照则保留
		require.Equal(t, "Linux", headers["x-stainless-os"])
		require.Equal(t, "arm64", headers["x-stainless-arch"], "无 arch 快照时不动")
	})

	t.Run("nil 不 panic", func(t *testing.T) {
		require.NotPanics(t, func() {
			overrideReclaudeStainlessHeaders(nil, stainlessAccount("linux/amd64", menvLinuxX64))
			overrideReclaudeStainlessHeaders(map[string]string{}, nil)
		})
	})
}

func TestReclaudeInnerBetaOverride(t *testing.T) {
	menv := `{"node_version":"v26.3.0","arch":"x64"}`

	t.Run("anthropic-beta 被 reclaude 16-token 集覆盖", func(t *testing.T) {
		headers := map[string]string{"anthropic-beta": "claude-code-20250219,oauth-2025-04-20"}
		overrideReclaudeStainlessHeaders(headers, stainlessAccount("linux/amd64", menv))

		beta := headers["anthropic-beta"]
		require.Contains(t, beta, "advanced-tool-use-2025-11-20", "补齐真客户端 token")
		require.Contains(t, beta, "cache-diagnosis-2026-04-07")
	})

	t.Run("剔除 fallback 相关 token —— 与我们删了 body.fallbacks 对称", func(t *testing.T) {
		// 🔴 beta 声称支持 fallback 而 body 没有 = 不对称。红线。
		headers := map[string]string{"anthropic-beta": "x"}
		overrideReclaudeStainlessHeaders(headers, stainlessAccount("linux/amd64", menv))

		require.NotContains(t, headers["anthropic-beta"], "server-side-fallback")
		require.NotContains(t, headers["anthropic-beta"], "fallback-credit")
	})

	t.Run("无 anthropic-beta 时不凭空造", func(t *testing.T) {
		headers := map[string]string{}
		overrideReclaudeStainlessHeaders(headers, stainlessAccount("linux/amd64", menv))
		_, present := headers["anthropic-beta"]
		require.False(t, present)
	})
}

func TestReclaudeNormalizeNodeVersion(t *testing.T) {
	require.Equal(t, "v26.3.0", reclaudeNormalizeNodeVersion("26.3.0"), "裸版本补 v")
	require.Equal(t, "v26.3.0", reclaudeNormalizeNodeVersion("v26.3.0"), "已有 v 不重复")
	require.Equal(t, "", reclaudeNormalizeNodeVersion(""), "空串不动")
}
