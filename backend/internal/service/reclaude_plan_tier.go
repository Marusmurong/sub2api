package service

import (
	"fmt"
	"strings"
)

// ReclaudePlanTier 是 reclaude 订阅的采购档位。
//
// 🔴 这个档位是**我们自己按采购规格定的**，不是从 reclaude 拿到的 ——
// 对方不提供套餐档位（§6.9），CLI 与 API 都没有这个字段。
// 它的作用是把「买的是什么规格」换算成一个日限额，而不是声称知道上游的额度。
type ReclaudePlanTier struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// DailyLimitUSD 是日消费上限（美元）。
	//
	// 用美元而不是 token：sub2api 既有的配额子系统（quota_daily_limit +
	// 日/周/总三级 + 固定/滚动重置）就是美元计，复用它比新造一套 token 闸
	// 少一半代码，且与其它账号类型在同一个口径上对账。
	// reclaude 的成本算得出来 —— usage_logs.total_cost 按 Anthropic 官方
	// 单价计，与账号类型无关。
	DailyLimitUSD float64 `json:"daily_limit_usd"`
	Note          string  `json:"note"`
}

// reclaudeBaseline20xUSD 是 20X 档的日限额基准。
//
// 🔴 来源：2026-09-22 从生产号池实测反推 —— 三个官方 oauth 号近 7 天的
// 日峰值消费为 $592.23 / $395.88 / $356.28，且**期间没有任何一个打到限流**
// （rate_limit_reset_at 全空、5h 窗口全 allowed）。
//
// 所以 $600 是一个**下限**而非天花板：它只证明「至少能跑这么多」。
// 真实包络要等打到限流才知道。先按这个值跑，有真实数据后再调。
const reclaudeBaseline20xUSD = 600.0

// ReclaudePlanTiers 是建号时可选的档位。
//
// 换算关系由采购规格决定：
//   - 拼车是把一份 20X 订阅切给 N 个人，所以额度是 20X ÷ N
//   - 5X 对标官方 5X 规格，实测值待今天上的 5X 号对标后修正
var ReclaudePlanTiers = []ReclaudePlanTier{
	{
		ID: "20x", Label: "20X",
		DailyLimitUSD: reclaudeBaseline20xUSD,
		Note:          "对标官方 20X 用量。基准值由号池实测反推（未打到限流，属下限）",
	},
	{
		ID: "20x-carpool-2", Label: "20X 拼车-2",
		DailyLimitUSD: reclaudeBaseline20xUSD / 2,
		Note:          "一份 20X 切 2 人",
	},
	{
		ID: "20x-carpool-4", Label: "20X 拼车-4",
		DailyLimitUSD: reclaudeBaseline20xUSD / 4,
		Note:          "一份 20X 切 4 人",
	},
	{
		ID: "5x", Label: "5X",
		DailyLimitUSD: reclaudeBaseline20xUSD / 2,
		Note:          "⚠️ 暂按 20X 的一半估算，待官方 5X 号上线后按实测对标",
	},
}

// ExtraKeyReclaudePlanTier 记录该账号选的档位。
//
// 只作展示与追溯 —— 真正生效的是 quota_daily_limit。分开存是因为档位换算
// 规则会随实测调整，而已建账号的限额不该被静默改掉。
const ExtraKeyReclaudePlanTier = "reclaude_plan_tier"

// FindReclaudePlanTier 按 ID 查档位；找不到返回 false。
func FindReclaudePlanTier(id string) (ReclaudePlanTier, bool) {
	for _, tier := range ReclaudePlanTiers {
		if tier.ID == id {
			return tier, true
		}
	}
	return ReclaudePlanTier{}, false
}

// ApplyReclaudePlanTier 把档位换算成账号的美元日限额，写进 extra。
//
// 档位是限额的**唯一入口** —— 不接受手工再填 quota_daily_limit，
// 否则「到底哪个值在生效」要靠查代码才能回答。
func ApplyReclaudePlanTier(extra map[string]any, tierID string) error {
	if extra == nil {
		return fmt.Errorf("reclaude 档位：extra 为 nil")
	}
	if strings.TrimSpace(tierID) == "" {
		return fmt.Errorf("reclaude 档位：必须选择套餐档位")
	}

	tier, ok := FindReclaudePlanTier(tierID)
	if !ok {
		// 不能静默放行：拿不到限额时 IsQuotaExceeded 会把 0 当作
		// 「未启用配额」直接放行，等于这个号没有闸。
		return fmt.Errorf("reclaude 档位：未知档位 %q（可选：%s）", tierID, reclaudePlanTierIDs())
	}

	extra[ExtraKeyReclaudePlanTier] = tier.ID
	extra["quota_daily_limit"] = tier.DailyLimitUSD
	return nil
}

func reclaudePlanTierIDs() string {
	ids := make([]string, 0, len(ReclaudePlanTiers))
	for _, tier := range ReclaudePlanTiers {
		ids = append(ids, tier.ID)
	}
	return strings.Join(ids, ", ")
}
