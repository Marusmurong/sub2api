package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReclaudeQuotaGate_TodayUsage(t *testing.T) {
	ctx := context.Background()
	account, _ := reclaudeTestAccount(t)
	account.Extra = map[string]any{ExtraKeyReclaudeDailyTokenCap: float64(1_000_000)}

	t.Run("回今日水位与上限", func(t *testing.T) {
		store := &fakeReclaudeUsageStore{}
		gate := NewReclaudeQuotaGate(store)
		require.NoError(t, gate.RecordUpstreamCall(ctx, account, 1234, true))

		snapshot, err := gate.TodayUsage(ctx, account)

		require.NoError(t, err)
		require.Equal(t, int64(1234), snapshot.Usage.Tokens)
		require.Equal(t, int64(1), snapshot.Usage.UpstreamCalls)
		require.Equal(t, int64(1_000_000), snapshot.DailyCap)
		require.Equal(t, int64(1234), snapshot.EffectiveTokens)
	})

	t.Run("失败调用按保守估算计入有效水位", func(t *testing.T) {
		// 失败调用不产生 usage，但对方的配额实打实扣了。界面上要看到这部分。
		store := &fakeReclaudeUsageStore{}
		gate := NewReclaudeQuotaGate(store)
		require.NoError(t, gate.RecordUpstreamCall(ctx, account, 0, false))

		snapshot, err := gate.TodayUsage(ctx, account)

		require.NoError(t, err)
		require.Equal(t, ReclaudeFailedCallTokenEstimate, snapshot.EffectiveTokens)
		require.Equal(t, int64(1), snapshot.Usage.FailedUpstreamCalls)
	})

	t.Run("上限未标定时如实回 0，不伪造", func(t *testing.T) {
		// 上游不提供任何用量接口，上限只有我们自己标定的这一个来源。
		// 没标定就如实显示没标定 —— 界面上不许假装知道。
		store := &fakeReclaudeUsageStore{}
		plain, _ := reclaudeTestAccount(t)

		snapshot, err := NewReclaudeQuotaGate(store).TodayUsage(ctx, plain)

		require.NoError(t, err)
		require.Zero(t, snapshot.DailyCap)
	})

	t.Run("非 reclaude 账号报错", func(t *testing.T) {
		_, err := NewReclaudeQuotaGate(&fakeReclaudeUsageStore{}).TodayUsage(
			ctx, &Account{ID: 1, Type: AccountTypeOAuth})

		require.Error(t, err)
	})

	t.Run("计数器缺席时报错而不是回 0", func(t *testing.T) {
		// 回 0 会被读成「今天还没用」，而真相是「我们不知道」。
		_, err := NewReclaudeQuotaGate(nil).TodayUsage(ctx, account)

		require.Error(t, err)
	})
}
