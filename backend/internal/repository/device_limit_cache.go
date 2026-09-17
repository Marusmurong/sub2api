package repository

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// deviceLimitKeyPrefix 设备限制 Redis 键前缀
const deviceLimitKeyPrefix = "device_limit:account:"

// keyExpireGraceSeconds 键 TTL 在窗口之外的余量，保证最后一个成员过期后键自行消失
const keyExpireGraceSeconds = 60

var (
	// registerDeviceScript 登记设备（原子操作）
	// KEYS[1] = device_limit:account:{accountID}
	// ARGV[1] = maxDevices
	// ARGV[2] = window（秒）
	// ARGV[3] = deviceID
	// 返回: 0 = 拒绝, 1 = 已存在并刷新, 2 = 新登记
	registerDeviceScript = redis.NewScript(`
		redis.replicate_commands()
		local key = KEYS[1]
		local maxDevices = tonumber(ARGV[1])
		local window = tonumber(ARGV[2])
		local deviceID = ARGV[3]

		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		local expireBefore = now - window

		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)

		local exists = redis.call('ZSCORE', key, deviceID)
		if exists ~= false then
			redis.call('ZADD', key, now, deviceID)
			redis.call('EXPIRE', key, window + ` + fmt.Sprint(keyExpireGraceSeconds) + `)
			return 1
		end

		local count = redis.call('ZCARD', key)
		if count < maxDevices then
			redis.call('ZADD', key, now, deviceID)
			redis.call('EXPIRE', key, window + ` + fmt.Sprint(keyExpireGraceSeconds) + `)
			return 2
		end

		return 0
	`)

	// getActiveDeviceCountScript 清理过期后返回活跃设备数
	// KEYS[1] = device_limit:account:{accountID}
	// ARGV[1] = window（秒）
	getActiveDeviceCountScript = redis.NewScript(`
		redis.replicate_commands()
		local key = KEYS[1]
		local window = tonumber(ARGV[1])

		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		local expireBefore = now - window

		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)

		return redis.call('ZCARD', key)
	`)

	// isDeviceActiveScript 只读检查设备是否已登记且未过期（不刷新时间戳）
	// KEYS[1] = device_limit:account:{accountID}
	// ARGV[1] = window（秒）
	// ARGV[2] = deviceID
	// 返回: 1 = 活跃, 0 = 未登记或已过期
	isDeviceActiveScript = redis.NewScript(`
		redis.replicate_commands()
		local key = KEYS[1]
		local window = tonumber(ARGV[1])
		local deviceID = ARGV[2]

		local score = redis.call('ZSCORE', key, deviceID)
		if score == false then
			return 0
		end

		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		if tonumber(score) <= now - window then
			return 0
		end
		return 1
	`)
)

type deviceLimitCache struct {
	rdb           *redis.Client
	defaultWindow time.Duration
}

// NewDeviceLimitCache 创建设备限制缓存
// defaultWindowMinutes: 账号未配置窗口时用于计数的默认窗口（分钟）
func NewDeviceLimitCache(rdb *redis.Client, defaultWindowMinutes int) service.DeviceLimitCache {
	if defaultWindowMinutes <= 0 {
		defaultWindowMinutes = service.DefaultDeviceWindowMinutes
	}

	// 预加载 Lua 脚本，避免 Pipeline 中出现 NOSCRIPT 错误
	ctx := context.Background()
	for _, script := range []*redis.Script{registerDeviceScript, getActiveDeviceCountScript, isDeviceActiveScript} {
		if err := script.Load(ctx, rdb).Err(); err != nil {
			log.Printf("[DeviceLimitCache] Failed to preload Lua script: %v", err)
		}
	}

	return &deviceLimitCache{
		rdb:           rdb,
		defaultWindow: time.Duration(defaultWindowMinutes) * time.Minute,
	}
}

