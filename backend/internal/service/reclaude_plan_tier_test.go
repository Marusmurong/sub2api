package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 档位换算错了的后果是超卖 —— 而超卖在这个模型里没有补救手段
// （配额包有限且不可热切）。所以每条换算关系都要钉住。
func TestReclaudePlanTiers(t *testing.T) {
	t.Run("拼车是把一份 20X 切给 N 个人", func(t *testing.T) {
		base, ok := FindReclaudePlanTier("20x")
		require.True(t, ok)

		two, _ := FindReclaudePlanTier("20x-carpool-2")
		four, _ := FindReclaudePlanTier("20x-carpool-4")

		require.Equal(t, base.DailyLimitUSD/2, two.DailyLimitUSD)
		require.Equal(t, base.DailyLimitUSD/4, four.DailyLimitUSD)
	})

	t.Run("5X 是 20X 的一半，不是四分之一", func(t *testing.T) {
		base, _ := FindReclaudePlanTier("20x")
		five, ok := FindReclaudePlanTier("5x")

		require.True(t, ok)
		require.Equal(t, base.DailyLimitUSD/2, five.DailyLimitUSD)
	})

	t.Run("每档都有正的日限额", func(t *testing.T) {
		// 0 会被 IsQuotaExceeded 当成「未启用配额」直接放行 —— 那等于没有闸。
		for _, tier := range ReclaudePlanTiers {
			require.Greaterf(t, tier.DailyLimitUSD, 0.0, "档位 %s 的限额为 0", tier.ID)
			require.NotEmptyf(t, tier.Label, "档位 %s 缺标签", tier.ID)
		}
	})

	t.Run("未知档位返回 false 而不是零值档", func(t *testing.T) {
		// 静默返回零值会让建号拿到 limit=0，等于没有闸。
		_, ok := FindReclaudePlanTier("不存在的档位")
		require.False(t, ok)
	})
}
