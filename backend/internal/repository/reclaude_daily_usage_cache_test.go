package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func newReclaudeUsageCache(t *testing.T) (service.ReclaudeDailyUsageStore, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewReclaudeDailyUsageCache(client), server, client
}

func TestReclaudeDailyUsageCache(t *testing.T) {
	ctx := context.Background()
	const day = "2026-09-21"

	t.Run("三个计数一起累加并回传当前水位", func(t *testing.T) {
		// Arrange
		cache, _, _ := newReclaudeUsageCache(t)

		// Act
		first, err := cache.AddReclaudeDailyUsage(ctx, 9, day,
			service.ReclaudeDailyUsage{Tokens: 100, UpstreamCalls: 1})
		require.NoError(t, err)
		second, err := cache.AddReclaudeDailyUsage(ctx, 9, day,
			service.ReclaudeDailyUsage{UpstreamCalls: 1, FailedUpstreamCalls: 1})
		require.NoError(t, err)

		// Assert
		require.Equal(t, int64(100), first.Tokens)
		require.Equal(t, service.ReclaudeDailyUsage{
			Tokens: 100, UpstreamCalls: 2, FailedUpstreamCalls: 1,
		}, second)
	})

	t.Run("读回与写入一致", func(t *testing.T) {
		cache, _, _ := newReclaudeUsageCache(t)
		_, err := cache.AddReclaudeDailyUsage(ctx, 9, day,
			service.ReclaudeDailyUsage{Tokens: 42, UpstreamCalls: 3, FailedUpstreamCalls: 2})
		require.NoError(t, err)

		usage, err := cache.GetReclaudeDailyUsage(ctx, 9, day)

		require.NoError(t, err)
		require.Equal(t, int64(42), usage.Tokens)
		require.Equal(t, int64(3), usage.UpstreamCalls)
		require.Equal(t, int64(2), usage.FailedUpstreamCalls)
	})

	// 当天还没跑过请求是**合法的零值**，不是错误 —— 否则每天第一个请求都会
	// 因为「读不到水位」被闸门判为超闸而停掉。
	t.Run("键不存在时返回零值而不是报错", func(t *testing.T) {
		cache, _, _ := newReclaudeUsageCache(t)

		usage, err := cache.GetReclaudeDailyUsage(ctx, 999, day)

		require.NoError(t, err)
		require.Zero(t, usage.Tokens)
		require.Zero(t, usage.EffectiveTokens())
	})

	t.Run("按账号与日期分键，互不串水位", func(t *testing.T) {
		cache, _, _ := newReclaudeUsageCache(t)
		_, err := cache.AddReclaudeDailyUsage(ctx, 9, day, service.ReclaudeDailyUsage{Tokens: 100})
		require.NoError(t, err)

		other, err := cache.GetReclaudeDailyUsage(ctx, 10, day)
		require.NoError(t, err)
		require.Zero(t, other.Tokens)

		nextDay, err := cache.GetReclaudeDailyUsage(ctx, 9, "2026-09-22")
		require.NoError(t, err)
		require.Zero(t, nextDay.Tokens)
	})

	// TTL 自愈：HINCRBY 成功、PEXPIRE 之前崩溃会留下永不过期的键，
	// 那个账号的水位再也不清零 ⇒ 设备被永久停调度。
	t.Run("首次创建设置 TTL，且能自愈丢失的 TTL", func(t *testing.T) {
		cache, server, client := newReclaudeUsageCache(t)
		_, err := cache.AddReclaudeDailyUsage(ctx, 9, day, service.ReclaudeDailyUsage{Tokens: 1})
		require.NoError(t, err)

		key := buildReclaudeDailyUsageKey(9, day)
		require.Positive(t, server.TTL(key))

		// 模拟「HINCRBY 成功但 PEXPIRE 之前崩了」留下的无 TTL 键。
		// 用真实的 PERSIST 命令，这样 PTTL 会像真 Redis 一样返回 -1。
		require.NoError(t, client.Persist(ctx, key).Err())
		require.Zero(t, server.TTL(key))

		_, err = cache.AddReclaudeDailyUsage(ctx, 9, day, service.ReclaudeDailyUsage{Tokens: 1})
		require.NoError(t, err)
		require.Positive(t, server.TTL(key), "TTL 没有自愈，水位会永不清零")
	})

	// 闸门读不到水位时判定为超闸（停调度）。吞掉 Redis 错误会让它误以为
	// 水位是 0 并放行 —— 而放行方向的失败没有补救手段。
	t.Run("Redis 故障时返回错误而不是零值", func(t *testing.T) {
		cache, server, _ := newReclaudeUsageCache(t)
		server.Close()

		_, err := cache.GetReclaudeDailyUsage(ctx, 9, day)
		require.Error(t, err)

		_, err = cache.AddReclaudeDailyUsage(ctx, 9, day, service.ReclaudeDailyUsage{Tokens: 1})
		require.Error(t, err)
	})

	t.Run("未配置 Redis 时报错而不是静默放行", func(t *testing.T) {
		cache := NewReclaudeDailyUsageCache(nil)

		_, err := cache.GetReclaudeDailyUsage(context.Background(), 9, day)
		require.Error(t, err)
	})

	t.Run("跨日后旧键自然过期", func(t *testing.T) {
		cache, server, _ := newReclaudeUsageCache(t)
		_, err := cache.AddReclaudeDailyUsage(ctx, 9, day, service.ReclaudeDailyUsage{Tokens: 1})
		require.NoError(t, err)

		server.FastForward(reclaudeDailyUsageTTL + time.Minute)

		usage, err := cache.GetReclaudeDailyUsage(ctx, 9, day)
		require.NoError(t, err)
		require.Zero(t, usage.Tokens)
	})
}
