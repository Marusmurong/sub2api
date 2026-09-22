package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 分片 = 1 台设备 1 个 group。
//
// 为什么不是"约定"而是硬校验：sub2api 的**组内 failover 是自动的**
// （FailoverContinue 会转给组内下一个账号）。两个 rec 账号放同一 group，
// 「片内自动跨设备兜底」就是默认行为 —— 而那恰恰是被禁止的：
// 一台设备被封时自动把流量甩到邻片，会把邻片的包络也打穿，形成连环封号。
func TestCheckReclaudeSingleAccountPerGroup(t *testing.T) {
	deviceA := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeReclaude}
	deviceB := &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeReclaude}

	t.Run("组内已有另一台 rec 设备时拒绝", func(t *testing.T) {
		err := CheckReclaudeSingleAccountPerGroup(deviceA, []*Account{deviceB})

		require.ErrorIs(t, err, ErrReclaudeGroupNotExclusive)
	})

	t.Run("空组放行", func(t *testing.T) {
		require.NoError(t, CheckReclaudeSingleAccountPerGroup(deviceA, nil))
	})

	// 编辑既有账号时，它自己已经在组里，不该被算成冲突。
	t.Run("忽略自身", func(t *testing.T) {
		require.NoError(t, CheckReclaudeSingleAccountPerGroup(deviceA, []*Account{deviceA}))
	})

	t.Run("非 reclaude 账号不受本约束影响", func(t *testing.T) {
		selfHosted := &Account{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

		require.NoError(t, CheckReclaudeSingleAccountPerGroup(selfHosted, []*Account{
			{ID: 4, Platform: PlatformAnthropic, Type: AccountTypeOAuth},
		}))
	})

	// 组里混了自建号由分组隔离（V-6）负责报错，本约束只管"几台 rec 设备"。
	t.Run("只统计 rec 账号", func(t *testing.T) {
		selfHosted := &Account{ID: 5, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

		require.NoError(t, CheckReclaudeSingleAccountPerGroup(deviceA, []*Account{selfHosted}))
	})
}
