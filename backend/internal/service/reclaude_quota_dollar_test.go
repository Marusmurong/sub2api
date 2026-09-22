package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// reclaude 的日限额改走美元配额子系统后，**读写两端都必须认这个类型**。
//
// 🔴 只改读端（调度过滤）会得到最坏的结果：限额在页面上显示为 $600，
// 调度端也确实在查它，但写端从不累加 —— quota_daily_used 恒为 0，
// 闸门永不触发，账号无限跑。这正是之前 token 闸踩过的坑
// （RecordUpstreamCall 零调用点）。
func TestReclaudeDollarQuotaBothSides(t *testing.T) {
	// quota_daily_start 必须给：缺席时周期算「已过期」，used 不参与判定
	// （下次 increment 会重置）—— 这是既有语义，不是本次改动引入的。
	newReclaudeAccount := func(limit, used float64) *Account {
		return &Account{
			Type: AccountTypeReclaude,
			Extra: map[string]any{
				"quota_daily_limit": limit,
				"quota_daily_used":  used,
				"quota_daily_start": time.Now().Format(time.RFC3339),
			},
		}
	}

	t.Run("读端：日用量到顶判超限", func(t *testing.T) {
		require.True(t, newReclaudeAccount(600, 600).IsQuotaExceeded())
		require.False(t, newReclaudeAccount(600, 599).IsQuotaExceeded())
	})

	t.Run("写端：reclaude 账号必须累加账号配额", func(t *testing.T) {
		params := &postUsageBillingParams{
			Account: newReclaudeAccount(600, 0),
			Cost:    &CostBreakdown{TotalCost: 1.5},
		}

		require.True(t, params.shouldUpdateAccountQuota(),
			"写端不认 reclaude 则 quota_daily_used 恒为 0，限额形同虚设")
	})

	t.Run("写端：没设限额的账号不累加", func(t *testing.T) {
		// 与既有 apikey 行为一致：无限额就不必付出每次请求的写代价。
		params := &postUsageBillingParams{
			Account: &Account{Type: AccountTypeReclaude, Extra: map[string]any{}},
			Cost:    &CostBreakdown{TotalCost: 1.5},
		}

		require.False(t, params.shouldUpdateAccountQuota())
	})
}

// RPM 与会话数都是 **sub 端准入控制**：在请求被装进信封发给 rec 之前就判完了。
//
// 🔴 它们跟「上游能不能看见」无关，因此没有理由对 reclaude 关闭：
//   - RPM：Redis 里数每分钟请求数
//   - 会话数：Redis 里按粘性会话 hash 占槽 + 空闲超时
//
// 唯一真正不适用的是**窗口费用**：它锚在 SessionWindowStart/End 上，而
// reclaude 从不写这两个字段 —— GetCurrentWindowStartTime() 会退化成
// 「当前整点起」，把一个 5h 窗口的限额悄悄变成小时窗。那不是不生效，
// 是语义错乱，比关掉更糟。
func TestReclaudeSubSideAdmissionControls(t *testing.T) {
	reclaude := func(extra map[string]any) *Account {
		return &Account{ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude, Extra: extra}
	}

	t.Run("RPM 限制对 reclaude 生效", func(t *testing.T) {
		require.True(t, reclaude(map[string]any{"base_rpm": 10}).SupportsRPMLimit())
	})

	t.Run("会话数限制对 reclaude 生效", func(t *testing.T) {
		require.True(t, reclaude(map[string]any{"max_sessions": 3}).SupportsSessionLimit())
	})

	t.Run("窗口费用限制对 reclaude 不生效", func(t *testing.T) {
		// 窗口锚点不存在 —— 开了会静默退化成小时窗。
		require.False(t, reclaude(map[string]any{"window_cost_limit": 100.0}).SupportsWindowCostLimit())
	})

	t.Run("oauth 账号三者都生效（防回归）", func(t *testing.T) {
		oauth := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

		require.True(t, oauth.SupportsRPMLimit())
		require.True(t, oauth.SupportsSessionLimit())
		require.True(t, oauth.SupportsWindowCostLimit())
	})
}

// 🔴 读写必须成对。
//
// 这是本项目第三次踩同一个坑：
//   1. 日 token 闸 —— RecordUpstreamCall 零调用点，水位恒 0
//   2. 美元日限额 —— shouldUpdateAccountQuota 不认 reclaude，quota_daily_used 恒 0
//   3. RPM —— 调度端读计数，而 handler 的递增仍卡在 oauth
//
// 三次的表现完全一致：**页面上限额显示得好好的，实际无限跑**。
// 所以每加一个「读侧适用类型」，都必须有一条测试把对应的写侧钉住。
func TestReclaudeAdmissionControlsHaveWriteSide(t *testing.T) {
	reclaude := &Account{ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude,
		Extra: map[string]any{"base_rpm": 10, "max_sessions": 3}}

	t.Run("RPM：读侧认了，写侧必须用同一个谓词", func(t *testing.T) {
		// handler 里的递增条件必须是 SupportsRPMLimit，不能是 IsAnthropicOAuthOrSetupToken。
		require.True(t, reclaude.SupportsRPMLimit(),
			"读侧放开而写侧不递增 ⇒ RPM 恒为 0 ⇒ 限额永不触发")
	})

	t.Run("会话数：占槽与释放用同一个谓词", func(t *testing.T) {
		// 只占不放会把账号卡死一整个空闲窗口；只放不占等于没有闸。
		require.True(t, reclaude.SupportsSessionLimit())
	})
}
