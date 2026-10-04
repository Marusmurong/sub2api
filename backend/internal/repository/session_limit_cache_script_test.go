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

// Redis 重启/SCRIPT FLUSH 后，批量会话计数的 pipeline 仍应返回真实值而非静默缺失。
func TestSessionLimitCache_BatchCountSurvivesScriptFlush(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache, ok := NewSessionLimitCache(rdb, 10).(*sessionLimitCache)
	require.True(t, ok)
	ctx := context.Background()

	allowed, err := cache.RegisterSession(ctx, 7, "session-a", 5, 10*time.Minute)
	require.NoError(t, err)
	require.True(t, allowed)

	require.NoError(t, rdb.ScriptFlush(ctx).Err())

	got, err := cache.GetActiveSessionCountBatch(ctx, []int64{7}, nil)
	require.NoError(t, err)
	require.Equal(t, map[int64]int{7: 1}, got)
}
