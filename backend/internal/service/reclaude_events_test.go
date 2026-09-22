package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/reclaude"
)

type recordedAction struct {
	kind        ReclaudeAccountActionKind
	accountID   int64
	retryAfter  int
	maskedEmail string
}

type recordingActuator struct {
	actions []recordedAction
}

func (r *recordingActuator) ApplyReclaudeAction(action ReclaudeAccountAction) {
	r.actions = append(r.actions, recordedAction{
		kind:        action.Kind,
		accountID:   action.AccountID,
		retryAfter:  action.RetryAfterSec,
		maskedEmail: action.NewAccountMaskedEmail,
	})
}

func (r *recordingActuator) kinds() []ReclaudeAccountActionKind {
	out := make([]ReclaudeAccountActionKind, 0, len(r.actions))
	for _, a := range r.actions {
		out = append(out, a.kind)
	}
	return out
}

func newTestEventDispatcher(actuator ReclaudeAccountActuator) *ReclaudeEventDispatcher {
	return NewReclaudeEventDispatcher(actuator, 10*time.Minute)
}

func TestReclaudeEventDispatcher(t *testing.T) {
	account := &Account{ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude}

	t.Run("限流事件映射为冷却", func(t *testing.T) {
		actuator := &recordingActuator{}
		newTestEventDispatcher(actuator).HandleReclaudeEvents(account, []reclaude.ReclaudeEvent{
			{Kind: reclaude.EventKindRateLimited, RetryAfterSec: 45},
		})

		require.Equal(t, []ReclaudeAccountActionKind{ReclaudeActionCooldown}, actuator.kinds())
		require.Equal(t, 45, actuator.actions[0].retryAfter)
		require.Equal(t, int64(9), actuator.actions[0].accountID)
	})

	// 换号会连锁作废会话粘性、prev_request_id 与 thinking 签名 ——
	// 同一个 account.ID 底下换了上游账号，旧的 request id 与签名新账号必拒。
	t.Run("换号事件映射为会话作废且带上新账号邮箱", func(t *testing.T) {
		actuator := &recordingActuator{}
		newTestEventDispatcher(actuator).HandleReclaudeEvents(account, []reclaude.ReclaudeEvent{
			{Kind: reclaude.EventKindAccountSwitched, NewAccountMaskedEmail: "a***@b.com"},
		})

		require.Equal(t, []ReclaudeAccountActionKind{ReclaudeActionAccountSwitched}, actuator.kinds())
		require.Equal(t, "a***@b.com", actuator.actions[0].maskedEmail)
	})

	t.Run("订阅到期事件映射为告警", func(t *testing.T) {
		actuator := &recordingActuator{}
		newTestEventDispatcher(actuator).HandleReclaudeEvents(account, []reclaude.ReclaudeEvent{
			{Kind: reclaude.EventKindSubscriptionExpiring, DaysLeft: 3},
		})

		require.Equal(t, []ReclaudeAccountActionKind{ReclaudeActionSubscriptionExpiring}, actuator.kinds())
	})

	// 未知 Kind 不能静默丢弃：它意味着协议变了。
	t.Run("未知 Kind 走告警且不影响请求", func(t *testing.T) {
		actuator := &recordingActuator{}
		newTestEventDispatcher(actuator).HandleReclaudeEvents(account, []reclaude.ReclaudeEvent{
			{Kind: "brand_new_kind"},
		})

		require.Equal(t, []ReclaudeAccountActionKind{ReclaudeActionUnknownEvent}, actuator.kinds())
	})

	// 铁律 0：rate_limited / subscription_expiring 很可能在窗口内**每个响应都带**，
	// 不去重就是告警风暴 + 重复写库。
	t.Run("窗口内同一事件只处理一次", func(t *testing.T) {
		actuator := &recordingActuator{}
		dispatcher := newTestEventDispatcher(actuator)
		event := reclaude.ReclaudeEvent{Kind: reclaude.EventKindSubscriptionExpiring, DaysLeft: 3}

		for i := 0; i < 5; i++ {
			dispatcher.HandleReclaudeEvents(account, []reclaude.ReclaudeEvent{event})
		}

		require.Len(t, actuator.actions, 1)
	})

	t.Run("关键参数变化时重新处理", func(t *testing.T) {
		actuator := &recordingActuator{}
		dispatcher := newTestEventDispatcher(actuator)

		dispatcher.HandleReclaudeEvents(account, []reclaude.ReclaudeEvent{
			{Kind: reclaude.EventKindSubscriptionExpiring, DaysLeft: 7},
		})
		dispatcher.HandleReclaudeEvents(account, []reclaude.ReclaudeEvent{
			{Kind: reclaude.EventKindSubscriptionExpiring, DaysLeft: 3},
		})

		require.Len(t, actuator.actions, 2)
	})

	// 幂等键必须按账号隔离，否则一个账号的限流会把另一个账号的同类事件吃掉。
	t.Run("不同账号之间幂等互不干扰", func(t *testing.T) {
		actuator := &recordingActuator{}
		dispatcher := newTestEventDispatcher(actuator)
		event := reclaude.ReclaudeEvent{Kind: reclaude.EventKindRateLimited, RetryAfterSec: 30}

		dispatcher.HandleReclaudeEvents(account, []reclaude.ReclaudeEvent{event})
		dispatcher.HandleReclaudeEvents(
			&Account{ID: 10, Platform: PlatformAnthropic, Type: AccountTypeReclaude},
			[]reclaude.ReclaudeEvent{event},
		)

		require.Len(t, actuator.actions, 2)
	})

	t.Run("窗口过期后重新处理", func(t *testing.T) {
		actuator := &recordingActuator{}
		dispatcher := NewReclaudeEventDispatcher(actuator, time.Nanosecond)
		event := reclaude.ReclaudeEvent{Kind: reclaude.EventKindRateLimited, RetryAfterSec: 30}

		dispatcher.HandleReclaudeEvents(account, []reclaude.ReclaudeEvent{event})
		time.Sleep(2 * time.Nanosecond)
		dispatcher.HandleReclaudeEvents(account, []reclaude.ReclaudeEvent{event})

		require.Len(t, actuator.actions, 2)
	})

	t.Run("一次响应里的多条事件逐条处理", func(t *testing.T) {
		actuator := &recordingActuator{}
		newTestEventDispatcher(actuator).HandleReclaudeEvents(account, []reclaude.ReclaudeEvent{
			{Kind: reclaude.EventKindRateLimited, RetryAfterSec: 10},
			{Kind: reclaude.EventKindSubscriptionExpiring, DaysLeft: 2},
		})

		require.Equal(t, []ReclaudeAccountActionKind{
			ReclaudeActionCooldown, ReclaudeActionSubscriptionExpiring,
		}, actuator.kinds())
	})

	t.Run("执行器缺席时不 panic", func(t *testing.T) {
		require.NotPanics(t, func() {
			newTestEventDispatcher(nil).HandleReclaudeEvents(account, []reclaude.ReclaudeEvent{
				{Kind: reclaude.EventKindRateLimited},
			})
		})
	})
}
