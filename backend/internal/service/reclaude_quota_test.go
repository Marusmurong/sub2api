package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeDailyUsageStore 用内存代替 Redis。
type fakeDailyUsageStore struct {
	usage map[string]ReclaudeDailyUsage
	err   error
}

func newFakeDailyUsageStore() *fakeDailyUsageStore {
	return &fakeDailyUsageStore{usage: map[string]ReclaudeDailyUsage{}}
}

func (s *fakeDailyUsageStore) key(accountID int64, day string) string {
	return day + ":" + time.Duration(accountID).String()
}

func (s *fakeDailyUsageStore) AddReclaudeDailyUsage(
	_ context.Context, accountID int64, day string, delta ReclaudeDailyUsage,
) (ReclaudeDailyUsage, error) {
	if s.err != nil {
		return ReclaudeDailyUsage{}, s.err
	}
	current := s.usage[s.key(accountID, day)]
	current.Tokens += delta.Tokens
	current.UpstreamCalls += delta.UpstreamCalls
	current.FailedUpstreamCalls += delta.FailedUpstreamCalls
	s.usage[s.key(accountID, day)] = current
	return current, nil
}

func (s *fakeDailyUsageStore) GetReclaudeDailyUsage(
	_ context.Context, accountID int64, day string,
) (ReclaudeDailyUsage, error) {
	if s.err != nil {
		return ReclaudeDailyUsage{}, s.err
	}
	return s.usage[s.key(accountID, day)], nil
}

func reclaudeAccountWithCap(id int64, cap int64) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformAnthropic,
		Type:     AccountTypeReclaude,
		Extra:    map[string]any{ExtraKeyReclaudeDailyTokenCap: float64(cap)},
	}
}

// 口径：**上游调用次数 + 已知 token** 双计。
// 一个下游请求最多打出 ~7 次上游调用，失败的那几次返回 nil usage、不进计费链路，
// 但对方的配额是实打实扣了的 —— 按 0 计等于系统性低估水位。
func TestReclaudeDailyUsage_EffectiveTokens(t *testing.T) {
	t.Run("成功调用按真实 token 计", func(t *testing.T) {
		usage := ReclaudeDailyUsage{Tokens: 1000, UpstreamCalls: 3}

		require.Equal(t, int64(1000), usage.EffectiveTokens())
	})

	t.Run("失败调用按保守估算计入而不是按 0", func(t *testing.T) {
		usage := ReclaudeDailyUsage{Tokens: 1000, UpstreamCalls: 5, FailedUpstreamCalls: 2}

		require.Equal(t, int64(1000)+2*ReclaudeFailedCallTokenEstimate, usage.EffectiveTokens())
	})

	t.Run("全失败时水位不为 0", func(t *testing.T) {
		usage := ReclaudeDailyUsage{UpstreamCalls: 4, FailedUpstreamCalls: 4}

		require.Positive(t, usage.EffectiveTokens())
	})
}

