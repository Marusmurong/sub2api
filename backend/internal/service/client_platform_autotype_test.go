//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func anthropicAccounts(platforms ...string) []Account {
	out := make([]Account, 0, len(platforms))
	for i, p := range platforms {
		a := Account{ID: int64(i + 1), Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{}}
		if p != "" {
			a.Extra[AccountClientPlatformExtraKey] = p
		}
		out = append(out, a)
	}
	return out
}

// 线上实测占比（2026-09-08~09 两天窗口）：Windows 86.4%、macOS 10.7%。
var liveShares = map[ClientPlatform]float64{
	ClientPlatformWindowsX64: 0.864,
	ClientPlatformMacOSArm64: 0.107,
	ClientPlatformMacOSX64:   0.011,
	ClientPlatformLinuxX64:   0.015,
}

func TestPickClientPlatformFollowsQuota(t *testing.T) {
	cases := []struct {
		name     string
		existing []Account
		want     ClientPlatform
	}{
		{"空池：第一个号给占比最大的平台", nil, ClientPlatformWindowsX64},
		{"两个 macOS 老号：Windows 缺口最大", anthropicAccounts("macos-arm64", "macos-arm64"), ClientPlatformWindowsX64},
		{"2 macOS + 3 Windows（09-09 现状）：仍缺 Windows",
			anthropicAccounts("macos-arm64", "macos-arm64", "windows-x64", "windows-x64", "windows-x64"),
			ClientPlatformWindowsX64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := PickClientPlatformForNewAccount(tc.existing, liveShares, ClientPlatformMacOSArm64)
			require.Equal(t, tc.want, got)
			require.Equal(t, "quota", reason)
		})
	}
}

// Windows 吃满配额之后必须改口，否则"智能分配"和写死没区别。
func TestPickClientPlatformSwitchesOnceMajoritySaturated(t *testing.T) {
	// 9 个号全是 Windows，加第 10 个：
	//   windows target = round(0.864*10) = 9，已有 9 → 无缺口
	//   macos-arm64 target = round(0.107*10) = 1，已有 0 → 缺 1
	existing := anthropicAccounts("windows-x64", "windows-x64", "windows-x64", "windows-x64", "windows-x64",
		"windows-x64", "windows-x64", "windows-x64", "windows-x64")
	got, reason := PickClientPlatformForNewAccount(existing, liveShares, ClientPlatformMacOSArm64)
	require.Equal(t, ClientPlatformMacOSArm64, got)
	require.Equal(t, "quota", reason)
}

// 没有观察数据时给确定值：留成未定型会退回全池默认身份，正是要消除的形态。
func TestPickClientPlatformFallsBackWithoutShares(t *testing.T) {
	got, reason := PickClientPlatformForNewAccount(nil, map[ClientPlatform]float64{}, ClientPlatformWindowsX64)
	require.Equal(t, ClientPlatformWindowsX64, got)
	require.Equal(t, "fallback_no_shares", reason)
}

// shares 是 map，遍历顺序随机；同缺口同占比时结果必须稳定。
func TestPickClientPlatformIsDeterministic(t *testing.T) {
	tied := map[ClientPlatform]float64{
		ClientPlatformWindowsX64: 0.5,
		ClientPlatformMacOSArm64: 0.5,
	}
	first, _ := PickClientPlatformForNewAccount(nil, tied, ClientPlatformLinuxX64)
	for i := 0; i < 50; i++ {
		got, _ := PickClientPlatformForNewAccount(nil, tied, ClientPlatformLinuxX64)
		require.Equal(t, first, got, "同样输入必须给同样结果")
	}
}

// 未定型的号也要计入总数：否则它们会被反复算成缺口。
func TestPickClientPlatformCountsUntypedInTotal(t *testing.T) {
	// 3 个未定型 + 待建 1 个 = total 4；windows target = round(0.864*4) = 3
	got, _ := PickClientPlatformForNewAccount(anthropicAccounts("", "", ""), liveShares, ClientPlatformMacOSArm64)
	require.Equal(t, ClientPlatformWindowsX64, got)
}

// 非 Anthropic 账号不进配额计算。
func TestPickClientPlatformIgnoresOtherPlatforms(t *testing.T) {
	existing := anthropicAccounts("windows-x64")
	existing = append(existing, Account{ID: 99, Platform: PlatformOpenAI, Type: AccountTypeOAuth})
	// 只有 1 个 anthropic 号 + 待建 1 = total 2；windows target = round(1.728) = 2，已有 1 → 缺 1
	got, _ := PickClientPlatformForNewAccount(existing, liveShares, ClientPlatformMacOSArm64)
	require.Equal(t, ClientPlatformWindowsX64, got)
}

func autoTypeService(autoType *bool) *adminServiceImpl {
	return &adminServiceImpl{cfg: &config.Config{Gateway: config.GatewayConfig{
		ClientPlatformPool: config.ClientPlatformPoolConfig{
			// Enabled 保持 false：定型必须与分池路由解耦，这正是本次改动的目的。
			Enabled: false, AutoType: autoType, DefaultPlatform: "macos-arm64", ShareWindowDays: 2,
		},
	}}}
}

func TestAutoTypeGuards(t *testing.T) {
	ctx := context.Background()
	off := false

	// 分池路由关着也要定型
	got := autoTypeService(nil).autoTypeNewAnthropicAccount(ctx, PlatformAnthropic, AccountTypeOAuth, map[string]any{})
	require.Equal(t, "macos-arm64", got[AccountClientPlatformExtraKey], "无观察数据时按 default_platform 兜底")

	// 显式指定的一律优先，不被覆盖
	explicit := map[string]any{AccountClientPlatformExtraKey: "windows-x64"}
	got = autoTypeService(nil).autoTypeNewAnthropicAccount(ctx, PlatformAnthropic, AccountTypeOAuth, explicit)
	require.Equal(t, "windows-x64", got[AccountClientPlatformExtraKey])

	// 开关关掉不动
	got = autoTypeService(&off).autoTypeNewAnthropicAccount(ctx, PlatformAnthropic, AccountTypeOAuth, map[string]any{})
	require.NotContains(t, got, AccountClientPlatformExtraKey)

	// 非 Anthropic / 非 OAuth 不动
	got = autoTypeService(nil).autoTypeNewAnthropicAccount(ctx, PlatformOpenAI, AccountTypeOAuth, map[string]any{})
	require.NotContains(t, got, AccountClientPlatformExtraKey)
	got = autoTypeService(nil).autoTypeNewAnthropicAccount(ctx, PlatformAnthropic, AccountTypeAPIKey, map[string]any{})
	require.NotContains(t, got, AccountClientPlatformExtraKey)

	// 不得就地改调用方传进来的 map
	in := map[string]any{"base_rpm": 25}
	_ = autoTypeService(nil).autoTypeNewAnthropicAccount(ctx, PlatformAnthropic, AccountTypeOAuth, in)
	require.NotContains(t, in, AccountClientPlatformExtraKey)
}
