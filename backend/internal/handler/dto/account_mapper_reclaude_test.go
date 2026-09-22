package dto

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// 🔴 准入控制的配置必须能**读回来**。
//
// 只写不读的后果比想象中糟：编辑弹窗打开时字段是空的，运维点保存就把已生效的
// 限额悄悄抹了 —— 而抹掉之后的表现是「这个号不再受限」，没有任何报错。
func TestAccountFromService_ReclaudeAdmissionFields(t *testing.T) {
	reclaude := &service.Account{
		ID:       9,
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeReclaude,
		Extra: map[string]any{
			"max_devices":           4,
			"device_window_minutes": 60,
			"max_devices_daily":     8,
			"max_sessions":          3,
			"base_rpm":              10,
			"window_cost_limit":     100.0,
		},
	}

	out := AccountFromServiceShallow(reclaude)
	require.NotNil(t, out)

	t.Run("设备数读得回来", func(t *testing.T) {
		require.NotNil(t, out.MaxDevices, "写进去读不回来 ⇒ 编辑时会被静默清空")
		require.Equal(t, 4, *out.MaxDevices)
		require.NotNil(t, out.MaxDevicesDaily)
		require.Equal(t, 8, *out.MaxDevicesDaily)
	})

	t.Run("会话数读得回来", func(t *testing.T) {
		require.NotNil(t, out.MaxSessions)
		require.Equal(t, 3, *out.MaxSessions)
	})

	t.Run("RPM 读得回来", func(t *testing.T) {
		require.NotNil(t, out.BaseRPM)
		require.Equal(t, 10, *out.BaseRPM)
	})

	t.Run("窗口费用不返回：它对 reclaude 根本不生效", func(t *testing.T) {
		// 返回一个不生效的限额，比不返回更误导 —— 页面会显示「已设 $100」，
		// 而调度端从来不看它。
		require.Nil(t, out.WindowCostLimit)
	})
}

// 防回归：oauth 账号的字段一个都不能少。
func TestAccountFromService_OAuthKeepsAllAdmissionFields(t *testing.T) {
	oauth := &service.Account{
		ID:       1,
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeOAuth,
		Extra: map[string]any{
			"max_devices":       4,
			"max_sessions":      3,
			"base_rpm":          10,
			"window_cost_limit": 100.0,
		},
	}

	out := AccountFromServiceShallow(oauth)

	require.NotNil(t, out.MaxDevices)
	require.NotNil(t, out.MaxSessions)
	require.NotNil(t, out.BaseRPM)
	require.NotNil(t, out.WindowCostLimit)
}
