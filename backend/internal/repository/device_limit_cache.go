package repository

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	// deviceLimitKeyPrefix 并发名额集合（score = 最后活动时间，空闲超过窗口即释放）
	deviceLimitKeyPrefix = "device_limit:account:"
	// deviceDailyKeyPrefix 24 小时额度集合（score = 首次进入时间，不刷新）
	deviceDailyKeyPrefix = "device_daily:account:"
	// keyExpireGraceSeconds 键 TTL 在窗口之外的余量，保证最后一个成员过期后键自行消失
	keyExpireGraceSeconds = 60
)

// 登记结果编码（Lua 只能方便地返回一个整数）
const (
	regRejectConcurrent = 0
	regRejectDaily      = 1
	regExisting         = 2 // 已占名额，未新计 24h
	regExistingNewDaily = 3 // 已占名额，本次补入 24h（设置变更清零后）
	regNew              = 4 // 新占名额，24h 内已接纳过
	regNewNewDaily      = 5 // 新占名额，新计 24h
)

var (
	// registerDeviceScript 原子登记（两层）
	// KEYS[1] = device_limit:account:{id}   KEYS[2] = device_daily:account:{id}
	// ARGV[1] = maxDevices（0=不限）  ARGV[2] = window 秒  ARGV[3] = deviceID
	// ARGV[4] = maxDaily（0=不限）    ARGV[5] = dailyWindow 秒
	registerDeviceScript = redis.NewScript(`
		redis.replicate_commands()
		local slotKey, dailyKey = KEYS[1], KEYS[2]
		local maxDevices = tonumber(ARGV[1])
		local window = tonumber(ARGV[2])
		local deviceID = ARGV[3]
		local maxDaily = tonumber(ARGV[4])
		local dailyWindow = tonumber(ARGV[5])
		local grace = ` + fmt.Sprint(keyExpireGraceSeconds) + `

		local now = tonumber(redis.call('TIME')[1])
		redis.call('ZREMRANGEBYSCORE', slotKey, '-inf', now - window)
		redis.call('ZREMRANGEBYSCORE', dailyKey, '-inf', now - dailyWindow)

		local inDaily = redis.call('ZSCORE', dailyKey, deviceID) ~= false
		local newDaily = 0
		local function countDaily()
			if not inDaily then
				redis.call('ZADD', dailyKey, now, deviceID)
				redis.call('EXPIRE', dailyKey, dailyWindow + grace)
				newDaily = 1
			end
		end

		if redis.call('ZSCORE', slotKey, deviceID) ~= false then
			-- 正占名额的设备永远放行；设置变更清零后补入 24h 集合，按新值重新计数
			redis.call('ZADD', slotKey, now, deviceID)
			redis.call('EXPIRE', slotKey, window + grace)
			countDaily()
			return 2 + newDaily
		end

		if maxDevices > 0 and redis.call('ZCARD', slotKey) >= maxDevices then
			return 0
		end
		if not inDaily and maxDaily > 0 and redis.call('ZCARD', dailyKey) >= maxDaily then
			return 1
		end

		redis.call('ZADD', slotKey, now, deviceID)
		redis.call('EXPIRE', slotKey, window + grace)
		countDaily()
		return 4 + newDaily
	`)

	// deviceCountsScript 清理过期后返回 {并发数, 24h 数}
	// KEYS 同上；ARGV[1] = window 秒  ARGV[2] = dailyWindow 秒
	deviceCountsScript = redis.NewScript(`
		redis.replicate_commands()
		local now = tonumber(redis.call('TIME')[1])
		redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - tonumber(ARGV[1]))
		redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now - tonumber(ARGV[2]))
		return { redis.call('ZCARD', KEYS[1]), redis.call('ZCARD', KEYS[2]) }
	`)

	// isDeviceKnownScript 只读：设备正占名额，或 24h 内接纳过（不刷新时间戳）
	// KEYS 同上；ARGV[1] = window 秒  ARGV[2] = dailyWindow 秒  ARGV[3] = deviceID
	isDeviceKnownScript = redis.NewScript(`
		redis.replicate_commands()
		local now = tonumber(redis.call('TIME')[1])
		local s = redis.call('ZSCORE', KEYS[1], ARGV[3])
		if s ~= false and tonumber(s) > now - tonumber(ARGV[1]) then
			return 1
		end
		local d = redis.call('ZSCORE', KEYS[2], ARGV[3])
		if d ~= false and tonumber(d) > now - tonumber(ARGV[2]) then
			return 1
		end
		return 0
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
	for _, script := range []*redis.Script{registerDeviceScript, deviceCountsScript, isDeviceKnownScript} {
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

func deviceDailyKey(accountID int64) string {
	return fmt.Sprintf("%s%d", deviceDailyKeyPrefix, accountID)
}

func deviceKeys(accountID int64) []string {
	return []string{deviceLimitKey(accountID), deviceDailyKey(accountID)}
}

func (c *deviceLimitCache) windowSeconds(window time.Duration) int {
	seconds := int(window.Seconds())
	if seconds <= 0 {
		seconds = int(c.defaultWindow.Seconds())
	}
	return seconds
}

func dailyWindowSeconds(window time.Duration) int {
	seconds := int(window.Seconds())
	if seconds <= 0 {
		seconds = int(service.DeviceDailyWindow.Seconds())
	}
	return seconds
}

// RegisterDevice 登记设备活动
func (c *deviceLimitCache) RegisterDevice(ctx context.Context, accountID int64, deviceID string, limits service.DeviceLimits) (service.DeviceRegistration, error) {
	if deviceID == "" || (limits.MaxDevices <= 0 && limits.MaxDaily <= 0) {
		return service.DeviceRegistration{Allowed: true}, nil // 无效参数或两层都不限，默认允许且不计为新登记
	}
	maxDevices := limits.MaxDevices
	if maxDevices < 0 {
		maxDevices = 0
	}
	maxDaily := limits.MaxDaily
	if maxDaily < 0 {
		maxDaily = 0
	}

	code, err := registerDeviceScript.Run(ctx, c.rdb, deviceKeys(accountID),
		maxDevices, c.windowSeconds(limits.Window), deviceID, maxDaily, dailyWindowSeconds(limits.DailyWindow)).Int()
	if err != nil {
		return service.DeviceRegistration{Allowed: true}, err // 失败开放：由调用方记录日志
	}
	switch code {
	case regRejectConcurrent:
		return service.DeviceRegistration{Reason: "concurrent"}, nil
	case regRejectDaily:
		return service.DeviceRegistration{Reason: "daily"}, nil
	case regExisting:
		return service.DeviceRegistration{Allowed: true}, nil
	case regExistingNewDaily:
		return service.DeviceRegistration{Allowed: true, IsNewDaily: true}, nil
	case regNew:
		return service.DeviceRegistration{Allowed: true, IsNew: true}, nil
	case regNewNewDaily:
		return service.DeviceRegistration{Allowed: true, IsNew: true, IsNewDaily: true}, nil
	default:
		return service.DeviceRegistration{Allowed: true}, fmt.Errorf("device_limit: unexpected register result %d", code)
	}
}

// UnregisterDevice 立即移除并发名额登记；daily=true 时同时退回 24h 额度
func (c *deviceLimitCache) UnregisterDevice(ctx context.Context, accountID int64, deviceID string, daily bool) error {
	if deviceID == "" {
		return nil
	}
	pipe := c.rdb.Pipeline()
	pipe.ZRem(ctx, deviceLimitKey(accountID), deviceID)
	if daily {
		pipe.ZRem(ctx, deviceDailyKey(accountID), deviceID)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// GetDeviceCountsBatch 批量获取用量
func (c *deviceLimitCache) GetDeviceCountsBatch(ctx context.Context, accountIDs []int64, windows map[int64]time.Duration) (map[int64]service.DeviceCounts, error) {
	results := make(map[int64]service.DeviceCounts, len(accountIDs))
	if len(accountIDs) == 0 {
		return results, nil
	}

	pipe := c.rdb.Pipeline()
	cmds := make(map[int64]*redis.Cmd, len(accountIDs))
	daily := dailyWindowSeconds(0)
	for _, accountID := range accountIDs {
		cmds[accountID] = deviceCountsScript.Run(ctx, pipe, deviceKeys(accountID), c.windowSeconds(c.windowFor(windows, accountID)), daily)
	}

	// 执行 pipeline，即使部分失败也尽量返回成功的结果
	_, _ = pipe.Exec(ctx)

	for accountID, cmd := range cmds {
		vals, err := cmd.Int64Slice()
		if err != nil || len(vals) != 2 {
			continue
		}
		results[accountID] = service.DeviceCounts{Active: int(vals[0]), Daily: int(vals[1])}
	}
	return results, nil
}

// ClearDevices 清空账号两层全部登记
func (c *deviceLimitCache) ClearDevices(ctx context.Context, accountID int64) error {
	return c.rdb.Del(ctx, deviceKeys(accountID)...).Err()
}

// ClearDailyDevices 只清空 24h 额度记录
func (c *deviceLimitCache) ClearDailyDevices(ctx context.Context, accountID int64) error {
	return c.rdb.Del(ctx, deviceDailyKey(accountID)).Err()
}

// ActiveDeviceAccounts 返回该设备正占名额或 24h 内接纳过的账号集合（只读）
func (c *deviceLimitCache) ActiveDeviceAccounts(ctx context.Context, deviceID string, accountIDs []int64, windows map[int64]time.Duration) (map[int64]struct{}, error) {
	result := make(map[int64]struct{})
	if deviceID == "" || len(accountIDs) == 0 {
		return result, nil
	}

	pipe := c.rdb.Pipeline()
	cmds := make(map[int64]*redis.Cmd, len(accountIDs))
	daily := dailyWindowSeconds(0)
	for _, accountID := range accountIDs {
		cmds[accountID] = isDeviceKnownScript.Run(ctx, pipe, deviceKeys(accountID), c.windowSeconds(c.windowFor(windows, accountID)), daily, deviceID)
	}
	_, _ = pipe.Exec(ctx)

	for accountID, cmd := range cmds {
		if known, err := cmd.Int(); err == nil && known == 1 {
			result[accountID] = struct{}{}
		}
	}
	return result, nil
}

func (c *deviceLimitCache) windowFor(windows map[int64]time.Duration, accountID int64) time.Duration {
	if windows != nil {
		if w, ok := windows[accountID]; ok && w > 0 {
			return w
		}
	}
	return c.defaultWindow
}
