package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const repeatPayloadPrefix = "repeat_payload:"

// repeatPayloadIncrScript 原子自增并在首次创建时设置 TTL。
//
// PTTL == -1 的分支是 TTL 自愈：INCR 成功、EXPIRE 之前进程崩溃会留下一个永不过期的
// key，没有这个分支该指纹就被永久计数，客户端再也恢复不了。同 middleware/rate_limiter.go。
//
// 刻意不在后续命中时续期 —— 固定窗口，到期自动清零。
var repeatPayloadIncrScript = redis.NewScript(`
	local key = KEYS[1]
	local ttl = tonumber(ARGV[1])

	local count = redis.call('INCR', key)
	local pttl = redis.call('PTTL', key)
	if count == 1 or pttl == -1 then
		redis.call('PEXPIRE', key, ttl)
	end

	return count
`)

type repeatPayloadCache struct {
	rdb *redis.Client
}

// NewRepeatPayloadCache 创建重复 payload 计数器。
func NewRepeatPayloadCache(rdb *redis.Client) service.RepeatPayloadCache {
	return &repeatPayloadCache{rdb: rdb}
}

func buildRepeatPayloadKey(scope service.RepeatPayloadScope, apiKeyID int64, fingerprint string) string {
	return fmt.Sprintf("%s%s:%d:%s", repeatPayloadPrefix, scope, apiKeyID, fingerprint)
}

// IncrementRepeatCount 见 service.RepeatPayloadCache。
//
// 空守卫返回 (0, nil) 而不是报错：调用方按 fail-open 处理，0 次自然不会触发阈值。
func (c *repeatPayloadCache) IncrementRepeatCount(ctx context.Context, scope service.RepeatPayloadScope, apiKeyID int64, fingerprint string, window time.Duration) (int64, error) {
	if c == nil || c.rdb == nil || fingerprint == "" {
		return 0, nil
	}
	if window <= 0 {
		return 0, nil
	}

	key := buildRepeatPayloadKey(scope, apiKeyID, fingerprint)
	count, err := repeatPayloadIncrScript.Run(ctx, c.rdb, []string{key}, window.Milliseconds()).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment repeat payload count: %w", err)
	}
	return count, nil
}

// ProvideClientPlatformStatsCache 把「按客户端平台的观察计数」这一面从
// RepeatPayloadCache 上取出来单独提供。
//
// 两者共用同一个 Redis 结构（计数只是几个哈希键，没必要再开一份连接与实例），
// 但消费方不同：网关调度按占比开池要读它，建号时的自动定型也要读它。用一个
// 独立的 provider 而不是让每个消费方自己做类型断言，是为了让依赖在 wire 图里
// 显式可见——断言失败会静默退化成 nil，排查时看不出是哪一步断的。
func ProvideClientPlatformStatsCache(c service.RepeatPayloadCache) service.ClientPlatformStatsCache {
	if stats, ok := c.(service.ClientPlatformStatsCache); ok {
		return stats
	}
	return nil
}
