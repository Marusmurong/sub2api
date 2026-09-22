package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	// ExtraKeyReclaudeLastSwitchedAt 最近一次静默换号的时间，管理端列表展示。
	// 换号频率 = 供给稳定性指标。
	ExtraKeyReclaudeLastSwitchedAt = "reclaude_last_switched_at"

	// ExtraKeyReclaudeBoundEmail 当前绑定的 Claude 账号掩码邮箱。
	//
	// ⚠️ 这是一个**会变的展示字段**，不是账号的固有属性，更不能参与命名。
	ExtraKeyReclaudeBoundEmail = "reclaude_bound_email"

	// ReclaudeRateLimitedReason 是限流冷却写入的理由。
	// 与模拟关机、网关不可用三者互相可区分，否则运维看到冷却分不清成因。
	ReclaudeRateLimitedReason = "reclaude_rate_limited"
)

// reclaudeDefaultCooldown 是对端没给 RetryAfterSec 时的保守兜底。
const reclaudeDefaultCooldown = 60 * time.Second

// ReclaudeAccountStore 是执行账号动作需要的最小仓储面。
//
// 刻意定义成小接口而不是直接用 AccountRepository：这里只需要三件事，
// 小接口让测试不必搭起整个仓储。
type ReclaudeAccountStore interface {
	SetTempUnschedulable(ctx context.Context, accountID int64, until time.Time, reason string) error
	UpdateAccountExtra(ctx context.Context, accountID int64, updates map[string]any) error
	GetAccount(ctx context.Context, accountID int64) (*Account, error)
	// SetError 置 status=error 并停止调度。用于凭据失效这类**不会自己好**的故障。
	SetError(ctx context.Context, accountID int64, message string) error
}

// ReclaudeAlerter 把需要人看的事件推出去。
type ReclaudeAlerter interface {
	AlertReclaude(action ReclaudeAccountAction)
}

// ReclaudeAccountService 把带外事件落成真实的账号状态变更。
type ReclaudeAccountService struct {
	store   ReclaudeAccountStore
	alerter ReclaudeAlerter
}

// NewReclaudeAccountService 构造执行器。
func NewReclaudeAccountService(store ReclaudeAccountStore, alerter ReclaudeAlerter) *ReclaudeAccountService {
	return &ReclaudeAccountService{store: store, alerter: alerter}
}

// ApplyReclaudeAction 实现 ReclaudeAccountActuator。
//
// ⚠️ 整个方法**不返回错误、不中断当前请求**：事件是搭在响应上的带外信道，
// 处理它失败不应该让用户的这次请求失败。失败走告警。
func (s *ReclaudeAccountService) ApplyReclaudeAction(action ReclaudeAccountAction) {
	if s == nil {
		return
	}
	ctx := context.Background()

	switch action.Kind {
	case ReclaudeActionCooldown:
		s.applyCooldown(ctx, action)
	case ReclaudeActionAccountSwitched:
		s.applyAccountSwitched(ctx, action)
		s.alert(action)
	case ReclaudeActionBoundEmailObserved:
		s.applyBoundEmailObserved(ctx, action)
	case ReclaudeActionCredentialRevoked:
		s.applyCredentialRevoked(ctx, action)
		s.alert(action)
	case ReclaudeActionGatewayUnavailable:
		s.applyGatewayUnavailable(ctx, action)
		s.alert(action)
	case ReclaudeActionSubscriptionExpiring, ReclaudeActionUnknownEvent:
		s.alert(action)
	}
}

// applyCredentialRevoked 置 error 并停止调度。
//
// 🔴 刻意**不写冷却**：冷却会到期自动恢复调度，而凭据失效不会自己好 ——
// 恢复要走「买新订阅 + 新代理 + 新人设 + 重走建号流程」的多小时 runbook。
// 让它自动复活只会在每个冷却周期结束时重新打一轮必然 401 的请求。
func (s *ReclaudeAccountService) applyCredentialRevoked(ctx context.Context, action ReclaudeAccountAction) {
	if s.store == nil {
		return
	}

	message := ReclaudeCredentialRevokedReason
	if action.Reason != "" {
		message += ": " + action.Reason
	}

	if err := s.store.SetError(ctx, action.AccountID, message); err != nil {
		logger.LegacyPrintf("service.reclaude",
			"failed to disable account %d after credential revocation: %v", action.AccountID, err)
	}
}

