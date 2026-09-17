//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

const (
	deviceA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	deviceB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	deviceC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func newDeviceLimitCacheForTest(t *testing.T) (*deviceLimitCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	mr.SetTime(time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC))
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache, ok := NewDeviceLimitCache(rdb, 360).(*deviceLimitCache)
	require.True(t, ok)
	return cache, mr
}

func TestDeviceLimitCache_RegisterNewThenExisting(t *testing.T) {
	cache, _ := newDeviceLimitCacheForTest(t)
	ctx := context.Background()

	allowed, isNew, err := cache.RegisterDevice(ctx, 1, deviceA, 1, time.Hour)
	require.NoError(t, err)
	require.True(t, allowed)
	require.True(t, isNew, "首次登记应标记为新设备")

	allowed, isNew, err = cache.RegisterDevice(ctx, 1, deviceA, 1, time.Hour)
	require.NoError(t, err)
	require.True(t, allowed)
	require.False(t, isNew, "同一设备再次登记只是刷新")
}

func TestDeviceLimitCache_RejectsWhenFullAndRoutesElsewhere(t *testing.T) {
	cache, _ := newDeviceLimitCacheForTest(t)
	ctx := context.Background()

	_, _, err := cache.RegisterDevice(ctx, 1, deviceA, 1, time.Hour)
	require.NoError(t, err)

	allowed, isNew, err := cache.RegisterDevice(ctx, 1, deviceB, 1, time.Hour)
	require.NoError(t, err)
	require.False(t, allowed, "满员后新设备应被拒")
	require.False(t, isNew)

	// 被拒不影响另一账号
	allowed, isNew, err = cache.RegisterDevice(ctx, 2, deviceB, 1, time.Hour)
	require.NoError(t, err)
	require.True(t, allowed)
	require.True(t, isNew)

	counts, err := cache.GetActiveDeviceCountBatch(ctx, []int64{1, 2, 3}, nil)
	require.NoError(t, err)
	require.Equal(t, map[int64]int{1: 1, 2: 1, 3: 0}, counts)
}

func TestDeviceLimitCache_MaxDevicesAboveOne(t *testing.T) {
	cache, _ := newDeviceLimitCacheForTest(t)
	ctx := context.Background()

	for _, d := range []string{deviceA, deviceB} {
		allowed, isNew, err := cache.RegisterDevice(ctx, 1, d, 2, time.Hour)
		require.NoError(t, err)
		require.True(t, allowed)
		require.True(t, isNew)
	}
	allowed, _, err := cache.RegisterDevice(ctx, 1, deviceC, 2, time.Hour)
	require.NoError(t, err)
	require.False(t, allowed)
}

