package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultReclaudeSettings(t *testing.T) {
	settings := DefaultReclaudeSettings()

	// 🔴 默认关。这条通道把**全量明文 prompt** 发给第三方网关，
	// 不该因为升了个版本就自己活过来。
	require.False(t, settings.Enabled)
	require.True(t, settings.UnknownEventAlert)
	require.Equal(t, DefaultReclaudeOversellRatio, settings.OversellRatio)
}

func TestReclaudeSettings_Normalize(t *testing.T) {
	t.Run("超卖率越界时回落到默认", func(t *testing.T) {
		for _, ratio := range []float64{0, -1, 1.5} {
			settings := ReclaudeSettings{OversellRatio: ratio}
			require.Equal(t, DefaultReclaudeOversellRatio, settings.Normalized().OversellRatio)
		}
	})

	t.Run("合法值原样保留", func(t *testing.T) {
		settings := ReclaudeSettings{Enabled: true, OversellRatio: 0.5}
		require.Equal(t, 0.5, settings.Normalized().OversellRatio)
		require.True(t, settings.Normalized().Enabled)
	})
}

func TestGatewayService_ReclaudeKillSwitch(t *testing.T) {
	// 日限额是套餐档位换算出的**美元**值；token 闸作为第二道并存。
	account := &Account{ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude,
		Extra: map[string]any{
			ExtraKeyReclaudePlanTier:      "20x",
			"quota_daily_limit":           600.0,
			ExtraKeyReclaudeDailyTokenCap: float64(1000),
		}}

	t.Run("开关关闭时 reclaude 账号立即不可调度", func(t *testing.T) {
		// 「关闭」的语义必须是立刻不可调度，不是「照跑到自然结束」——
		// 出事时需要的是止血，不是优雅退场。
		gateway := &GatewayService{
			reclaudeQuota:   NewReclaudeQuotaGate(&fakeReclaudeUsageStore{}),
			reclaudeEnabled: func(context.Context) bool { return false },
		}

		require.False(t, gateway.isAccountSchedulableForQuota(account))
	})

	t.Run("开关打开时回到日闸判定", func(t *testing.T) {
		gateway := &GatewayService{
			reclaudeQuota:   NewReclaudeQuotaGate(&fakeReclaudeUsageStore{}),
			reclaudeEnabled: func(context.Context) bool { return true },
		}

		require.True(t, gateway.isAccountSchedulableForQuota(account))
	})

	t.Run("开关未注入时按关处理", func(t *testing.T) {
		// 缺件一律判不可调度：这条链路上「误放行」没有补救手段。
		gateway := &GatewayService{reclaudeQuota: NewReclaudeQuotaGate(&fakeReclaudeUsageStore{})}

		require.False(t, gateway.isAccountSchedulableForQuota(account))
	})

	// 未标定 = 禁止调度：未标定就售卖等于超卖，而超卖没有补救手段。
	t.Run("没设日限额的 reclaude 账号不可调度", func(t *testing.T) {
		gateway := &GatewayService{
			reclaudeQuota:   NewReclaudeQuotaGate(&fakeReclaudeUsageStore{}),
			reclaudeEnabled: func(context.Context) bool { return true },
		}
		unmetered := &Account{ID: 10, Platform: PlatformAnthropic, Type: AccountTypeReclaude,
			Extra: map[string]any{}}

		require.False(t, gateway.isAccountSchedulableForQuota(unmetered))
	})

	t.Run("非 reclaude 账号不受影响", func(t *testing.T) {
		gateway := &GatewayService{}

		require.True(t, gateway.isAccountSchedulableForQuota(
			&Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}))
	})
}
