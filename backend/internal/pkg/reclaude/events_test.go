package reclaude

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassifyEvent(t *testing.T) {
	tests := []struct {
		name  string
		event ReclaudeEvent
		want  EventClass
	}{
		{"限流", ReclaudeEvent{Kind: EventKindRateLimited, RetryAfterSec: 30}, EventClassRateLimited},
		{"静默换号", ReclaudeEvent{Kind: EventKindAccountSwitched, NewAccountMaskedEmail: "a***@b.com"}, EventClassAccountSwitched},
		{"订阅将到期", ReclaudeEvent{Kind: EventKindSubscriptionExpiring, DaysLeft: 3}, EventClassSubscriptionExpiring},
		{"未知 Kind", ReclaudeEvent{Kind: "something_new"}, EventClassUnknown},
		{"空 Kind", ReclaudeEvent{}, EventClassUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ClassifyEvent(tt.event))
		})
	}
}

// 未知 Kind 绝不能被静默丢弃：它意味着协议变了。
func TestEventClassRequiresAlert(t *testing.T) {
	require.True(t, EventClassUnknown.RequiresAlert(), "未知 Kind 必须告警")
	require.True(t, EventClassAccountSwitched.RequiresAlert(), "换号必须告警")
	require.False(t, EventClassRateLimited.RequiresAlert())
}

// 幂等键：subscription_expiring 与 rate_limited 很可能在窗口内每个响应都带，
// 同一 Kind + 同一关键参数在窗口内只能处理一次，否则告警风暴 + 重复写库。
func TestEventIdempotencyKey(t *testing.T) {
	t.Run("同一事件的键稳定", func(t *testing.T) {
		event := ReclaudeEvent{Kind: EventKindRateLimited, RetryAfterSec: 30}

		require.Equal(t, EventIdempotencyKey(event), EventIdempotencyKey(event))
	})

	t.Run("关键参数变化则键变化", func(t *testing.T) {
		base := ReclaudeEvent{Kind: EventKindSubscriptionExpiring, DaysLeft: 7}
		closer := ReclaudeEvent{Kind: EventKindSubscriptionExpiring, DaysLeft: 3}

		require.NotEqual(t, EventIdempotencyKey(base), EventIdempotencyKey(closer))
	})

	t.Run("换号按新账号区分", func(t *testing.T) {
		first := ReclaudeEvent{Kind: EventKindAccountSwitched, NewAccountMaskedEmail: "a***@x.com"}
		second := ReclaudeEvent{Kind: EventKindAccountSwitched, NewAccountMaskedEmail: "b***@x.com"}

		require.NotEqual(t, EventIdempotencyKey(first), EventIdempotencyKey(second))
	})

	t.Run("不同 Kind 的键不冲突", func(t *testing.T) {
		rateLimited := ReclaudeEvent{Kind: EventKindRateLimited, RetryAfterSec: 3}
		expiring := ReclaudeEvent{Kind: EventKindSubscriptionExpiring, DaysLeft: 3}

		require.NotEqual(t, EventIdempotencyKey(rateLimited), EventIdempotencyKey(expiring))
	})

	// Reason 是自由文本，可能带时间戳之类的抖动；把它纳入幂等键会让幂等失效。
	t.Run("Reason 不参与幂等键", func(t *testing.T) {
		withReason := ReclaudeEvent{Kind: EventKindRateLimited, RetryAfterSec: 30, Reason: "quota exhausted at 12:01"}
		withoutReason := ReclaudeEvent{Kind: EventKindRateLimited, RetryAfterSec: 30}

		require.Equal(t, EventIdempotencyKey(withReason), EventIdempotencyKey(withoutReason))
	})
}
