package service

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultDeviceWindowMinutes 设备空闲释放窗口的默认值（分钟）。
// 账号 extra 里未设置 device_window_minutes 时使用。
const DefaultDeviceWindowMinutes = 360

// DeviceLimitCache 管理账号级别的活跃设备跟踪，用于 Anthropic OAuth/SetupToken
// 账号的设备数量限制（extra: max_devices / device_window_minutes）。
//
// Key 格式: device_limit:account:{accountID}
// 数据结构: Sorted Set (member=deviceID, score=最后活动时间戳)
//
// 设备在空闲超过窗口后自动过期，与会话限制同构。
type DeviceLimitCache interface {
	// RegisterDevice 登记设备活动
	// - 设备已存在：刷新时间戳，返回 allowed=true, isNew=false
	// - 设备不存在且活跃设备数 < maxDevices：添加，返回 allowed=true, isNew=true
	// - 设备不存在且活跃设备数 >= maxDevices：返回 allowed=false
	RegisterDevice(ctx context.Context, accountID int64, deviceID string, maxDevices int, window time.Duration) (allowed bool, isNew bool, err error)

	// UnregisterDevice 立即移除设备登记（不等待窗口过期）。
	// 只应用于"本次请求新登记、且最终未由该账号服务"的设备，否则会把正在使用的设备踢掉。
	UnregisterDevice(ctx context.Context, accountID int64, deviceID string) error

	// GetActiveDeviceCountBatch 批量获取多个账号的活跃设备数。
	// windows: 每个账号的窗口配置；缺失时使用默认窗口。查询失败的账号不在结果里。
	GetActiveDeviceCountBatch(ctx context.Context, accountIDs []int64, windows map[int64]time.Duration) (map[int64]int, error)

	// ClearDevices 清空账号的全部设备登记（运营手动放行换机）。
	ClearDevices(ctx context.Context, accountID int64) error

	// ActiveDeviceAccounts 返回 accountIDs 中已登记 deviceID 且未过期的账号集合（设备亲和用）。
	// 只读，不刷新时间戳；查询失败的账号视为未登记。
	ActiveDeviceAccounts(ctx context.Context, deviceID string, accountIDs []int64, windows map[int64]time.Duration) (map[int64]struct{}, error)
}

// deviceLimitIDRegex 合法设备 ID：Claude Code 的 userID，恰好 64 位十六进制。
var deviceLimitIDRegex = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// ExtractDeviceLimitID 从 metadata.user_id 提取用于设备限制的设备 ID。
// 两种格式（legacy 拼接串 / JSON）都支持；不合法或缺失返回空串，调用方视为"不计数"。
func ExtractDeviceLimitID(metadataUserID string) string {
	parsed := ParseMetadataUserID(metadataUserID)
	if parsed == nil {
		return ""
	}
	id := strings.TrimSpace(parsed.DeviceID)
	if !deviceLimitIDRegex.MatchString(id) {
		return ""
	}
	return strings.ToLower(id)
}

// DeviceLimitRequest 是一次网关请求的设备限制上下文：
// 携带设备 ID，并记录本次请求在哪些账号上"新登记"了设备，
// 以便请求失败或换号后只释放这些新登记，不动此前已登记的设备。
type DeviceLimitRequest struct {
	deviceID string

	mu              sync.Mutex
	newlyRegistered map[int64]struct{}
}

// NewDeviceLimitRequest 从 metadata.user_id 构造请求级设备上下文。
// 设备 ID 不合法时 DeviceID() 为空，之后的设备检查一律放行。
func NewDeviceLimitRequest(metadataUserID string) *DeviceLimitRequest {
	return &DeviceLimitRequest{
		deviceID:        ExtractDeviceLimitID(metadataUserID),
		newlyRegistered: make(map[int64]struct{}),
	}
}

// DeviceID 返回归一化后的设备 ID；不合法时为空串。
func (r *DeviceLimitRequest) DeviceID() string {
	if r == nil {
		return ""
	}
	return r.deviceID
}

// NewlyRegisteredAccountIDs 返回本次请求新登记过设备的账号（测试与日志用）。
func (r *DeviceLimitRequest) NewlyRegisteredAccountIDs() []int64 {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]int64, 0, len(r.newlyRegistered))
	for id := range r.newlyRegistered {
		ids = append(ids, id)
	}
	return ids
}

func (r *DeviceLimitRequest) markNew(accountID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.newlyRegistered[accountID] = struct{}{}
}

// takeNew 移除并报告该账号是否由本次请求新登记。
func (r *DeviceLimitRequest) takeNew(accountID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.newlyRegistered[accountID]; !ok {
		return false
	}
	delete(r.newlyRegistered, accountID)
	return true
}

