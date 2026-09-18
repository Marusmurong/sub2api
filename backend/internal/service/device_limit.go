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

// DeviceDailyWindow 「24 小时最大设备数」的统计窗口。每台设备从首次进入起占用 24 小时额度，
// 期间再来不刷新；到点自动掉出。
const DeviceDailyWindow = 24 * time.Hour

// DeviceLimits 是一个账号的设备限制配置（两层各自独立，0 = 该层不限）。
type DeviceLimits struct {
	MaxDevices  int           // 同一时刻最多占名额的设备数
	Window      time.Duration // 设备空闲多久后让出名额
	MaxDaily    int           // 滚动 24 小时内最多接纳的不同设备数
	DailyWindow time.Duration // 固定 DeviceDailyWindow，留作参数便于测试
}

// DeviceRegistration 是一次登记的结果。
type DeviceRegistration struct {
	Allowed    bool
	IsNew      bool   // 本次新占了并发名额（之前不在名额集合里）
	IsNewDaily bool   // 本次新计入了 24 小时额度
	Reason     string // 被拒原因："concurrent" / "daily"；放行时为空
}

// DeviceCounts 是账号当前的设备用量。
type DeviceCounts struct {
	Active int // 正占着名额的设备数
	Daily  int // 24 小时内接纳过的设备数
}

