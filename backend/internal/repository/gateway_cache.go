package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const stickySessionPrefix = "sticky_session:"
const liveCallPrefix = "live:call:"

type gatewayCache struct {
	rdb *redis.Client
}

func NewGatewayCache(rdb *redis.Client) service.GatewayCache {
	return &gatewayCache{rdb: rdb}
}

// buildSessionKey 构建 session key，包含 groupID 实现分组隔离
// 格式: sticky_session:{groupID}:{sessionHash}
func buildSessionKey(groupID int64, sessionHash string) string {
	return fmt.Sprintf("%s%d:%s", stickySessionPrefix, groupID, sessionHash)
}

func (c *gatewayCache) GetSessionAccountID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
	key := buildSessionKey(groupID, sessionHash)
	accountID, err := c.rdb.Get(ctx, key).Int64()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, service.ErrStickySessionNotFound
		}
		return 0, err
	}
	return accountID, nil
}

func (c *gatewayCache) SetSessionAccountID(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) error {
	key := buildSessionKey(groupID, sessionHash)
	if err := c.rdb.Set(ctx, key, accountID, ttl).Err(); err != nil {
		return err
	}
	// 同步记录签名归属。写在这里而不是各个调用点，是因为"会话现在由哪个账号服务"
	// 与"历史 thinking 签名由谁签发"在写入时刻是同一件事，分开写迟早会有调用点漏掉。
	// 失败不影响绑定本身：归属记录只是让剥离提前发生，缺了仍有 400 重试兜底。
	_ = c.setSignatureOwnerAccountID(ctx, groupID, sessionHash, accountID)
	return nil
}

func (c *gatewayCache) RefreshSessionTTL(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) error {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Expire(ctx, key, ttl).Err()
}

// DeleteSessionAccountID 删除粘性会话与账号的绑定关系。
// 当检测到绑定的账号不可用（如状态错误、禁用、不可调度等）时调用，
// 以便下次请求能够重新选择可用账号。
//
// DeleteSessionAccountID removes the sticky session binding for the given session.
// Called when the bound account becomes unavailable (e.g., error status, disabled,
// or unschedulable), allowing subsequent requests to select a new available account.
//
// 只删路由绑定，**不删** sig_owner。这个区别是有意的：本方法被调用的时刻，正是下一轮
// 必然换号、因而历史 thinking 签名必然失效的时刻。若把签名归属一并删掉，下一轮就只能
// 盲发一个已知会被上游拒绝的请求（生产实测每天 175 次，其中 16 次连重试都没救回来）。
func (c *gatewayCache) DeleteSessionAccountID(ctx context.Context, groupID int64, sessionHash string) error {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Del(ctx, key).Err()
}

// signatureOwnerPrefix 记录"某会话的历史 thinking 签名由哪个账号签发"。
// 格式: sig_owner:{groupID}:{sessionHash}
//
// 它与 sticky_session 是**故意分开**的两条记录，因为生命周期不同：
//
//	sticky_session —— 路由事实。账号 429、被驱逐、不可调度时必须立刻删除，
//	                  否则会把请求继续送去一个用不了的账号。
//	sig_owner      —— 历史事实。客户端手里那份 thinking 签名不会因为账号被驱逐
//	                  就变得有效，删掉它只会让我们在下一轮盲发一个必然被拒的请求。
//
// 所以 DeleteSessionAccountID 不碰这个 key（见该方法的说明）。
const signatureOwnerPrefix = "sig_owner:"

// signatureOwnerTTL 取 24h：thinking 签名本身不会过期，但隔夜之后再接着聊的会话
// 极少，而 key 需要有界。超出这个窗口仍有 400 重试路径兜底。
const signatureOwnerTTL = 24 * time.Hour

func buildSignatureOwnerKey(groupID int64, sessionHash string) string {
	return fmt.Sprintf("%s%d:%s", signatureOwnerPrefix, groupID, sessionHash)
}

func (c *gatewayCache) setSignatureOwnerAccountID(ctx context.Context, groupID int64, sessionHash string, accountID int64) error {
	key := buildSignatureOwnerKey(groupID, sessionHash)
	return c.rdb.Set(ctx, key, accountID, signatureOwnerTTL).Err()
}

// GetSignatureOwnerAccountID 返回该会话历史 thinking 签名的签发账号；无记录时返回 0。
func (c *gatewayCache) GetSignatureOwnerAccountID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
	key := buildSignatureOwnerKey(groupID, sessionHash)
	return c.rdb.Get(ctx, key).Int64()
}

// signatureTaintedPrefix 标记「这个会话的历史里混有别的账号签发的 thinking 签名」。
// 格式: sig_tainted:{groupID}:{sessionHash}
//
// 为什么 sig_owner 不够：上游校验历史里的**每一个** thinking 块，不只是最近一轮
// （生产实测错误落在 content.16 / content.58 / content.101 这类深处下标）。
// sig_owner 只记最近一次绑定，而一个换过账号的长对话，历史里可能混着好几个账号
// 的签名——即使之后一直待在同一个账号上，每一轮仍会被拒。
//
// 这一位一旦置位就保持到 TTL 结束：污染是不可逆的，客户端不会把历史里的旧签名换掉。
const signatureTaintedPrefix = "sig_tainted:"