// takeAllExcept 移除并返回除 keep 之外所有新登记的账号。
func (r *DeviceLimitRequest) takeAllExcept(keep int64) []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]int64, 0, len(r.newlyRegistered))
	for id := range r.newlyRegistered {
		if id == keep {
			continue
		}
		ids = append(ids, id)
		delete(r.newlyRegistered, id)
	}
	return ids
}

type deviceLimitRequestCtxKey struct{}

// WithDeviceLimitRequest 把请求级设备上下文挂到 ctx；调度栈通过 ctx 取用，签名不变。
func WithDeviceLimitRequest(ctx context.Context, req *DeviceLimitRequest) context.Context {
	if req == nil {
		return ctx
	}
	return context.WithValue(ctx, deviceLimitRequestCtxKey{}, req)
}

func deviceLimitRequestFromContext(ctx context.Context) *DeviceLimitRequest {
	if ctx == nil {
		return nil
	}
	req, _ := ctx.Value(deviceLimitRequestCtxKey{}).(*DeviceLimitRequest)
	return req
}

// isDeviceLimitApplicable 报告账号是否启用了设备限制（仅 Anthropic OAuth/SetupToken）。
func isDeviceLimitApplicable(account *Account) bool {
	return account != nil && account.IsAnthropicOAuthOrSetupToken() && account.GetMaxDevices() > 0
}

// checkAndRegisterDevice 检查并登记设备，返回 false 表示该账号对本请求不可选（设备已满且是新设备）。
// 不适用账号、未启用、无请求上下文、无合法设备 ID、缓存不可用、缓存报错：一律放行。
func (s *GatewayService) checkAndRegisterDevice(ctx context.Context, account *Account) bool {
	if s == nil || !isDeviceLimitApplicable(account) {
		return true
	}
	req := deviceLimitRequestFromContext(ctx)
	if req == nil || req.deviceID == "" {
		return true
	}
	if s.deviceLimitCache == nil {
		return true
	}

	maxDevices := account.GetMaxDevices()
	windowMinutes := account.GetDeviceWindowMinutes()
	window := time.Duration(windowMinutes) * time.Minute

	allowed, isNew, err := s.deviceLimitCache.RegisterDevice(ctx, account.ID, req.deviceID, maxDevices, window)
	if err != nil {
		// 失败开放：缓存错误时允许通过
		slog.Warn("device_limit.register_failed",
			"account_id", account.ID,
			"device", shortDeviceID(req.deviceID),
			"error", err)
		return true
	}
	if !allowed {
		slog.Info("device_limit.rejected",
			"account_id", account.ID,
			"device", shortDeviceID(req.deviceID),
			"max_devices", maxDevices,
			"window_minutes", windowMinutes)
		return false
	}
	if isNew {
		req.markNew(account.ID)
	}
	return true
}

// rollbackNewDeviceRegistration 在设备登记成功、但同账号后续检查（会话限制）失败时，
// 撤销本次请求对该账号的新登记；此前已登记的设备不动。
func (s *GatewayService) rollbackNewDeviceRegistration(ctx context.Context, account *Account) {
	if s == nil || account == nil || s.deviceLimitCache == nil {
		return
	}
	req := deviceLimitRequestFromContext(ctx)
	if req == nil || !req.takeNew(account.ID) {
		return
	}
	s.unregisterDevice(ctx, account.ID, req.deviceID)
}

// ReleaseAccountDeviceRegistration 释放本次请求在某个账号上的新登记（failover 换号时立即调用）。
// 非新登记、不适用、缓存不可用均为 no-op，幂等。
func (s *GatewayService) ReleaseAccountDeviceRegistration(ctx context.Context, req *DeviceLimitRequest, accountID int64) {
	if s == nil || req == nil || s.deviceLimitCache == nil {
		return
	}
	if !req.takeNew(accountID) {
		return
	}
	s.unregisterDevice(ctx, accountID, req.deviceID)
}

// ReleaseUnservedDeviceRegistrations 释放本次请求除 servedAccountID 之外的所有新登记。
// 请求结束时无条件调用：成功时清掉换号链上未服务的账号，失败时（servedAccountID=0）清掉全部。幂等。
func (s *GatewayService) ReleaseUnservedDeviceRegistrations(ctx context.Context, req *DeviceLimitRequest, servedAccountID int64) {
	if s == nil || req == nil || s.deviceLimitCache == nil {
		return
	}
	for _, accountID := range req.takeAllExcept(servedAccountID) {
		s.unregisterDevice(ctx, accountID, req.deviceID)
	}
}

// ====== 设备亲和：一台设备尽量固定在同一个账号上 ======

