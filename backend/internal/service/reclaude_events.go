package service

import (
	"strconv"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/reclaude"
)

// ReclaudeAccountActionKind 是带外事件映射出来的账号动作。
type ReclaudeAccountActionKind string

const (
	// ReclaudeActionCooldown 进入冷却（走既有的限流冷却路径）。
	ReclaudeActionCooldown ReclaudeAccountActionKind = "cooldown"

	// ReclaudeActionAccountSwitched 底层 Claude 账号被静默换掉。
	//
	// 这一条的连锁后果最多：account.ID 没变但上游换了人 ⇒
	// 会话粘性、prev_request_id（旧 request id 会被当成假 parent-link 注入）、
	// thinking 签名（旧账号签的，新账号必拒）全部要作废；
	// 而合成 account_uuid **不得更换** —— 换了上游就会看到「一台设备突然换了人」。
	ReclaudeActionAccountSwitched ReclaudeAccountActionKind = "account_switched"

	// ReclaudeActionSubscriptionExpiring 订阅将到期，运维告警。
	ReclaudeActionSubscriptionExpiring ReclaudeAccountActionKind = "subscription_expiring"

	// ReclaudeActionCredentialRevoked 网关拒绝了我们的身份（401/403）。
	//
	// 与冷却的区别是**它不会自己好**：SK 被撤销 / 设备被顶掉之后，恢复要走
	// 「买新订阅 + 新代理 + 新人设 + 重走建号流程」的多小时 runbook
	// （R-4 禁止二次 login）。所以动作是置 error + 停调度 + 立刻告警，
	// 绝不能写成一个会到期自动恢复的冷却。
	ReclaudeActionCredentialRevoked ReclaudeAccountActionKind = "credential_revoked"

	// ReclaudeActionGatewayUnavailable 网关节点自身故障（5xx / 连接失败）。
	//
	// **不自动切节点**（§6.7）：探测与漂移本身就是可识别的行为特征，
	// 一台「个人电脑」不会每次换边缘节点。短冷却 + 告警，人工确认。
	ReclaudeActionGatewayUnavailable ReclaudeAccountActionKind = "gateway_unavailable"

	// ReclaudeActionBoundEmailObserved 第一次观测到绑定邮箱（建号后的首次心跳）。
	//
	// 与换号区分开：建号时没有这个字段，首次拿到它只是回填。当成换号会白白推进
	// 身份纪元，把刚建好的会话态整体作废。
	ReclaudeActionBoundEmailObserved ReclaudeAccountActionKind = "bound_email_observed"

	// ReclaudeActionUnknownEvent 未知 Kind。
	//
	// **不能静默丢弃**：枚举是从逆向材料推断的，出现新 Kind 意味着协议变了。
	ReclaudeActionUnknownEvent ReclaudeAccountActionKind = "unknown_event"
)

// ReclaudeAccountAction 是一条待执行的账号动作。
type ReclaudeAccountAction struct {
	Kind                  ReclaudeAccountActionKind
	AccountID             int64
	Reason                string
	RetryAfterSec         int
	DaysLeft              int
	NewAccountMaskedEmail string
	RawKind               string
}

// ReclaudeAccountActuator 执行账号动作。
//
// 把「解析事件」与「改账号状态」分开：前者是纯映射且必须幂等，
// 后者要碰仓储与缓存。
type ReclaudeAccountActuator interface {
	ApplyReclaudeAction(action ReclaudeAccountAction)
}

// ReclaudeEventDispatcher 把带外事件映射成账号动作，并在窗口内去重。
type ReclaudeEventDispatcher struct {
	actuator ReclaudeAccountActuator
	window   time.Duration

	mu   sync.Mutex
	seen map[string]time.Time
}

// NewReclaudeEventDispatcher 构造分发器。window 是幂等窗口。
func NewReclaudeEventDispatcher(actuator ReclaudeAccountActuator, window time.Duration) *ReclaudeEventDispatcher {
	return &ReclaudeEventDispatcher{
		actuator: actuator,
		window:   window,
		seen:     make(map[string]time.Time),
	}
}