// DeviceLimitCache 管理账号级别的活跃设备跟踪，用于 Anthropic OAuth/SetupToken
// 账号的设备数量限制（extra: max_devices / device_window_minutes / max_devices_daily）。
//
// Key 格式:
//
//	device_limit:account:{accountID}  Sorted Set (member=deviceID, score=最后活动时间)  并发名额
//	device_daily:account:{accountID}  Sorted Set (member=deviceID, score=首次进入时间)  24 小时额度
type DeviceLimitCache interface {
	// RegisterDevice 原子登记设备：两层都放行才允许。
	// - 已占名额的设备：刷新时间戳，若不在 24h 集合则补入（设置变更清零后重新计数）
	// - 新设备：并发名额满 → 拒（concurrent）；不在 24h 集合且 24h 已满 → 拒（daily）
	RegisterDevice(ctx context.Context, accountID int64, deviceID string, limits DeviceLimits) (DeviceRegistration, error)

	// UnregisterDevice 立即移除并发名额登记；daily=true 时同时退回 24 小时额度。
	// 只应用于"本次请求新登记、且最终未由该账号服务"的设备。
	UnregisterDevice(ctx context.Context, accountID int64, deviceID string, daily bool) error

	// GetDeviceCountsBatch 批量获取用量。windows 缺失时使用默认窗口；查询失败的账号不在结果里。
	GetDeviceCountsBatch(ctx context.Context, accountIDs []int64, windows map[int64]time.Duration) (map[int64]DeviceCounts, error)

	// ClearDevices 清空账号两层全部登记（运营手动放行换机）。
	ClearDevices(ctx context.Context, accountID int64) error

	// ClearDailyDevices 只清空 24 小时额度记录（24 小时上限设置变更时重新计时）。
	ClearDailyDevices(ctx context.Context, accountID int64) error

	// ActiveDeviceAccounts 返回 accountIDs 中该设备"正占名额或 24h 内接纳过"的账号集合（设备亲和用）。
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

// deviceLimitsFor 读取账号的设备限制配置。
func deviceLimitsFor(account *Account) DeviceLimits {
	return DeviceLimits{
		MaxDevices:  account.GetMaxDevices(),
		Window:      time.Duration(account.GetDeviceWindowMinutes()) * time.Minute,
		MaxDaily:    account.GetMaxDevicesDaily(),
		DailyWindow: DeviceDailyWindow,
	}
}

// isDeviceLimitApplicable 报告账号是否启用了任一层设备限制（仅 Anthropic OAuth/SetupToken）。
func isDeviceLimitApplicable(account *Account) bool {
	return account != nil && account.IsAnthropicOAuthOrSetupToken() &&
		(account.GetMaxDevices() > 0 || account.GetMaxDevicesDaily() > 0)
}

// deviceRegKind 记录本次请求在某账号上新登记了哪几层。
type deviceRegKind struct {
	daily bool
}

// DeviceLimitRequest 是一次网关请求的设备限制上下文：
// 携带设备 ID，并记录本次请求在哪些账号上"新登记"了设备（以及是否新计入 24h 额度），
// 以便请求失败或换号后只释放这些新登记，不动此前已登记的设备。
type DeviceLimitRequest struct {
	deviceID string

	mu              sync.Mutex
	newlyRegistered map[int64]deviceRegKind
}

// NewDeviceLimitRequest 从 metadata.user_id 构造请求级设备上下文。
// 设备 ID 不合法时 DeviceID() 为空，之后的设备检查一律放行。
func NewDeviceLimitRequest(metadataUserID string) *DeviceLimitRequest {
	return &DeviceLimitRequest{
		deviceID:        ExtractDeviceLimitID(metadataUserID),
		newlyRegistered: make(map[int64]deviceRegKind),
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

func (r *DeviceLimitRequest) markNew(accountID int64, daily bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prev := r.newlyRegistered[accountID]
	r.newlyRegistered[accountID] = deviceRegKind{daily: prev.daily || daily}
}

// takeNew 移除并报告该账号是否由本次请求新登记，以及是否新计入了 24h 额度。
func (r *DeviceLimitRequest) takeNew(accountID int64) (found bool, daily bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kind, ok := r.newlyRegistered[accountID]
	if !ok {
		return false, false
	}
	delete(r.newlyRegistered, accountID)
	return true, kind.daily
}

// takeAllExcept 移除并返回除 keep 之外所有新登记的账号。
func (r *DeviceLimitRequest) takeAllExcept(keep int64) map[int64]deviceRegKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[int64]deviceRegKind, len(r.newlyRegistered))
	for id, kind := range r.newlyRegistered {
		if id == keep {
			continue
		}
		out[id] = kind
		delete(r.newlyRegistered, id)
	}
	return out
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

// checkAndRegisterDevice 检查并登记设备，返回 false 表示该账号对本请求不可选。
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

	limits := deviceLimitsFor(account)
	reg, err := s.deviceLimitCache.RegisterDevice(ctx, account.ID, req.deviceID, limits)
	if err != nil {
		// 失败开放：缓存错误时允许通过
		slog.Warn("device_limit.register_failed",
			"account_id", account.ID,
			"device", shortDeviceID(req.deviceID),
			"error", err)
		return true
	}
	if !reg.Allowed {
		slog.Info("device_limit.rejected",
			"account_id", account.ID,
			"device", shortDeviceID(req.deviceID),
			"reason", reg.Reason,
			"max_devices", limits.MaxDevices,
			"window_minutes", int(limits.Window.Minutes()),
			"max_devices_daily", limits.MaxDaily)
		return false
	}
	if reg.IsNew || reg.IsNewDaily {
		req.markNew(account.ID, reg.IsNewDaily)
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
	if req == nil {
		return
	}
	found, daily := req.takeNew(account.ID)
	if !found {
		return
	}
	s.unregisterDevice(ctx, account.ID, req.deviceID, daily)
}

// ReleaseAccountDeviceRegistration 释放本次请求在某个账号上的新登记（failover 换号时立即调用）。
// 非新登记、不适用、缓存不可用均为 no-op，幂等。
func (s *GatewayService) ReleaseAccountDeviceRegistration(ctx context.Context, req *DeviceLimitRequest, accountID int64) {
	if s == nil || req == nil || s.deviceLimitCache == nil {
		return
	}
	found, daily := req.takeNew(accountID)
	if !found {
		return
	}
	s.unregisterDevice(ctx, accountID, req.deviceID, daily)
}

// ReleaseUnservedDeviceRegistrations 释放本次请求除 servedAccountID 之外的所有新登记。
// 请求结束时无条件调用：成功时清掉换号链上未服务的账号，失败时（servedAccountID=0）清掉全部。幂等。
func (s *GatewayService) ReleaseUnservedDeviceRegistrations(ctx context.Context, req *DeviceLimitRequest, servedAccountID int64) {
	if s == nil || req == nil || s.deviceLimitCache == nil {
		return
	}
	for accountID, kind := range req.takeAllExcept(servedAccountID) {
		s.unregisterDevice(ctx, accountID, req.deviceID, kind.daily)
	}
}

// ====== 设备亲和：一台设备尽量固定在同一个账号上 ======

// deviceAffinitySet 返回候选账号中"本请求设备正占名额或 24h 内接纳过"的账号集合。
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

func (s *GatewayService) unregisterDevice(ctx context.Context, accountID int64, deviceID string, daily bool) {
	if err := s.deviceLimitCache.UnregisterDevice(ctx, accountID, deviceID, daily); err != nil {
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