// deviceAffinitySet 返回候选账号中"本请求设备已登记且未过期"的账号集合。
// 无设备上下文、无合法设备 ID、缓存不可用、无启用设备限制的候选：返回 nil（不启用亲和）。
func (s *GatewayService) deviceAffinitySet(ctx context.Context, accounts []Account) map[int64]struct{} {
	if s == nil || s.deviceLimitCache == nil {
		return nil
	}
	req := deviceLimitRequestFromContext(ctx)
	if req == nil || req.deviceID == "" {
		return nil
	}
	ids := make([]int64, 0, len(accounts))
	windows := make(map[int64]time.Duration, len(accounts))
	for i := range accounts {
		acc := &accounts[i]
		if !isDeviceLimitApplicable(acc) {
			continue
		}
		ids = append(ids, acc.ID)
		windows[acc.ID] = time.Duration(acc.GetDeviceWindowMinutes()) * time.Minute
	}
	if len(ids) == 0 {
		return nil
	}
	set, err := s.deviceLimitCache.ActiveDeviceAccounts(ctx, req.deviceID, ids, windows)
	if err != nil {
		slog.Warn("device_affinity.lookup_failed", "device", shortDeviceID(req.deviceID), "error", err)
		return nil
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// partitionByDeviceAffinity 稳定地把亲和账号移到切片前部，其余相对顺序不变。
func partitionByDeviceAffinity(accounts []*Account, affinity map[int64]struct{}) []*Account {
	if len(affinity) == 0 || len(accounts) == 0 {
		return accounts
	}
	front := make([]*Account, 0, len(accounts))
	rest := make([]*Account, 0, len(accounts))
	for _, acc := range accounts {
		if _, ok := affinity[acc.ID]; ok {
			front = append(front, acc)
		} else {
			rest = append(rest, acc)
		}
	}
	return append(front, rest...)
}

// partitionLoadsByDeviceAffinity 同 partitionByDeviceAffinity，作用于带负载信息的候选。
func partitionLoadsByDeviceAffinity(items []accountWithLoad, affinity map[int64]struct{}) []accountWithLoad {
	if len(affinity) == 0 || len(items) == 0 {
		return items
	}
	front := make([]accountWithLoad, 0, len(items))
	rest := make([]accountWithLoad, 0, len(items))
	for _, item := range items {
		if _, ok := affinity[item.account.ID]; ok {
			front = append(front, item)
		} else {
			rest = append(rest, item)
		}
	}
	return append(front, rest...)
}

// tryDeviceAffinity 在负载感知选号之前，优先把请求落到本设备已登记的账号上：
//  1. 亲和账号有空槽 → 直接占槽返回；
//  2. 亲和账号槽满但排队未超过粘性会话的上限 → 返回该账号的等待计划（宁可等也不换号）；
//  3. 都不行 → handled=false，交给常规负载感知选号。
func (s *GatewayService) tryDeviceAffinity(ctx context.Context, candidates []*Account, affinity map[int64]struct{}, groupID *int64, sessionHash string, preferOAuth bool, waitTimeout time.Duration, maxWaiting int) (*AccountSelectionResult, bool, error) {
	if len(affinity) == 0 {
		return nil, false, nil
	}
	affine := make([]*Account, 0, len(affinity))
	for _, acc := range candidates {
		if _, ok := affinity[acc.ID]; ok {
			affine = append(affine, acc)
		}
	}
	if len(affine) == 0 {
		return nil, false, nil
	}

	result, ok, err := s.tryAcquireByLegacyOrder(ctx, affine, groupID, sessionHash, preferOAuth)
	if err != nil {
		return nil, false, err
	}
	if ok {
		slog.Debug("device_affinity.hit", "account_id", result.Account.ID, "result", "slot_acquired")
		return result, true, nil
	}

	if s.concurrencyService == nil || maxWaiting <= 0 {
		return nil, false, nil
	}
	for _, acc := range affine {
		waitingCount, _ := s.concurrencyService.GetAccountWaitingCount(ctx, acc.ID)
		if waitingCount >= maxWaiting {
			continue
		}
		if !s.checkAndRegisterSession(ctx, acc, sessionHash) {
			continue
		}
		slog.Debug("device_affinity.hit", "account_id", acc.ID, "result", "wait_plan")
		selection, err := s.newSelectionResult(ctx, acc, false, nil, &AccountWaitPlan{
			AccountID:      acc.ID,
			MaxConcurrency: acc.Concurrency,
			Timeout:        waitTimeout,
			MaxWaiting:     maxWaiting,
		})
		if err != nil {
			return nil, false, err
		}
		return selection, true, nil
	}
	return nil, false, nil
}

func (s *GatewayService) unregisterDevice(ctx context.Context, accountID int64, deviceID string) {
	if err := s.deviceLimitCache.UnregisterDevice(ctx, accountID, deviceID); err != nil {
		slog.Debug("device_limit.release_failed",
			"account_id", accountID,
			"device", shortDeviceID(deviceID),
			"error", err)
	}
}

// shortDeviceID 日志用短设备 ID，避免整段 64 位 hex 刷屏。
func shortDeviceID(deviceID string) string {
	if len(deviceID) <= 12 {
		return deviceID
	}
	return deviceID[:12]
}