// 滑动空闲窗口：设备安静满窗口后名额释放；期间有活动则续期。
func TestDeviceLimitCache_SlidingWindowExpiry(t *testing.T) {
	cache, mr := newDeviceLimitCacheForTest(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	window := time.Hour

	_, _, err := cache.RegisterDevice(ctx, 1, deviceA, 1, window)
	require.NoError(t, err)

	// 30 分钟后 A 再活动一次：时间戳刷新
	mr.SetTime(base.Add(30 * time.Minute))
	allowed, isNew, err := cache.RegisterDevice(ctx, 1, deviceA, 1, window)
	require.NoError(t, err)
	require.True(t, allowed)
	require.False(t, isNew)

	// 距 A 上次活动 59 分钟：仍占位，B 被拒
	mr.SetTime(base.Add(89 * time.Minute))
	allowed, _, err = cache.RegisterDevice(ctx, 1, deviceB, 1, window)
	require.NoError(t, err)
	require.False(t, allowed)

	counts, err := cache.GetActiveDeviceCountBatch(ctx, []int64{1}, map[int64]time.Duration{1: window})
	require.NoError(t, err)
	require.Equal(t, 1, counts[1])

	// 距 A 上次活动 61 分钟：名额释放，B 可登记
	mr.SetTime(base.Add(91 * time.Minute))
	counts, err = cache.GetActiveDeviceCountBatch(ctx, []int64{1}, map[int64]time.Duration{1: window})
	require.NoError(t, err)
	require.Equal(t, 0, counts[1], "过期设备应被清理")

	allowed, isNew, err = cache.RegisterDevice(ctx, 1, deviceB, 1, window)
	require.NoError(t, err)
	require.True(t, allowed)
	require.True(t, isNew)

	// A 回来时已是新设备且满员 → 被拒
	allowed, _, err = cache.RegisterDevice(ctx, 1, deviceA, 1, window)
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestDeviceLimitCache_UnregisterFreesSlotImmediately(t *testing.T) {
	cache, _ := newDeviceLimitCacheForTest(t)
	ctx := context.Background()

	_, _, err := cache.RegisterDevice(ctx, 1, deviceA, 1, time.Hour)
	require.NoError(t, err)
	require.NoError(t, cache.UnregisterDevice(ctx, 1, deviceA))

	allowed, isNew, err := cache.RegisterDevice(ctx, 1, deviceB, 1, time.Hour)
	require.NoError(t, err)
	require.True(t, allowed)
	require.True(t, isNew)

	// 幂等：释放不存在的设备与重复释放不报错
	require.NoError(t, cache.UnregisterDevice(ctx, 1, deviceA))
	require.NoError(t, cache.UnregisterDevice(ctx, 1, "missing"))
	require.NoError(t, cache.UnregisterDevice(ctx, 1, ""))
}

func TestDeviceLimitCache_ClearDevices(t *testing.T) {
	cache, _ := newDeviceLimitCacheForTest(t)
	ctx := context.Background()

	for _, d := range []string{deviceA, deviceB} {
		_, _, err := cache.RegisterDevice(ctx, 1, d, 2, time.Hour)
		require.NoError(t, err)
	}
	require.NoError(t, cache.ClearDevices(ctx, 1))
	require.NoError(t, cache.ClearDevices(ctx, 1), "清空空键应幂等")

	counts, err := cache.GetActiveDeviceCountBatch(ctx, []int64{1}, nil)
	require.NoError(t, err)
	require.Equal(t, 0, counts[1])
}

func TestDeviceLimitCache_InvalidParamsAllowWithoutRegistering(t *testing.T) {
	cache, _ := newDeviceLimitCacheForTest(t)
	ctx := context.Background()

	allowed, isNew, err := cache.RegisterDevice(ctx, 1, "", 1, time.Hour)
	require.NoError(t, err)
	require.True(t, allowed)
	require.False(t, isNew)

	allowed, isNew, err = cache.RegisterDevice(ctx, 1, deviceA, 0, time.Hour)
	require.NoError(t, err)
	require.True(t, allowed)
	require.False(t, isNew)

	counts, err := cache.GetActiveDeviceCountBatch(ctx, []int64{1}, nil)
	require.NoError(t, err)
	require.Equal(t, 0, counts[1])

	empty, err := cache.GetActiveDeviceCountBatch(ctx, nil, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

// 窗口 ≤0 时退回默认窗口，而不是把所有设备当成立即过期。
func TestDeviceLimitCache_ZeroWindowFallsBackToDefault(t *testing.T) {
	cache, mr := newDeviceLimitCacheForTest(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

	_, _, err := cache.RegisterDevice(ctx, 1, deviceA, 1, 0)
	require.NoError(t, err)

	mr.SetTime(base.Add(5 * time.Hour))
	counts, err := cache.GetActiveDeviceCountBatch(ctx, []int64{1}, map[int64]time.Duration{1: 0})
	require.NoError(t, err)
	require.Equal(t, 1, counts[1], "默认 360 分钟窗口内应仍在")

	mr.SetTime(base.Add(7 * time.Hour))
	counts, err = cache.GetActiveDeviceCountBatch(ctx, []int64{1}, nil)
	require.NoError(t, err)
	require.Equal(t, 0, counts[1])
}