// applyGatewayUnavailable 写一个短冷却。
//
// 理由字符串必须与模拟关机、限流冷却三者互相可区分 —— 运维看到一台设备停了，
// 第一个问题永远是「它自己合盖了，还是被限流了，还是节点挂了」。
func (s *ReclaudeAccountService) applyGatewayUnavailable(ctx context.Context, action ReclaudeAccountAction) {
	if s.store == nil {
		return
	}

	until := time.Now().Add(ReclaudeGatewayUnavailableCooldown)
	if err := s.store.SetTempUnschedulable(ctx, action.AccountID, until, ReclaudeGatewayUnavailableReason); err != nil {
		logger.LegacyPrintf("service.reclaude",
			"failed to cool down account %d after gateway failure: %v", action.AccountID, err)
		s.alert(action)
	}
}

// applyCooldown 走既有的 TempUnschedulableUntil 冷却路径。
//
// 不告警：限流是常态流控，告警会变成噪音。
func (s *ReclaudeAccountService) applyCooldown(ctx context.Context, action ReclaudeAccountAction) {
	if s.store == nil {
		return
	}

	cooldown := reclaudeDefaultCooldown
	if action.RetryAfterSec > 0 {
		cooldown = time.Duration(action.RetryAfterSec) * time.Second
	}

	// 🔴 封顶。RetryAfterSec 来自**底层那个 Claude 账号**的限流窗口，不是我们
	// 配额包的；换号之后它指向一个已经不相关的时间点。不封顶就是一台设备
	// 白白躺几个小时 —— 而这类账号一共没几台。
	now := time.Now()
	until := CapReclaudeCooldown(
		&Account{Type: AccountTypeReclaude}, now.Add(cooldown), now)

	if err := s.store.SetTempUnschedulable(
		ctx, action.AccountID, until, ReclaudeRateLimitedReason,
	); err != nil {
		logger.LegacyPrintf("service.reclaude",
			"failed to cool down account %d: %v", action.AccountID, err)
		s.alert(action)
	}
}

// applyAccountSwitched 处理底层 Claude 账号被静默换掉。
//
// 连锁的核心是**推进身份纪元**：sub2api 里会话态的 key 都以 account.ID 为轴，
// 而换号时 account.ID 不变 ⇒ 旧的 prev_request_id 会被当成假 parent-link 发上去，
// 旧的 thinking 签名新账号必拒。纪元 +1 把这些态整体翻页。
//
// 🔴 合成 account_uuid **绝不跟着换** —— 换了上游就会看到「一台设备突然换了人」。
func (s *ReclaudeAccountService) applyAccountSwitched(ctx context.Context, action ReclaudeAccountAction) {
	if s.store == nil {
		return
	}

	account, err := s.store.GetAccount(ctx, action.AccountID)
	if err != nil {
		logger.LegacyPrintf("service.reclaude",
			"failed to load account %d for switch handling: %v", action.AccountID, err)
		return
	}

	updates := map[string]any{
		ExtraKeyReclaudeIdentityEpoch:  NextReclaudeIdentityEpoch(account),
		ExtraKeyReclaudeLastSwitchedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if action.NewAccountMaskedEmail != "" {
		updates[ExtraKeyReclaudeBoundEmail] = action.NewAccountMaskedEmail
	}

	if err := s.store.UpdateAccountExtra(ctx, action.AccountID, updates); err != nil {
		logger.LegacyPrintf("service.reclaude",
			"failed to advance identity epoch for account %d: %v", action.AccountID, err)
	}
}

// applyBoundEmailObserved 只回填「当前绑定的 Claude 账号邮箱」。
//
// 🔴 刻意**不推进身份纪元**、不写「最近换号时间」、不告警：建号后第一次心跳
// 拿到这个字段不是换号。推进纪元会把 prev_request_id 与签名态整体翻页，
// 等于把刚建好的会话态白白作废。
func (s *ReclaudeAccountService) applyBoundEmailObserved(ctx context.Context, action ReclaudeAccountAction) {
	if s.store == nil || action.NewAccountMaskedEmail == "" {
		return
	}

	updates := map[string]any{ExtraKeyReclaudeBoundEmail: action.NewAccountMaskedEmail}
	if err := s.store.UpdateAccountExtra(ctx, action.AccountID, updates); err != nil {
		logger.LegacyPrintf("service.reclaude",
			"failed to record bound email for account %d: %v", action.AccountID, err)
	}
}

func (s *ReclaudeAccountService) alert(action ReclaudeAccountAction) {
	if s.alerter == nil {
		return
	}
	s.alerter.AlertReclaude(action)
}