func deviceLimitKey(accountID int64) string {
	return fmt.Sprintf("%s%d", deviceLimitKeyPrefix, accountID)
}

func (c *deviceLimitCache) windowSeconds(window time.Duration) int {
	seconds := int(window.Seconds())
	if seconds <= 0 {
		seconds = int(c.defaultWindow.Seconds())
	}
	return seconds
}

// RegisterDevice 登记设备活动，返回 (allowed, isNew, err)
func (c *deviceLimitCache) RegisterDevice(ctx context.Context, accountID int64, deviceID string, maxDevices int, window time.Duration) (bool, bool, error) {
	if deviceID == "" || maxDevices <= 0 {
		return true, false, nil // 无效参数，默认允许且不计为新登记
	}

	result, err := registerDeviceScript.Run(ctx, c.rdb, []string{deviceLimitKey(accountID)}, maxDevices, c.windowSeconds(window), deviceID).Int()
	if err != nil {
		return true, false, err // 失败开放：由调用方记录日志
	}
	switch result {
	case 1:
		return true, false, nil
	case 2:
		return true, true, nil
	default:
		return false, false, nil
	}
}

// UnregisterDevice 立即移除设备登记
func (c *deviceLimitCache) UnregisterDevice(ctx context.Context, accountID int64, deviceID string) error {
	if deviceID == "" {
		return nil
	}
	return c.rdb.ZRem(ctx, deviceLimitKey(accountID), deviceID).Err()
}

// GetActiveDeviceCountBatch 批量获取活跃设备数
func (c *deviceLimitCache) GetActiveDeviceCountBatch(ctx context.Context, accountIDs []int64, windows map[int64]time.Duration) (map[int64]int, error) {
	results := make(map[int64]int, len(accountIDs))
	if len(accountIDs) == 0 {
		return results, nil
	}

	pipe := c.rdb.Pipeline()
	cmds := make(map[int64]*redis.Cmd, len(accountIDs))
	for _, accountID := range accountIDs {
		window := c.defaultWindow
		if windows != nil {
			if w, ok := windows[accountID]; ok && w > 0 {
				window = w
			}
		}
		cmds[accountID] = getActiveDeviceCountScript.Run(ctx, pipe, []string{deviceLimitKey(accountID)}, c.windowSeconds(window))
	}

	// 执行 pipeline，即使部分失败也尽量返回成功的结果
	_, _ = pipe.Exec(ctx)

	for accountID, cmd := range cmds {
		if count, err := cmd.Int(); err == nil {
			results[accountID] = count
		}
	}
	return results, nil
}

// ClearDevices 清空账号的全部设备登记
func (c *deviceLimitCache) ClearDevices(ctx context.Context, accountID int64) error {
	return c.rdb.Del(ctx, deviceLimitKey(accountID)).Err()
}

// ActiveDeviceAccounts 返回已登记 deviceID 且未过期的账号集合（只读）
func (c *deviceLimitCache) ActiveDeviceAccounts(ctx context.Context, deviceID string, accountIDs []int64, windows map[int64]time.Duration) (map[int64]struct{}, error) {
	result := make(map[int64]struct{})
	if deviceID == "" || len(accountIDs) == 0 {
		return result, nil
	}

	pipe := c.rdb.Pipeline()
	cmds := make(map[int64]*redis.Cmd, len(accountIDs))
	for _, accountID := range accountIDs {
		window := c.defaultWindow
		if windows != nil {
			if w, ok := windows[accountID]; ok && w > 0 {
				window = w
			}
		}
		cmds[accountID] = isDeviceActiveScript.Run(ctx, pipe, []string{deviceLimitKey(accountID)}, c.windowSeconds(window), deviceID)
	}
	_, _ = pipe.Exec(ctx)

	for accountID, cmd := range cmds {
		if active, err := cmd.Int(); err == nil && active == 1 {
			result[accountID] = struct{}{}
		}
	}
	return result, nil
}
