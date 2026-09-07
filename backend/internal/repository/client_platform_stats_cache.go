package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// 观察期计数（审计 C-1 按平台分池的第一阶段）。挂在 repeatPayloadCache 同一个结构上，
// 复用它的 Redis 连接与 wire 注入，handler 侧按接口断言取用，不改 DI。
//
// Redis 结构：
//
//	client_platform:stats:{YYYY-MM-DD}   HASH  field={平台}|{来源}|{cc|other} → 次数，30 天过期
//	client_platform:session:{sessionHash} STRING 会话首次判定的平台，只写一次（NX），TTL 由调用方给
const (
	clientPlatformStatsPrefix    = "client_platform:stats:"
	clientPlatformIdentityPrefix = "client_platform:identity:"
	clientPlatformSessionPrefix  = "client_platform:session:"
	clientPlatformStatsTTL       = 30 * 24 * time.Hour
)

var clientPlatformStatsIncrScript = redis.NewScript(`
	local key = KEYS[1]
	redis.call('HINCRBY', key, ARGV[1], 1)
	if redis.call('TTL', key) == -1 then
		redis.call('EXPIRE', key, tonumber(ARGV[2]))
	end
	return 1
`)

// 只写一次并返回旧值：首次返回空串。
var clientPlatformSessionScript = redis.NewScript(`
	local key = KEYS[1]
	local prev = redis.call('GET', key)
	if not prev then
		redis.call('SET', key, ARGV[1], 'PX', tonumber(ARGV[2]))
		return ''
	end
	return prev
`)

func (c *repeatPayloadCache) IncrClientPlatformStat(ctx context.Context, day, field string) error {
	if c == nil || c.rdb == nil || day == "" || field == "" {
		return nil
	}
	key := clientPlatformStatsPrefix + day
	if err := clientPlatformStatsIncrScript.Run(ctx, c.rdb, []string{key}, field, int64(clientPlatformStatsTTL.Seconds())).Err(); err != nil {
		return fmt.Errorf("incr client platform stat: %w", err)
	}
	return nil
}

// IncrClientIdentityStat 记 HTTP 身份组合：client_platform:identity:{day}，30 天过期。
func (c *repeatPayloadCache) IncrClientIdentityStat(ctx context.Context, day, field string) error {
	if c == nil || c.rdb == nil || day == "" || field == "" {
		return nil
	}
	key := clientPlatformIdentityPrefix + day
	if err := clientPlatformStatsIncrScript.Run(ctx, c.rdb, []string{key}, field, int64(clientPlatformStatsTTL.Seconds())).Err(); err != nil {
		return fmt.Errorf("incr client identity stat: %w", err)
	}
	return nil
}

func (c *repeatPayloadCache) RecordSessionClientPlatform(ctx context.Context, sessionHash, platform string, ttl time.Duration) (string, error) {
	if c == nil || c.rdb == nil || sessionHash == "" || platform == "" || ttl <= 0 {
		return "", nil
	}
	key := clientPlatformSessionPrefix + sessionHash
	prev, err := clientPlatformSessionScript.Run(ctx, c.rdb, []string{key}, platform, ttl.Milliseconds()).Text()
	if err != nil {
		return "", fmt.Errorf("record session client platform: %w", err)
	}
	return prev, nil
}
