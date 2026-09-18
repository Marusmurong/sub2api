//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

const (
	deviceA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	deviceB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	deviceC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	deviceD = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

var testBase = time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

func newDeviceLimitCacheForTest(t *testing.T) (*deviceLimitCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	mr.SetTime(testBase)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache, ok := NewDeviceLimitCache(rdb, 360).(*deviceLimitCache)
	require.True(t, ok)
	return cache, mr
}

func limits(maxDevices int, window time.Duration, maxDaily int) service.DeviceLimits {
	return service.DeviceLimits{MaxDevices: maxDevices, Window: window, MaxDaily: maxDaily, DailyWindow: service.DeviceDailyWindow}
}

func register(t *testing.T, c *deviceLimitCache, acc int64, dev string, l service.DeviceLimits) service.DeviceRegistration {
	t.Helper()
	reg, err := c.RegisterDevice(context.Background(), acc, dev, l)
	require.NoError(t, err)
	return reg
}

func counts(t *testing.T, c *deviceLimitCache, acc int64, window time.Duration) service.DeviceCounts {
	t.Helper()
	out, err := c.GetDeviceCountsBatch(context.Background(), []int64{acc}, map[int64]time.Duration{acc: window})
	require.NoError(t, err)
	return out[acc]
}

func TestDeviceLimitCache_RegisterNewThenExisting(t *testing.T) {
	cache, _ := newDeviceLimitCacheForTest(t)
	l := limits(1, time.Hour, 0)

	reg := register(t, cache, 1, deviceA, l)
	require.Equal(t, service.DeviceRegistration{Allowed: true, IsNew: true, IsNewDaily: true}, reg, "首次登记：新占名额、新计 24h")

	reg = register(t, cache, 1, deviceA, l)
	require.Equal(t, service.DeviceRegistration{Allowed: true}, reg, "同一设备再次登记只是刷新")
}

func TestDeviceLimitCache_RejectsWhenConcurrentFull(t *testing.T) {
	cache, _ := newDeviceLimitCacheForTest(t)
	l := limits(1, time.Hour, 0)

	register(t, cache, 1, deviceA, l)
	reg := register(t, cache, 1, deviceB, l)
	require.False(t, reg.Allowed)
	require.Equal(t, "concurrent", reg.Reason)

	// 被拒不影响另一账号
	require.True(t, register(t, cache, 2, deviceB, l).Allowed)

	all, err := cache.GetDeviceCountsBatch(context.Background(), []int64{1, 2, 3}, nil)
	require.NoError(t, err)
	require.Equal(t, service.DeviceCounts{Active: 1, Daily: 1}, all[1])
	require.Equal(t, service.DeviceCounts{Active: 1, Daily: 1}, all[2])
	require.Equal(t, service.DeviceCounts{}, all[3])
}

// 用户给的例子：释放 3h、设备数 1、24h 上限 3 → 同一时刻 1 台、一天最多 3 台不同设备。
func TestDeviceLimitCache_DailyCapAcrossReleases(t *testing.T) {
	cache, mr := newDeviceLimitCacheForTest(t)
	l := limits(1, 3*time.Hour, 3)

	require.True(t, register(t, cache, 1, deviceA, l).Allowed)
	require.Equal(t, "concurrent", register(t, cache, 1, deviceB, l).Reason, "A 在用时 B 被并发层拒")

	mr.SetTime(testBase.Add(4 * time.Hour)) // A 闲置满 3h 释放
	require.True(t, register(t, cache, 1, deviceB, l).Allowed)
	mr.SetTime(testBase.Add(8 * time.Hour))
	require.True(t, register(t, cache, 1, deviceC, l).Allowed)
	mr.SetTime(testBase.Add(12 * time.Hour))

	reg := register(t, cache, 1, deviceD, l)
	require.False(t, reg.Allowed)
	require.Equal(t, "daily", reg.Reason, "名额空着但 24h 内已接纳 3 台，第 4 台被 24h 层拒")
	require.Equal(t, service.DeviceCounts{Active: 0, Daily: 3}, counts(t, cache, 1, 3*time.Hour))

	// 24h 内接纳过的设备回来不消耗额度，只需并发名额
	reg = register(t, cache, 1, deviceA, l)
	require.True(t, reg.Allowed)
	require.True(t, reg.IsNew, "A 重新占名额")
	require.False(t, reg.IsNewDaily, "A 仍在 24h 集合里，不再计数")

	// A 首次进入满 24h 后掉出，额度腾出一个给 D
	mr.SetTime(testBase.Add(24*time.Hour + time.Minute))
	require.Equal(t, service.DeviceCounts{Active: 0, Daily: 2}, counts(t, cache, 1, 3*time.Hour))
	require.True(t, register(t, cache, 1, deviceD, l).Allowed)
}

// 24h 额度以首次进入时间计，期间活动不续期；否则一台常驻设备会永久占着一个日额度。
func TestDeviceLimitCache_DailyWindowNotRefreshedByActivity(t *testing.T) {
	cache, mr := newDeviceLimitCacheForTest(t)
	l := limits(0, time.Hour, 1)

	require.True(t, register(t, cache, 1, deviceA, l).Allowed)
	mr.SetTime(testBase.Add(23 * time.Hour))
	require.True(t, register(t, cache, 1, deviceA, l).Allowed) // A 又活动了一次
	mr.SetTime(testBase.Add(24*time.Hour + time.Minute))
	reg := register(t, cache, 1, deviceB, l)
	require.True(t, reg.Allowed, "A 的 24h 额度按首次进入起算，此时应已释放")
}

// 只设 24h 上限、不设并发上限：并发层不拒，24h 层照常拒。
func TestDeviceLimitCache_DailyOnly(t *testing.T) {
	cache, _ := newDeviceLimitCacheForTest(t)
	l := limits(0, time.Hour, 2)

	require.True(t, register(t, cache, 1, deviceA, l).Allowed)
	require.True(t, register(t, cache, 1, deviceB, l).Allowed)
	reg := register(t, cache, 1, deviceC, l)
	require.False(t, reg.Allowed)
	require.Equal(t, "daily", reg.Reason)
	require.Equal(t, service.DeviceCounts{Active: 2, Daily: 2}, counts(t, cache, 1, time.Hour))
}

// 24h 上限设置变更 → ClearDailyDevices 清零；正占名额的设备下次活动时补入并按新值计数。
func TestDeviceLimitCache_ClearDailyRestartsCountingWithActiveDevices(t *testing.T) {
	cache, _ := newDeviceLimitCacheForTest(t)
	l := limits(3, time.Hour, 2)

	require.True(t, register(t, cache, 1, deviceA, l).Allowed)
	require.True(t, register(t, cache, 1, deviceB, l).Allowed)
	require.Equal(t, "daily", register(t, cache, 1, deviceC, l).Reason)

	require.NoError(t, cache.ClearDailyDevices(context.Background(), 1))
	require.Equal(t, service.DeviceCounts{Active: 2, Daily: 0}, counts(t, cache, 1, time.Hour), "只清 24h 记录，名额保留")

	// 新值 3：C 现在能进
	l3 := limits(3, time.Hour, 3)
	require.True(t, register(t, cache, 1, deviceC, l3).Allowed)
	reg := register(t, cache, 1, deviceA, l3)
	require.True(t, reg.Allowed)
	require.False(t, reg.IsNew, "A 仍占着名额")
	require.True(t, reg.IsNewDaily, "A 补入新一轮 24h 计数")
	require.Equal(t, service.DeviceCounts{Active: 3, Daily: 2}, counts(t, cache, 1, time.Hour))
}

func TestDeviceLimitCache_MaxDevicesAboveOne(t *testing.T) {
	cache, _ := newDeviceLimitCacheForTest(t)
	l := limits(2, time.Hour, 0)

	require.True(t, register(t, cache, 1, deviceA, l).Allowed)
	require.True(t, register(t, cache, 1, deviceB, l).Allowed)
	require.False(t, register(t, cache, 1, deviceC, l).Allowed)
}

// 滑动空闲窗口：设备安静满窗口后名额释放；期间有活动则续期。
func TestDeviceLimitCache_SlidingWindowExpiry(t *testing.T) {
	cache, mr := newDeviceLimitCacheForTest(t)
	window := time.Hour
	l := limits(1, window, 0)

	register(t, cache, 1, deviceA, l)

	mr.SetTime(testBase.Add(30 * time.Minute))
	reg := register(t, cache, 1, deviceA, l)
	require.True(t, reg.Allowed)
	require.False(t, reg.IsNew)

	mr.SetTime(testBase.Add(89 * time.Minute)) // 距 A 上次活动 59 分钟
	require.False(t, register(t, cache, 1, deviceB, l).Allowed)
	require.Equal(t, 1, counts(t, cache, 1, window).Active)

	mr.SetTime(testBase.Add(91 * time.Minute)) // 61 分钟：名额释放
	require.Equal(t, 0, counts(t, cache, 1, window).Active, "过期设备应被清理")
	reg = register(t, cache, 1, deviceB, l)
	require.True(t, reg.Allowed)
	require.True(t, reg.IsNew)

	require.False(t, register(t, cache, 1, deviceA, l).Allowed, "A 回来时名额已被 B 占")
}

func TestDeviceLimitCache_UnregisterFreesSlotImmediately(t *testing.T) {
	cache, _ := newDeviceLimitCacheForTest(t)
	l := limits(1, time.Hour, 1)
	ctx := context.Background()

	register(t, cache, 1, deviceA, l)

	t.Run("slot_only_keeps_daily", func(t *testing.T) {
		require.NoError(t, cache.UnregisterDevice(ctx, 1, deviceA, false))
		require.Equal(t, service.DeviceCounts{Active: 0, Daily: 1}, counts(t, cache, 1, time.Hour))
		require.Equal(t, "daily", register(t, cache, 1, deviceB, l).Reason, "24h 额度仍被 A 占着")
	})

	t.Run("with_daily_frees_both", func(t *testing.T) {
		require.NoError(t, cache.UnregisterDevice(ctx, 1, deviceA, true))
		require.Equal(t, service.DeviceCounts{}, counts(t, cache, 1, time.Hour))
		reg := register(t, cache, 1, deviceB, l)
		require.True(t, reg.Allowed)
		require.True(t, reg.IsNew)
		require.True(t, reg.IsNewDaily)
	})

	// 幂等：释放不存在的设备与重复释放不报错
	require.NoError(t, cache.UnregisterDevice(ctx, 1, deviceA, true))
	require.NoError(t, cache.UnregisterDevice(ctx, 1, "missing", false))
	require.NoError(t, cache.UnregisterDevice(ctx, 1, "", true))
}

func TestDeviceLimitCache_ClearDevices(t *testing.T) {
	cache, _ := newDeviceLimitCacheForTest(t)
	l := limits(2, time.Hour, 2)
	ctx := context.Background()

	register(t, cache, 1, deviceA, l)
	register(t, cache, 1, deviceB, l)
	require.NoError(t, cache.ClearDevices(ctx, 1))
	require.NoError(t, cache.ClearDevices(ctx, 1), "清空空键应幂等")
	require.Equal(t, service.DeviceCounts{}, counts(t, cache, 1, time.Hour), "两层都清")
	require.True(t, register(t, cache, 1, deviceC, l).Allowed)
}

func TestDeviceLimitCache_InvalidParamsAllowWithoutRegistering(t *testing.T) {
	cache, _ := newDeviceLimitCacheForTest(t)

	require.Equal(t, service.DeviceRegistration{Allowed: true}, register(t, cache, 1, "", limits(1, time.Hour, 0)))
	require.Equal(t, service.DeviceRegistration{Allowed: true}, register(t, cache, 1, deviceA, limits(0, time.Hour, 0)), "两层都不限时不登记")
	require.Equal(t, service.DeviceCounts{}, counts(t, cache, 1, time.Hour))

	empty, err := cache.GetDeviceCountsBatch(context.Background(), nil, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

// 窗口 ≤0 时退回默认窗口，而不是把所有设备当成立即过期。
func TestDeviceLimitCache_ZeroWindowFallsBackToDefault(t *testing.T) {
	cache, mr := newDeviceLimitCacheForTest(t)

	register(t, cache, 1, deviceA, limits(1, 0, 0))

	mr.SetTime(testBase.Add(5 * time.Hour))
	require.Equal(t, 1, counts(t, cache, 1, 0).Active, "默认 360 分钟窗口内应仍在")

	mr.SetTime(testBase.Add(7 * time.Hour))
	require.Equal(t, 0, counts(t, cache, 1, 0).Active)
}

// 设备亲和只读查询：正占名额 或 24h 内接纳过 都算；不刷新时间戳。
func TestDeviceLimitCache_ActiveDeviceAccounts(t *testing.T) {
	cache, mr := newDeviceLimitCacheForTest(t)
	ctx := context.Background()
	windows := map[int64]time.Duration{1: time.Hour, 2: time.Hour, 3: time.Hour}
	l := limits(1, time.Hour, 3)

	register(t, cache, 1, deviceA, l)
	register(t, cache, 2, deviceB, l)

	set, err := cache.ActiveDeviceAccounts(ctx, deviceA, []int64{1, 2, 3}, windows)
	require.NoError(t, err)
	require.Equal(t, map[int64]struct{}{1: {}}, set)

	// 名额释放后（61 分钟），24h 内仍算"接纳过" → 仍有亲和
	mr.SetTime(testBase.Add(61 * time.Minute))
	set, err = cache.ActiveDeviceAccounts(ctx, deviceA, []int64{1, 2, 3}, windows)
	require.NoError(t, err)
	require.Equal(t, map[int64]struct{}{1: {}}, set, "24h 内回到原账号不消耗新额度，应优先")

	// 24h 过后两层都过期 → 无亲和；且查询本身不续期
	mr.SetTime(testBase.Add(24*time.Hour + time.Minute))
	set, err = cache.ActiveDeviceAccounts(ctx, deviceA, []int64{1, 2, 3}, windows)
	require.NoError(t, err)
	require.Empty(t, set)

	empty, err := cache.ActiveDeviceAccounts(ctx, "", []int64{1}, windows)
	require.NoError(t, err)
	require.Empty(t, empty)
}
