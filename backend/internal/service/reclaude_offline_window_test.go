package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func offlineAccount(t *testing.T, timezone string, startHour, durationHours int) *Account {
	t.Helper()
	return &Account{
		ID:       9,
		Platform: PlatformAnthropic,
		Type:     AccountTypeReclaude,
		Extra: map[string]any{
			ExtraKeyReclaudeOfflineWindow: map[string]any{
				"timezone":       timezone,
				"start_hour":     float64(startHour),
				"duration_hours": float64(durationHours),
			},
		},
	}
}

// 异常的不是「永远亮着」，而是「从不关机」——
// 所以要模拟一台被合上的笔记本：整段停心跳 + 停调度。
func TestReclaudeOfflineWindow(t *testing.T) {
	t.Run("窗口内判定为离线", func(t *testing.T) {
		account := offlineAccount(t, "Asia/Shanghai", 2, 6) // 02:00–08:00 +08
		at := time.Date(2026, 9, 21, 3, 0, 0, 0, mustLoadLocation(t, "Asia/Shanghai"))

		until, offline := ReclaudeOfflineUntil(account, at)

		require.True(t, offline)
		require.True(t, until.After(at))
	})

	t.Run("窗口外判定为在线", func(t *testing.T) {
		account := offlineAccount(t, "Asia/Shanghai", 2, 6)
		at := time.Date(2026, 9, 21, 12, 0, 0, 0, mustLoadLocation(t, "Asia/Shanghai"))

		_, offline := ReclaudeOfflineUntil(account, at)

		require.False(t, offline)
	})

	t.Run("跨午夜的窗口", func(t *testing.T) {
		account := offlineAccount(t, "Asia/Shanghai", 23, 4) // 23:00–03:00
		shanghai := mustLoadLocation(t, "Asia/Shanghai")

		for _, hour := range []int{23, 0, 1, 2} {
			at := time.Date(2026, 9, 21, hour, 30, 0, 0, shanghai)
			_, offline := ReclaudeOfflineUntil(account, at)
			require.Truef(t, offline, "hour=%d 应当在离线窗口内", hour)
		}

		at := time.Date(2026, 9, 21, 4, 0, 0, 0, shanghai)
		_, offline := ReclaudeOfflineUntil(account, at)
		require.False(t, offline)
	})

	// 🔴 窗口必须是账号级的：N 台"个人电脑"每天在同一分钟一起合盖，
	// 比版本号集体同步更显眼（它每天发生一次），且与"每台设备时区不同"矛盾。
	t.Run("同一 UTC 时刻下不同时区的账号不会一起合盖", func(t *testing.T) {
		shanghai := offlineAccount(t, "Asia/Shanghai", 2, 6)
		newYork := offlineAccount(t, "America/New_York", 2, 6)
		at := time.Date(2026, 9, 20, 18, 0, 0, 0, time.UTC) // 上海 02:00 / 纽约 14:00

		_, shanghaiOffline := ReclaudeOfflineUntil(shanghai, at)
		_, newYorkOffline := ReclaudeOfflineUntil(newYork, at)

		require.True(t, shanghaiOffline)
		require.False(t, newYorkOffline, "两台设备在同一分钟一起合盖就是批量特征")
	})

	t.Run("未配置窗口时永不离线", func(t *testing.T) {
		account := &Account{ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude}

		_, offline := ReclaudeOfflineUntil(account, time.Now())

		require.False(t, offline)
	})

	t.Run("非法时区退回 UTC 而不是报错停机", func(t *testing.T) {
		account := offlineAccount(t, "Not/AZone", 2, 6)
		at := time.Date(2026, 9, 21, 3, 0, 0, 0, time.UTC)

		_, offline := ReclaudeOfflineUntil(account, at)

		require.True(t, offline)
	})

	t.Run("非法时长视为未配置", func(t *testing.T) {
		for _, duration := range []int{0, -1, 25} {
			account := offlineAccount(t, "UTC", 2, duration)
			_, offline := ReclaudeOfflineUntil(account, time.Date(2026, 9, 21, 3, 0, 0, 0, time.UTC))
			require.Falsef(t, offline, "duration=%d 应当被当作未配置", duration)
		}
	})

	t.Run("非 reclaude 账号不受影响", func(t *testing.T) {
		account := offlineAccount(t, "UTC", 0, 24)
		account.Type = AccountTypeOAuth

		_, offline := ReclaudeOfflineUntil(account, time.Now())

		require.False(t, offline)
	})
}

// 停机理由必须与「节点不可用」可区分，否则运维看到冷却分不清是作息还是故障。
func TestReclaudeOfflineReasonIsDistinct(t *testing.T) {
	require.NotEqual(t, ReclaudeOfflineReason, ReclaudeGatewayUnavailableReason)
	require.Contains(t, ReclaudeOfflineReason, "offline_window")
}

// 建号时按该设备人设时区生成带随机抖动的作息 —— 不抖动就是批量特征。
func TestGenerateReclaudeOfflineWindow(t *testing.T) {
	t.Run("产出可用配置", func(t *testing.T) {
		window := GenerateReclaudeOfflineWindow("Asia/Shanghai")

		require.Equal(t, "Asia/Shanghai", window["timezone"])
		require.GreaterOrEqual(t, window["start_hour"], 0)
		require.Less(t, window["start_hour"], 24)
		require.Positive(t, window["duration_hours"])
	})

	t.Run("多次生成不应当全都相同", func(t *testing.T) {
		seen := map[int]bool{}
		for i := 0; i < 40; i++ {
			seen[GenerateReclaudeOfflineWindow("UTC")["start_hour"].(int)] = true
		}

		require.Greater(t, len(seen), 1, "作息没有抖动，N 台设备会在同一分钟一起合盖")
	})
}

func mustLoadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	require.NoError(t, err)
	return location
}