func buildSignatureTaintedKey(groupID int64, sessionHash string) string {
	return fmt.Sprintf("%s%d:%s", signatureTaintedPrefix, groupID, sessionHash)
}

// MarkSignatureTainted 置位并续期。重复调用是幂等的。
func (c *gatewayCache) MarkSignatureTainted(ctx context.Context, groupID int64, sessionHash string) error {
	return c.rdb.Set(ctx, buildSignatureTaintedKey(groupID, sessionHash), 1, signatureOwnerTTL).Err()
}

// IsSignatureTainted 无记录时返回 false，不返回错误——拿不到这一位时按未污染处理，
// 退回既有的「发出 → 400 → 剥离重试」路径，不影响正确性。
func (c *gatewayCache) IsSignatureTainted(ctx context.Context, groupID int64, sessionHash string) bool {
	n, err := c.rdb.Exists(ctx, buildSignatureTaintedKey(groupID, sessionHash)).Result()
	return err == nil && n > 0
}

// prevRequestIDPrefix 是 cc_prev_req parent-link 的 key 前缀。
// 格式: cc_prev_req:{accountID}:{sessionID}
//
// key 必须含 accountID：parent-link 只在产生该 request id 的账号上有效，
// 换号后必须 miss（见 service.lookupPrevRequestID）。
const prevRequestIDPrefix = "cc_prev_req:"

func buildPrevRequestIDKey(accountID int64, sessionID string) string {
	return fmt.Sprintf("%s%d:%s", prevRequestIDPrefix, accountID, sessionID)
}

func (c *gatewayCache) GetPrevRequestID(ctx context.Context, accountID int64, sessionID string) (string, error) {
	return c.rdb.Get(ctx, buildPrevRequestIDKey(accountID, sessionID)).Result()
}

func (c *gatewayCache) SetPrevRequestID(ctx context.Context, accountID int64, sessionID, requestID string, ttl time.Duration) error {
	return c.rdb.Set(ctx, buildPrevRequestIDKey(accountID, sessionID), requestID, ttl).Err()
}

// modelNotFoundPrefix 记录"某平台上某模型不存在"。
//
// 格式: model_not_found:{platform}:{model}
//
// key 刻意**不含** accountID：模型下架是全局事实，记成账号维度就失去了意义——
// 原有的 (账号, 模型) 冷却正是因此让同一次请求依次喷遍整个账号池。
const modelNotFoundPrefix = "model_not_found:"

func buildModelNotFoundKey(platform, model string) string {
	return fmt.Sprintf("%s%s:%s", modelNotFoundPrefix, platform, model)
}

