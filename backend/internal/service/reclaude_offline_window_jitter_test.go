package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 🔴 人设时区集中到中国后（rec 客群本来就是中国用户翻墙），
// 「模拟关机」作息若只精确到**整点**，N 台设备就会在北京时间同一分钟
// 一起合盖 —— §6.10 自己点名说这比版本号同步更显眼，因为它每天发生一次。
//
// 分钟级抖动是时区集中的必要补偿。
func TestReclaudeOfflineWindowJitter(t *testing.T) {
	t.Run("同一时区生成多份作息，开始分钟必须分散", func(t *testing.T) {
		minutes := map[int]int{}
		for range 40 {
			window := GenerateReclaudeOfflineWindow("Asia/Shanghai")
			minutes[int(credentialInt64(window, "start_minute"))]++
		}

		require.Greater(t, len(minutes), 10,
			"开始分钟只有 %d 种取值 —— 同时区设备会扎堆合盖", len(minutes))
	})

	t.Run("抖动落在合法分钟范围内", func(t *testing.T) {
		for range 50 {
			window := GenerateReclaudeOfflineWindow("Asia/Shanghai")
			minute := int(credentialInt64(window, "start_minute"))

			require.GreaterOrEqual(t, minute, 0)
			require.Less(t, minute, 60)
		}
	})

	t.Run("判定时真的用上了分钟，不是只存不读", func(t *testing.T) {
		// 存了不读 = 抖动形同虚设，而表现上完全看不出来。
		account := &Account{
			Type: AccountTypeReclaude,
			Extra: map[string]any{
				ExtraKeyReclaudeOfflineWindow: map[string]any{
					"timezone":       "Asia/Shanghai",
					"start_hour":     2,
					"start_minute":   40,
					"duration_hours": 6,
				},
			},
		}
		shanghai, err := time.LoadLocation("Asia/Shanghai")
		require.NoError(t, err)

		// 02:20 在 02:40 之前 —— 还没到合盖时间。
		_, offline := ReclaudeOfflineUntil(account, time.Date(2026, 9, 23, 2, 20, 0, 0, shanghai))
		require.False(t, offline, "02:20 早于 02:40，不该判为离线")

		// 02:50 已进入窗口。
		_, offline = ReclaudeOfflineUntil(account, time.Date(2026, 9, 23, 2, 50, 0, 0, shanghai))
		require.True(t, offline)
	})

	t.Run("老账号没有 start_minute 时按整点处理", func(t *testing.T) {
		// 历史账号的 extra 里没有这个键，不能因此判成「永不离线」。
		account := &Account{
			Type: AccountTypeReclaude,
			Extra: map[string]any{
				ExtraKeyReclaudeOfflineWindow: map[string]any{
					"timezone":       "Asia/Shanghai",
					"start_hour":     2,
					"duration_hours": 6,
				},
			},
		}
		shanghai, _ := time.LoadLocation("Asia/Shanghai")

		_, offline := ReclaudeOfflineUntil(account, time.Date(2026, 9, 23, 3, 0, 0, 0, shanghai))
		require.True(t, offline)
	})
}