func TestReclaudeQuotaGate(t *testing.T) {
	ctx := context.Background()

	t.Run("未超闸时放行", func(t *testing.T) {
		store := newFakeDailyUsageStore()
		gate := NewReclaudeQuotaGate(store)
		account := reclaudeAccountWithCap(9, 1_000_000)
		_, err := store.AddReclaudeDailyUsage(ctx, 9, reclaudeUsageDay(time.Now()), ReclaudeDailyUsage{Tokens: 10})
		require.NoError(t, err)

		require.False(t, gate.IsDailyCapExceeded(ctx, account))
	})

	t.Run("超闸后不可调度", func(t *testing.T) {
		store := newFakeDailyUsageStore()
		gate := NewReclaudeQuotaGate(store)
		account := reclaudeAccountWithCap(9, 1_000)
		_, err := store.AddReclaudeDailyUsage(ctx, 9, reclaudeUsageDay(time.Now()), ReclaudeDailyUsage{Tokens: 1_000})
		require.NoError(t, err)

		require.True(t, gate.IsDailyCapExceeded(ctx, account))
	})

	// 上限为 0 = 包络未标定。未标定就调度等于超卖，而超卖在这个模型里
	// 没有「临时加号顶上」的补救手段。
	t.Run("日上限为 0 时一律不可调度", func(t *testing.T) {
		store := newFakeDailyUsageStore()
		gate := NewReclaudeQuotaGate(store)
		account := reclaudeAccountWithCap(9, 0)

		require.True(t, gate.IsDailyCapExceeded(ctx, account), "未标定包络不得调度")
	})

	t.Run("缺少上限字段时一律不可调度", func(t *testing.T) {
		store := newFakeDailyUsageStore()
		gate := NewReclaudeQuotaGate(store)
		account := &Account{ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude}

		require.True(t, gate.IsDailyCapExceeded(ctx, account))
	})

	// 计数器挂了不能变成「无限放行」—— 那正好是最危险的方向。
	t.Run("计数器故障时保守判定为超闸", func(t *testing.T) {
		store := newFakeDailyUsageStore()
		store.err = context.DeadlineExceeded
		gate := NewReclaudeQuotaGate(store)

		require.True(t, gate.IsDailyCapExceeded(ctx, reclaudeAccountWithCap(9, 1_000_000)))
	})

	t.Run("计数器缺席时保守判定为超闸", func(t *testing.T) {
		gate := NewReclaudeQuotaGate(nil)

		require.True(t, gate.IsDailyCapExceeded(ctx, reclaudeAccountWithCap(9, 1_000_000)))
	})

	t.Run("非 reclaude 账号不受本闸影响", func(t *testing.T) {
		gate := NewReclaudeQuotaGate(nil)
		account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

		require.False(t, gate.IsDailyCapExceeded(ctx, account))
	})

	t.Run("记账把 token 与调用次数一起写进去", func(t *testing.T) {
		store := newFakeDailyUsageStore()
		gate := NewReclaudeQuotaGate(store)
		account := reclaudeAccountWithCap(9, 1_000_000)

		require.NoError(t, gate.RecordUpstreamCall(ctx, account, 320, true))
		require.NoError(t, gate.RecordUpstreamCall(ctx, account, 0, false))

		usage, err := store.GetReclaudeDailyUsage(ctx, 9, reclaudeUsageDay(time.Now()))
		require.NoError(t, err)
		require.Equal(t, int64(320), usage.Tokens)
		require.Equal(t, int64(2), usage.UpstreamCalls)
		require.Equal(t, int64(1), usage.FailedUpstreamCalls)
	})
}

// 周闸是软闸：告警，不硬停。
func TestReclaudeWeeklySoftLimit(t *testing.T) {
	t.Run("按日闸 ×7×0.8 推导", func(t *testing.T) {
		require.Equal(t, int64(5_600), ReclaudeWeeklySoftLimit(1_000))
	})

	t.Run("日闸为 0 时软闸也是 0", func(t *testing.T) {
		require.Zero(t, ReclaudeWeeklySoftLimit(0))
	})
}

// 每次重试都是从有限的配额包里扣钱，而这个包加不了号。
func TestShouldRetryUpstreamError_Reclaude(t *testing.T) {
	gateway := &GatewayService{}

	t.Run("reclaude 账号一律不重试", func(t *testing.T) {
		account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeReclaude}

		for _, status := range []int{400, 401, 403, 429, 500, 502, 529} {
			require.Falsef(t, gateway.shouldRetryUpstreamError(account, status), "status=%d", status)
		}
	})

	t.Run("OAuth 账号行为不变", func(t *testing.T) {
		account := &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

		require.True(t, gateway.shouldRetryUpstreamError(account, 403))
		require.False(t, gateway.shouldRetryUpstreamError(account, 500))
	})
}
