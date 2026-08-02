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

func newRepeatPayloadCacheForTest(t *testing.T) (*repeatPayloadCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &repeatPayloadCache{rdb: rdb}, mr
}

// 计数必须单调递增，且 TTL 只在首次创建时设置。
//
// 固定窗口是刻意的：若每次命中都续期，被拦下的客户端只要继续重试就永远出不来。
func TestRepeatPayloadCache_CountsAndKeepsFixedWindow(t *testing.T) {
	ctx := context.Background()
	cache, mr := newRepeatPayloadCacheForTest(t)
	const window = 30 * time.Minute

	for i := int64(1); i <= 5; i++ {
		got, err := cache.IncrementRepeatCount(ctx, service.RepeatPayloadScopeMessages, 83, "fp1", window)
		require.NoError(t, err)
		require.Equal(t, i, got)
	}

	key := buildRepeatPayloadKey(service.RepeatPayloadScopeMessages, 83, "fp1")
	require.True(t, mr.Exists(key))
	require.Equal(t, window, mr.TTL(key))

	// 窗口走掉一半后再命中，TTL 不得被续期。
	mr.FastForward(10 * time.Minute)
	_, err := cache.IncrementRepeatCount(ctx, service.RepeatPayloadScopeMessages, 83, "fp1", window)
	require.NoError(t, err)
	require.Equal(t, 20*time.Minute, mr.TTL(key), "固定窗口不得随后续命中续期")

	// 窗口到期后自动清零，客户端自行恢复。
	mr.FastForward(21 * time.Minute)
	got, err := cache.IncrementRepeatCount(ctx, service.RepeatPayloadScopeMessages, 83, "fp1", window)
	require.NoError(t, err)
	require.Equal(t, int64(1), got, "窗口过期后应重新计数")
}

// scope / api_key / 指纹三个维度必须互相隔离。
func TestRepeatPayloadCache_KeyDimensionsAreIsolated(t *testing.T) {
	ctx := context.Background()
	cache, _ := newRepeatPayloadCacheForTest(t)
	const window = time.Minute

	bump := func(scope service.RepeatPayloadScope, keyID int64, fp string) int64 {
		got, err := cache.IncrementRepeatCount(ctx, scope, keyID, fp, window)
		require.NoError(t, err)
		return got
	}

	require.Equal(t, int64(1), bump(service.RepeatPayloadScopeMessages, 83, "fp"))
	require.Equal(t, int64(1), bump(service.RepeatPayloadScopeCountTokens, 83, "fp"), "count_tokens 不得与 messages 共用计数")
	require.Equal(t, int64(1), bump(service.RepeatPayloadScopeMessages, 84, "fp"), "不同 api_key 不得互相影响")
	require.Equal(t, int64(1), bump(service.RepeatPayloadScopeMessages, 83, "other"), "不同指纹不得互相影响")
	require.Equal(t, int64(2), bump(service.RepeatPayloadScopeMessages, 83, "fp"))
}

// TTL 自愈：INCR 成功但 EXPIRE 之前进程崩溃会留下永不过期的 key，
// 没有自愈分支该指纹就被永久计数，客户端再也恢复不了。
func TestRepeatPayloadCache_RepairsMissingTTL(t *testing.T) {
	ctx := context.Background()
	cache, mr := newRepeatPayloadCacheForTest(t)
	const window = 30 * time.Minute

	key := buildRepeatPayloadKey(service.RepeatPayloadScopeMessages, 83, "fp1")
	require.NoError(t, mr.Set(key, "7")) // 模拟崩溃残留：有值，无 TTL
	require.Equal(t, time.Duration(0), mr.TTL(key))

	got, err := cache.IncrementRepeatCount(ctx, service.RepeatPayloadScopeMessages, 83, "fp1", window)
	require.NoError(t, err)
	require.Equal(t, int64(8), got)
	require.Equal(t, window, mr.TTL(key), "缺失的 TTL 必须被补上，否则计数永不过期")
}

// 空守卫：返回 (0, nil) 让调用方按 fail-open 放行，0 次不会触发任何阈值。
func TestRepeatPayloadCache_GuardsReturnZeroWithoutError(t *testing.T) {
	ctx := context.Background()
	cache, _ := newRepeatPayloadCacheForTest(t)

	tests := []struct {
		name        string
		cache       *repeatPayloadCache
		fingerprint string
		window      time.Duration
	}{
		{name: "nil receiver", cache: nil, fingerprint: "fp", window: time.Minute},
		{name: "nil client", cache: &repeatPayloadCache{}, fingerprint: "fp", window: time.Minute},
		{name: "空指纹", cache: cache, fingerprint: "", window: time.Minute},
		{name: "非正窗口", cache: cache, fingerprint: "fp", window: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.cache.IncrementRepeatCount(ctx, service.RepeatPayloadScopeMessages, 83, tt.fingerprint, tt.window)
			require.NoError(t, err)
			require.Equal(t, int64(0), got)
		})
	}
}
