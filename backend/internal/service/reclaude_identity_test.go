package service

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/reclaude"
)

// 缺口 2：account_uuid 来自 Anthropic OAuth 流程，reclaude 不给我们，
// 而且底层账号会被静默换掉 ⇒ **根本不存在一个稳定的真实值**。
// 取不到时 RewriteUserIDWithMasking 整段被跳过，身份统一直接落空。
func TestGetAccountUUID_Reclaude(t *testing.T) {
	t.Run("回落到合成 uuid", func(t *testing.T) {
		account := &Account{
			ID:       9,
			Platform: PlatformAnthropic,
			Type:     AccountTypeReclaude,
			Credentials: map[string]any{
				CredKeyReclaudeSyntheticAccountUUID: "11111111-2222-3333-4444-555555555555",
			},
		}

		require.Equal(t, "11111111-2222-3333-4444-555555555555", account.GetAccountUUID())
	})

	// 合成值是**我们自己的**稳定 UUID，不是上游真值。
	// 上游看到的 user_id 哈希因此与自建号池不同源 —— 这是刻意的。
	t.Run("上游真值存在时仍优先用它", func(t *testing.T) {
		account := &Account{
			ID:       9,
			Platform: PlatformAnthropic,
			Type:     AccountTypeReclaude,
			Extra:    map[string]any{"account_uuid": "upstream-real-uuid"},
			Credentials: map[string]any{
				CredKeyReclaudeSyntheticAccountUUID: "synthetic-uuid",
			},
		}

		require.Equal(t, "upstream-real-uuid", account.GetAccountUUID())
	})

	t.Run("非 reclaude 账号不读合成字段", func(t *testing.T) {
		account := &Account{
			ID:       9,
			Platform: PlatformAnthropic,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				CredKeyReclaudeSyntheticAccountUUID: "synthetic-uuid",
			},
		}

		require.Empty(t, account.GetAccountUUID())
	})

	t.Run("合成字段缺失时为空", func(t *testing.T) {
		account := &Account{ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude}

		require.Empty(t, account.GetAccountUUID())
	})
}

// 缺口 1：身份统一挂在 account.IsOAuth() 上，reclaude 不在其中 ⇒
// 「一台设备底下挂 50 个不同 user_id」这条最直接的定性证据完全没被处理。
//
// 这是阻塞上线项，而且是**静默失效**：不执行重写不会报任何错。
// 所以除了语义断言，再用结构性断言钉死那一处门控用的是哪个谓词。
func TestIdentityUnificationGateCoversReclaude(t *testing.T) {
	t.Run("谓词覆盖 reclaude", func(t *testing.T) {
		require.True(t,
			(&Account{Platform: PlatformAnthropic, Type: AccountTypeReclaude}).UsesClaudeCodeMimicry())
	})

	t.Run("门控处确实用了该谓词", func(t *testing.T) {
		content, err := os.ReadFile(filepath.Clean("gateway_upstream_request.go"))
		require.NoError(t, err)

		var gateLine string
		for _, line := range strings.Split(string(content), "\n") {
			if strings.Contains(line, "s.identityService != nil") {
				gateLine = line
				break
			}
		}

		require.NotEmpty(t, gateLine, "身份统一的门控不见了，可能已被上游重构")
		require.Containsf(t, gateLine, "UsesClaudeCodeMimicry()",
			"门控退回了 IsOAuth()，reclaude 的身份统一会静默失效：%s", strings.TrimSpace(gateLine))
	})
}

// 静默换号时**不得**更换合成 uuid —— 换了上游就会看到「一台设备突然换了人」。
//
// 换号动作本身不携带任何身份字段，这就是「不会轮换」的结构性保证；
// 「建号时已有则保留」由 TestPrepareReclaudeAccountCreate 覆盖。
func TestAccountSwitchedActionCarriesNoIdentityRotation(t *testing.T) {
	action := mapReclaudeEvent(9, reclaude.ReclaudeEvent{
		Kind:                  reclaude.EventKindAccountSwitched,
		NewAccountMaskedEmail: "a***@b.com",
	})

	require.Equal(t, ReclaudeActionAccountSwitched, action.Kind)
	require.Equal(t, "a***@b.com", action.NewAccountMaskedEmail)

	// 动作结构体里没有任何可用于改写身份的字段 —— 这是刻意的。
	require.NotContains(t, reflect.TypeOf(action).String(), "UUID")
	_, hasUUIDField := reflect.TypeOf(action).FieldByName("SyntheticAccountUUID")
	require.False(t, hasUUIDField, "换号动作不得携带身份字段")
}
