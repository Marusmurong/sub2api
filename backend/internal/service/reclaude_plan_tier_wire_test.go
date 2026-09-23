package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 建号时选档位 → 自动落 quota_daily_limit，走既有的美元配额闸。
//
// 🔴 为什么不再单独维护 token 计的日闸：既有配额子系统已经有日/周/总三级 +
// 固定/滚动重置，而 reclaude 的成本算得出来（usage_logs.total_cost 按
// Anthropic 官方单价计，与账号类型无关）。两套闸并存只会让「到底哪个在生效」
// 变成一个需要查代码才能回答的问题。
func TestApplyReclaudePlanTier(t *testing.T) {
	t.Run("选档位自动落美元日限额", func(t *testing.T) {
		extra := map[string]any{}

		err := ApplyReclaudePlanTier(extra, "20x-carpool-2")

		require.NoError(t, err)
		require.Equal(t, 300.0, extra["quota_daily_limit"])
		require.Equal(t, "20x-carpool-2", extra[ExtraKeyReclaudePlanTier])
	})

	t.Run("未知档位报错，不静默放行", func(t *testing.T) {
		// 静默放行会让账号拿到 limit=0 —— IsQuotaExceeded 把 0 当「未启用配额」
		// 直接放行，等于没有闸。
		err := ApplyReclaudePlanTier(map[string]any{}, "不存在")

		require.Error(t, err)
	})

	t.Run("档位为空时报错", func(t *testing.T) {
		require.Error(t, ApplyReclaudePlanTier(map[string]any{}, ""))
	})

	t.Run("已手工设过 quota_daily_limit 时以档位为准", func(t *testing.T) {
		// 档位是唯一入口，避免两个地方各写一个值。
		extra := map[string]any{"quota_daily_limit": 999.0}

		require.NoError(t, ApplyReclaudePlanTier(extra, "20x"))
		require.Equal(t, 600.0, extra["quota_daily_limit"])
	})
}

// V-5：档位没选就建号 = 这个号没有闸。
//
// 原来这里卡的是 DailyTokenCap（token 计的日闸），现在换成档位 ——
// 档位换算出美元日限额，走既有配额子系统。
func TestValidateReclaudeAccountInput_PlanTier(t *testing.T) {
	proxyID := int64(1)
	valid := func() ReclaudeAccountInput {
		return ReclaudeAccountInput{
			ProxyID:        &proxyID,
			SK:             ReclaudeSKPrefix + "abcdefghijklmnopqrstuvwxyz",
			SeedEncoded:    validSeedHex(),
			DeviceID:       43871,
			ClientVersion:  "1.0.0",
			ClientPlatform: "darwin",
			GatewayURL:     "https://" + ReclaudeAllowedGatewayHosts[0],
			PlanTier:       "20x",
			ClaudeUserID:   "c8f2a1b09d3e4f5a6b7c8d9e0f1a2b3c4d5e6f708192a3b4c5d6e7f809a1b2c3",
			AccountUUID:    "9c67eb02-4001-4cde-a6e2-e40f1a71649e",
		}
	}

	t.Run("选了档位就通过", func(t *testing.T) {
		_, err := ValidateReclaudeAccountInput(valid())
		require.NoError(t, err)
	})

	t.Run("没选档位被拒", func(t *testing.T) {
		input := valid()
		input.PlanTier = ""

		_, err := ValidateReclaudeAccountInput(input)

		require.ErrorIs(t, err, ErrReclaudePlanTierRequired)
	})

	t.Run("未知档位被拒", func(t *testing.T) {
		input := valid()
		input.PlanTier = "50x"

		_, err := ValidateReclaudeAccountInput(input)

		require.ErrorIs(t, err, ErrReclaudePlanTierRequired)
	})

	t.Run("校验产物带出日限额，建号时直接落库", func(t *testing.T) {
		result, err := ValidateReclaudeAccountInput(valid())

		require.NoError(t, err)
		require.Equal(t, 600.0, result.DailyLimitUSD)
	})
}
