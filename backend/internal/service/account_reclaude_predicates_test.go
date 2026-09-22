package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 批次 0（纯重构）：谓词化。
// IsReclaude 目前对任何已存在的账号都返回 false —— 这是「零行为变更」的基础：
// 还没有任何入口能创建 reclaude 类型账号（GetAccessToken 仍然拒绝它）。
func TestAccount_IsReclaude(t *testing.T) {
	t.Run("reclaude 类型返回 true", func(t *testing.T) {
		account := &Account{Platform: PlatformAnthropic, Type: AccountTypeReclaude}
		require.True(t, account.IsReclaude())
	})

	t.Run("现有账号类型一律返回 false", func(t *testing.T) {
		for _, accountType := range []string{
			AccountTypeOAuth,
			AccountTypeSetupToken,
			AccountTypeAPIKey,
			AccountTypeUpstream,
			AccountTypeBedrock,
			AccountTypeServiceAccount,
		} {
			account := &Account{Platform: PlatformAnthropic, Type: accountType}
			require.Falsef(t, account.IsReclaude(), "type=%s 不应被判为 reclaude", accountType)
		}
	})

	t.Run("nil 账号安全返回 false", func(t *testing.T) {
		var account *Account
		require.False(t, account.IsReclaude())
	})
}

// UsesClaudeCodeMimicry 是 CC 伪装链路的新钥匙，取代散落的 account.IsOAuth()。
// 本测试同时是回归测试：现有账号的判定结果必须与 IsOAuth() 逐个相同。
func TestAccount_UsesClaudeCodeMimicry(t *testing.T) {
	t.Run("对现有账号与 IsOAuth 完全一致（零行为变更）", func(t *testing.T) {
		for _, accountType := range []string{
			AccountTypeOAuth,
			AccountTypeSetupToken,
			AccountTypeAPIKey,
			AccountTypeUpstream,
			AccountTypeBedrock,
			AccountTypeServiceAccount,
		} {
			account := &Account{Platform: PlatformAnthropic, Type: accountType}
			require.Equalf(t, account.IsOAuth(), account.UsesClaudeCodeMimicry(),
				"type=%s 的伪装判定与重构前不一致", accountType)
		}
	})

	t.Run("reclaude 账号也走 CC 伪装", func(t *testing.T) {
		account := &Account{Platform: PlatformAnthropic, Type: AccountTypeReclaude}
		require.True(t, account.UsesClaudeCodeMimicry())
	})

	t.Run("nil 账号安全返回 false", func(t *testing.T) {
		var account *Account
		require.False(t, account.UsesClaudeCodeMimicry())
	})
}

// UsesAnthropicClientIdentity 管的是「出站请求要不要按 Claude Code 身份清洗」
// （目前只有 dateline 隐写归一）。它与 UsesClaudeCodeMimicry 的作用域今天并不相同
// —— 前者限定 Anthropic 平台，后者不限 —— 所以不能合并成一个谓词。
//
// 单独立一个而不是直接改 IsAnthropicOAuthOrSetupToken：后者还管着 5h 窗口额度、
// 会话数控制与 TLS 指纹，扩大它的范围会顺带改掉那三样。
func TestAccount_UsesAnthropicClientIdentity(t *testing.T) {
	t.Run("对现有账号与 IsAnthropicOAuthOrSetupToken 完全一致（零行为变更）", func(t *testing.T) {
		for _, platform := range []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini} {
			for _, accountType := range []string{
				AccountTypeOAuth,
				AccountTypeSetupToken,
				AccountTypeAPIKey,
				AccountTypeUpstream,
				AccountTypeBedrock,
				AccountTypeServiceAccount,
			} {
				account := &Account{Platform: platform, Type: accountType}
				require.Equalf(t, account.IsAnthropicOAuthOrSetupToken(), account.UsesAnthropicClientIdentity(),
					"platform=%s type=%s 的判定与重构前不一致", platform, accountType)
			}
		}
	})

	t.Run("reclaude 账号纳入清洗范围", func(t *testing.T) {
		account := &Account{Platform: PlatformAnthropic, Type: AccountTypeReclaude}
		require.True(t, account.UsesAnthropicClientIdentity())
	})

	t.Run("nil 账号安全返回 false", func(t *testing.T) {
		var account *Account
		require.False(t, account.UsesAnthropicClientIdentity())
	})
}
