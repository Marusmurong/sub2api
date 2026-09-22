package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// GetAccessToken 是穷举 switch，不加 case 则每个 reclaude 请求在进入重试循环
// **之前**就失败，永远走不到转发。
//
// 返回 tokenType="oauth" 是刻意的：它是 gateway_upstream_request.go 里十余处
// CC 伪装门控的共同钥匙（统一指纹、billing attribution 归一、header 不透传等）。
// 返回空 token 同样是刻意的：Anthropic 侧的凭据由 reclaude 自己填，我们不发
// Authorization 头。
func TestGetAccessToken_Reclaude(t *testing.T) {
	t.Run("返回空 token 与 oauth 类型", func(t *testing.T) {
		// Arrange
		gateway := &GatewayService{}
		account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeReclaude}

		// Act
		token, tokenType, err := gateway.GetAccessToken(context.Background(), account)

		// Assert
		require.NoError(t, err)
		require.Empty(t, token, "Anthropic 凭据由 reclaude 填，我们不持有")
		require.Equal(t, "oauth", tokenType, "tokenType 是 CC 伪装门控的共同钥匙")
	})

	t.Run("未知类型仍然被拒绝", func(t *testing.T) {
		gateway := &GatewayService{}
		account := &Account{ID: 1, Platform: PlatformAnthropic, Type: "not-a-real-type"}

		_, _, err := gateway.GetAccessToken(context.Background(), account)

		require.Error(t, err)
	})
}

// V-7：复制账号会产生两行同 device_id ⇒ 同一台设备被当成两个池子调度，
// 并发翻倍打穿包络。
func TestCanDuplicateAccountType_RejectsReclaude(t *testing.T) {
	require.False(t, canDuplicateAccountType(AccountTypeReclaude))

	t.Run("既有可复制类型不受影响", func(t *testing.T) {
		for _, accountType := range []string{
			AccountTypeAPIKey, AccountTypeUpstream, AccountTypeBedrock, AccountTypeServiceAccount,
		} {
			require.Truef(t, canDuplicateAccountType(accountType), "%s 应当仍可复制", accountType)
		}
	})
}
