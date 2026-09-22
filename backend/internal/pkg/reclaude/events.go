package reclaude

import (
	"strconv"
	"strings"
)

// 已知的事件 Kind。
//
// 🔴 K-5 未结案：枚举是从逆向材料推断的，需要 Phase 0 触发态实测补全。
// 正因为枚举不完整，未知 Kind 必须告警而不是静默丢弃 —— 它意味着协议变了。
const (
	EventKindRateLimited          = "rate_limited"
	EventKindAccountSwitched      = "account_switched"
	EventKindSubscriptionExpiring = "subscription_expiring"
)

// EventClass 是事件的语义分类，供 service 层映射到账号动作。
type EventClass string

const (
	EventClassRateLimited          EventClass = "rate_limited"
	EventClassAccountSwitched      EventClass = "account_switched"
	EventClassSubscriptionExpiring EventClass = "subscription_expiring"
	EventClassUnknown              EventClass = "unknown"
)

// ClassifyEvent 把线上的 Kind 映射成语义分类。
func ClassifyEvent(event ReclaudeEvent) EventClass {
	switch event.Kind {
	case EventKindRateLimited:
		return EventClassRateLimited
	case EventKindAccountSwitched:
		return EventClassAccountSwitched
	case EventKindSubscriptionExpiring:
		return EventClassSubscriptionExpiring
	default:
		return EventClassUnknown
	}
}

// RequiresAlert 表示该类事件是否需要推运维告警。
//
// unknown 必须告警：协议变了。
// account_switched 必须告警：底层账号被静默换掉会连锁作废会话粘性、
// prev_request_id 与 thinking 签名，不是一个可以事后再看的事件。
// rate_limited 是常态流控，只走冷却路径，不告警。
func (c EventClass) RequiresAlert() bool {
	return c == EventClassUnknown || c == EventClassAccountSwitched
}

// EventIdempotencyKey 生成事件的幂等键。
//
// rate_limited 与 subscription_expiring 很可能在窗口内**每个响应都带**，
// 不去重就是告警风暴 + 重复写库。键只取 Kind + 关键参数：
// Reason 是自由文本（可能带时间戳之类的抖动），纳入键会让幂等失效。
func EventIdempotencyKey(event ReclaudeEvent) string {
	var discriminator string
	switch ClassifyEvent(event) {
	case EventClassRateLimited:
		discriminator = strconv.Itoa(event.RetryAfterSec)
	case EventClassAccountSwitched:
		discriminator = event.NewAccountMaskedEmail
	case EventClassSubscriptionExpiring:
		discriminator = strconv.Itoa(event.DaysLeft)
	default:
		discriminator = event.Kind
	}
	return strings.Join([]string{string(ClassifyEvent(event)), discriminator}, "|")
}