// HandleReclaudeEvents 实现 ReclaudeEventHandler。
//
// 四条铁律：
//  1. 永远不把 event 写进返回给下游的 body（由调用方保证：events 走本函数，不进 body）
//  2. 未知 Kind 不静默丢弃
//  3. status=200 的响应里出现 events 也要处理（它是带外信道，不是错误信道）
//  4. 窗口内幂等，否则告警风暴 + 重复写库
func (d *ReclaudeEventDispatcher) HandleReclaudeEvents(account *Account, events []reclaude.ReclaudeEvent) {
	if d == nil || account == nil || len(events) == 0 {
		return
	}

	for _, event := range events {
		if !d.shouldHandle(account.ID, event) {
			continue
		}
		action := mapReclaudeEvent(account.ID, event)
		if d.actuator != nil {
			d.actuator.ApplyReclaudeAction(action)
		}
	}
}

// HandleReclaudeGatewayError 把外层非 200 映射成账号动作。
//
// 复用同一个去重窗口：一次 401 会被这一轮的每次重试、每个并发请求各触发一遍，
// 不去重就是告警风暴 + 重复写库 —— 与带外事件完全同源的问题。
func (d *ReclaudeEventDispatcher) HandleReclaudeGatewayError(account *Account, statusCode int) {
	if d == nil || account == nil || d.actuator == nil {
		return
	}
	if !d.shouldHandleKey(account.ID, "gateway_status", strconv.Itoa(statusCode)) {
		return
	}

	d.actuator.ApplyReclaudeAction(ReclaudeAccountAction{
		Kind:      ClassifyReclaudeGatewayStatus(statusCode),
		AccountID: account.ID,
		Reason:    "reclaude gateway status " + strconv.Itoa(statusCode),
		RawKind:   "gateway_status_" + strconv.Itoa(statusCode),
	})
}

// shouldHandle 做窗口内去重。键按账号隔离，否则一个账号的限流事件会把另一个
// 账号的同类事件吃掉。
func (d *ReclaudeEventDispatcher) shouldHandle(accountID int64, event reclaude.ReclaudeEvent) bool {
	return d.shouldHandleKey(accountID, "event", reclaude.EventIdempotencyKey(event))
}

// shouldHandleKey 是去重的公共实现。namespace 把带外事件与网关错误分开，
// 否则两条信道会互相吃掉对方的去重位。
func (d *ReclaudeEventDispatcher) shouldHandleKey(accountID int64, namespace, key string) bool {
	full := strconv.FormatInt(accountID, 10) + "|" + namespace + "|" + key
	now := time.Now()

	d.mu.Lock()
	defer d.mu.Unlock()

	if seenAt, ok := d.seen[full]; ok && now.Sub(seenAt) < d.window {
		return false
	}
	d.seen[full] = now
	d.pruneLocked(now)
	return true
}

// pruneLocked 清掉过期条目，避免 map 无限增长。
func (d *ReclaudeEventDispatcher) pruneLocked(now time.Time) {
	for key, seenAt := range d.seen {
		if now.Sub(seenAt) >= d.window {
			delete(d.seen, key)
		}
	}
}

func mapReclaudeEvent(accountID int64, event reclaude.ReclaudeEvent) ReclaudeAccountAction {
	action := ReclaudeAccountAction{
		AccountID:             accountID,
		Reason:                event.Reason,
		RetryAfterSec:         event.RetryAfterSec,
		DaysLeft:              event.DaysLeft,
		NewAccountMaskedEmail: event.NewAccountMaskedEmail,
		RawKind:               event.Kind,
	}

	switch reclaude.ClassifyEvent(event) {
	case reclaude.EventClassRateLimited:
		action.Kind = ReclaudeActionCooldown
	case reclaude.EventClassAccountSwitched:
		action.Kind = ReclaudeActionAccountSwitched
	case reclaude.EventClassSubscriptionExpiring:
		action.Kind = ReclaudeActionSubscriptionExpiring
	default:
		action.Kind = ReclaudeActionUnknownEvent
	}
	return action
}
