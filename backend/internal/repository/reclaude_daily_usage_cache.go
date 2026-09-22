package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const reclaudeDailyUsagePrefix = "reclaude:daily_usage:"

// reclaudeDailyUsageTTL 让日水位在跨日后自然清理。
//
// 取 48 小时而不是 24：日切按 UTC，保留一天余量便于跨日排查，
// 且水位本身按 day 分键，过期与否不影响判定正确性。
const reclaudeDailyUsageTTL = 48 * time.Hour

// reclaudeDailyUsageAddScript 三个计数一次原子累加，并在首次创建时设置 TTL。
//
// PTTL == -1 的分支是 TTL 自愈：HINCRBY 成功、PEXPIRE 之前进程崩溃会留下一个
// 永不过期的键，那个账号的水位就再也不会清零，设备被永久停调度。
var reclaudeDailyUsageAddScript = redis.NewScript(`
	local key = KEYS[1]
	local ttl = tonumber(ARGV[1])

	local tokens = redis.call('HINCRBY', key, 'tokens', tonumber(ARGV[2]))
	local calls  = redis.call('HINCRBY', key, 'calls', tonumber(ARGV[3]))
	local failed = redis.call('HINCRBY', key, 'failed', tonumber(ARGV[4]))

	local pttl = redis.call('PTTL', key)
	if pttl == -1 then
		redis.call('PEXPIRE', key, ttl)
	end

	return {tokens, calls, failed}
`)

type reclaudeDailyUsageCache struct {
	rdb *redis.Client
}

// NewReclaudeDailyUsageCache 创建 reclaude 日水位计数器。
func NewReclaudeDailyUsageCache(rdb *redis.Client) service.ReclaudeDailyUsageStore {
	return &reclaudeDailyUsageCache{rdb: rdb}
}

func buildReclaudeDailyUsageKey(accountID int64, day string) string {
	return fmt.Sprintf("%s%d:%s", reclaudeDailyUsagePrefix, accountID, day)
}

// AddReclaudeDailyUsage 见 service.ReclaudeDailyUsageStore。
//
// ⚠️ 与多数缓存不同，这里的错误**必须**返回而不是吞掉：闸门在读不到水位时
// 判定为超闸（停调度），吞掉错误会让它误以为水位是 0 并放行 —— 而放行方向的
// 失败在这个模型里没有补救手段。
func (c *reclaudeDailyUsageCache) AddReclaudeDailyUsage(
	ctx context.Context, accountID int64, day string, delta service.ReclaudeDailyUsage,
) (service.ReclaudeDailyUsage, error) {
	if c == nil || c.rdb == nil {
		return service.ReclaudeDailyUsage{}, fmt.Errorf("reclaude daily usage: redis is not configured")
	}

	key := buildReclaudeDailyUsageKey(accountID, day)
	values, err := reclaudeDailyUsageAddScript.Run(ctx, c.rdb, []string{key},
		reclaudeDailyUsageTTL.Milliseconds(),
		delta.Tokens, delta.UpstreamCalls, delta.FailedUpstreamCalls,
	).Int64Slice()
	if err != nil {
		return service.ReclaudeDailyUsage{}, fmt.Errorf("add reclaude daily usage: %w", err)
	}
	if len(values) != 3 {
		return service.ReclaudeDailyUsage{}, fmt.Errorf("add reclaude daily usage: unexpected reply length %d", len(values))
	}

	return service.ReclaudeDailyUsage{
		Tokens:              values[0],
		UpstreamCalls:       values[1],
		FailedUpstreamCalls: values[2],
	}, nil
}

// GetReclaudeDailyUsage 见 service.ReclaudeDailyUsageStore。
func (c *reclaudeDailyUsageCache) GetReclaudeDailyUsage(
	ctx context.Context, accountID int64, day string,
) (service.ReclaudeDailyUsage, error) {
	if c == nil || c.rdb == nil {
		return service.ReclaudeDailyUsage{}, fmt.Errorf("reclaude daily usage: redis is not configured")
	}

	key := buildReclaudeDailyUsageKey(accountID, day)
	fields, err := c.rdb.HMGet(ctx, key, "tokens", "calls", "failed").Result()
	if err != nil {
		return service.ReclaudeDailyUsage{}, fmt.Errorf("get reclaude daily usage: %w", err)
	}

	// 键不存在时三个字段都是 nil ⇒ 水位为 0。这是**合法的零值**
	// （当天还没跑过请求），不是错误。
	return service.ReclaudeDailyUsage{
		Tokens:              parseRedisCounter(fields[0]),
		UpstreamCalls:       parseRedisCounter(fields[1]),
		FailedUpstreamCalls: parseRedisCounter(fields[2]),
	}, nil
}

// parseRedisCounter 把 HMGet 返回的 any 解成计数；缺失或格式异常都按 0。
func parseRedisCounter(value any) int64 {
	text, ok := value.(string)
	if !ok {
		return 0
	}
	var parsed int64
	if _, err := fmt.Sscanf(text, "%d", &parsed); err != nil {
		return 0
	}
	return parsed
}