func (c *gatewayCache) IsModelNotFound(ctx context.Context, platform, model string) (bool, error) {
	n, err := c.rdb.Exists(ctx, buildModelNotFoundKey(platform, model)).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (c *gatewayCache) MarkModelNotFound(ctx context.Context, platform, model string, ttl time.Duration) error {
	return c.rdb.Set(ctx, buildModelNotFoundKey(platform, model), "1", ttl).Err()
}

// Compile-time assertion: gatewayCache must implement CyberSessionBlockStore.
var _ service.CyberSessionBlockStore = (*gatewayCache)(nil)

// Compile-time assertion: gatewayCache must implement ModelNotFoundStore.
var _ service.ModelNotFoundStore = (*gatewayCache)(nil)

// Compile-time assertion: gatewayCache must implement PrevRequestIDStore.
// 该断言是 cc_prev_req 能力生效的前提——service 侧走运行时类型断言，
// 断了只会静默降级，不会编译失败。
var _ service.PrevRequestIDStore = (*gatewayCache)(nil)
var _ service.LiveCallStore = (*gatewayCache)(nil)

const cyberSessionBlockPrefix = "cyber_session_block:"

// SetCyberSessionBlocked 把被 cyber_policy 命中的会话写入屏蔽表（TTL 自动过期）。
// 存储值 "1" 作为存在标记（IsCyberSessionBlocked 只检查 key 是否存在，不读值）。
func (c *gatewayCache) SetCyberSessionBlocked(ctx context.Context, key string, ttl time.Duration) error {
	return c.rdb.Set(ctx, cyberSessionBlockPrefix+key, "1", ttl).Err()
}

// IsCyberSessionBlocked 查询会话是否在屏蔽表中。
func (c *gatewayCache) IsCyberSessionBlocked(ctx context.Context, key string) (bool, error) {
	n, err := c.rdb.Exists(ctx, cyberSessionBlockPrefix+key).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

var claimLiveControllerScript = redis.NewScript(`
	local key = KEYS[1]
	local target = ARGV[1]
	local owner = ARGV[2]
	local current = redis.call('HGET', key, 'controller')
	if current == false or current == 'closed' then
		return 0
	end
	if target == 'observer' and current ~= 'pending' then
		return 0
	end
	if target == 'proxy' and current ~= 'pending' and current ~= 'observer' and
		(current ~= 'proxy' or redis.call('HGET', key, 'controller_owner') ~= owner) then
		return 0
	end
	redis.call('HSET', key, 'controller', target, 'controller_owner', owner)
	return 1
`)

var markLiveCallClosedScript = redis.NewScript(`
	local key = KEYS[1]
	if redis.call('EXISTS', key) == 0 then
		return 0
	end
	if redis.call('HGET', key, 'controller') == 'closed' then
		return 0
	end
	redis.call('HSET', key, 'controller', 'closed', 'controller_owner', '')
	redis.call('EXPIRE', key, ARGV[1])
	return 1
`)

var releaseLiveControllerScript = redis.NewScript(`
	local key = KEYS[1]
	if redis.call('HGET', key, 'controller') ~= 'proxy' or
		redis.call('HGET', key, 'controller_owner') ~= ARGV[1] then
		return 0
	end
	redis.call('HSET', key, 'controller', 'pending', 'controller_owner', '')
	return 1
`)

func liveCallKey(callHash string) string {
	return liveCallPrefix + callHash
}

func HashLiveCallID(callID string) string {
	sum := sha256.Sum256([]byte(callID))
	return hex.EncodeToString(sum[:])
}

func (c *gatewayCache) SaveLiveCall(ctx context.Context, record *service.LiveCallRecord, ttl time.Duration) error {
	if record == nil || record.CallHash == "" || record.CallID == "" {
		return fmt.Errorf("invalid live call record")
	}
	values := map[string]any{
		"call_id":          record.CallID,
		"account_id":       record.AccountID,
		"api_key_id":       record.APIKeyID,
		"user_id":          record.UserID,
		"group_id":         record.GroupID,
		"subscription_id":  record.SubscriptionID,
		"lease_id":         record.LeaseID,
		"model":            record.Model,
		"created_at":       record.CreatedAt.UnixMilli(),
		"expires_at":       record.ExpiresAt.UnixMilli(),
		"controller":       record.Controller,
		"controller_owner": record.ControllerOwner,
		"user_agent":       record.UserAgent,
		"ip_address":       record.IPAddress,
		"inbound_endpoint": record.InboundEndpoint,
		"attestation":      record.AttestationCiphertext,
	}
	key := liveCallKey(record.CallHash)
	pipe := c.rdb.TxPipeline()
	pipe.HSet(ctx, key, values)
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

func (c *gatewayCache) GetLiveCall(ctx context.Context, callHash string) (*service.LiveCallRecord, error) {
	values, err := c.rdb.HGetAll(ctx, liveCallKey(callHash)).Result()
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, service.ErrLiveCallNotFound
	}
	parseInt := func(field string) int64 {
		value, _ := strconv.ParseInt(values[field], 10, 64)
		return value
	}
	createdAt := time.UnixMilli(parseInt("created_at"))
	expiresAt := time.UnixMilli(parseInt("expires_at"))
	return &service.LiveCallRecord{
		CallID:                values["call_id"],
		CallHash:              callHash,
		AccountID:             parseInt("account_id"),
		APIKeyID:              parseInt("api_key_id"),
		UserID:                parseInt("user_id"),
		GroupID:               parseInt("group_id"),
		SubscriptionID:        parseInt("subscription_id"),
		LeaseID:               values["lease_id"],
		Model:                 values["model"],
		CreatedAt:             createdAt,
		ExpiresAt:             expiresAt,
		Controller:            values["controller"],
		ControllerOwner:       values["controller_owner"],
		UserAgent:             values["user_agent"],
		IPAddress:             values["ip_address"],
		InboundEndpoint:       values["inbound_endpoint"],
		AttestationCiphertext: values["attestation"],
	}, nil
}

func (c *gatewayCache) ClaimLiveController(ctx context.Context, callHash, controller, owner string) (bool, error) {
	result, err := claimLiveControllerScript.Run(ctx, c.rdb, []string{liveCallKey(callHash)}, controller, owner).Int()
	return result == 1, err
}

func (c *gatewayCache) GetLiveController(ctx context.Context, callHash string) (string, error) {
	value, err := c.rdb.HGet(ctx, liveCallKey(callHash), "controller").Result()
	if err == redis.Nil {
		return "", service.ErrLiveCallNotFound
	}
	return value, err
}

func (c *gatewayCache) ReleaseLiveController(ctx context.Context, callHash, owner string) (bool, error) {
	result, err := releaseLiveControllerScript.Run(ctx, c.rdb, []string{liveCallKey(callHash)}, owner).Int()
	return result == 1, err
}

func (c *gatewayCache) MarkLiveCallClosed(ctx context.Context, callHash string, ttl time.Duration) (bool, error) {
	result, err := markLiveCallClosedScript.Run(ctx, c.rdb, []string{liveCallKey(callHash)}, int64(ttl.Seconds())).Int()
	return result == 1, err
}
