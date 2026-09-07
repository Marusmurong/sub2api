//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClientPlatformStatsCache_IncrAndExpire(t *testing.T) {
	cache, mr := newRepeatPayloadCacheForTest(t)
	ctx := context.Background()

	require.NoError(t, cache.IncrClientPlatformStat(ctx, "2026-09-07", "macos-arm64|env_block|cc"))
	require.NoError(t, cache.IncrClientPlatformStat(ctx, "2026-09-07", "macos-arm64|env_block|cc"))
	require.NoError(t, cache.IncrClientPlatformStat(ctx, "2026-09-07", "unknown|none|other"))

	key := "client_platform:stats:2026-09-07"
	require.Equal(t, "2", mr.HGet(key, "macos-arm64|env_block|cc"))
	require.Equal(t, "1", mr.HGet(key, "unknown|none|other"))
	require.Greater(t, mr.TTL(key), time.Duration(0), "日计数键必须有过期时间")

	// 空参数不写也不报错（fail-open）
	require.NoError(t, cache.IncrClientPlatformStat(ctx, "", "x"))
	require.NoError(t, cache.IncrClientPlatformStat(ctx, "2026-09-07", ""))
}

func TestClientPlatformStatsCache_SessionWriteOnce(t *testing.T) {
	cache, mr := newRepeatPayloadCacheForTest(t)
	ctx := context.Background()

	prev, err := cache.RecordSessionClientPlatform(ctx, "sess1", "macos-arm64", time.Hour)
	require.NoError(t, err)
	require.Equal(t, "", prev, "首次记录返回空串")

	prev, err = cache.RecordSessionClientPlatform(ctx, "sess1", "windows-x64", time.Hour)
	require.NoError(t, err)
	require.Equal(t, "macos-arm64", prev, "第二次返回首次的值，且不覆盖")
	stored, err := mr.Get("client_platform:session:sess1")
	require.NoError(t, err)
	require.Equal(t, "macos-arm64", stored)

	prev, err = cache.RecordSessionClientPlatform(ctx, "", "macos-arm64", time.Hour)
	require.NoError(t, err)
	require.Equal(t, "", prev)
}

func TestClientPlatformStatsCache_NilSafe(t *testing.T) {
	var cache *repeatPayloadCache
	require.NoError(t, cache.IncrClientPlatformStat(context.Background(), "d", "f"))
	prev, err := cache.RecordSessionClientPlatform(context.Background(), "s", "p", time.Hour)
	require.NoError(t, err)
	require.Equal(t, "", prev)
}
