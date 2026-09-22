package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeReclaudeAccountStore struct {
	mu       sync.Mutex
	accounts map[int64]*Account
	cooldown map[int64]cooldownRecord
	extra    map[int64]map[string]any

	errorMessages map[int64]string

	err error
}

type cooldownRecord struct {
	until  time.Time
	reason string
}

func newFakeReclaudeAccountStore(accounts ...*Account) *fakeReclaudeAccountStore {
	store := &fakeReclaudeAccountStore{
		accounts: map[int64]*Account{},
		cooldown: map[int64]cooldownRecord{},
		extra:    map[int64]map[string]any{},

		errorMessages: map[int64]string{},
	}
	for _, account := range accounts {
		store.accounts[account.ID] = account
	}
	return store
}

func (s *fakeReclaudeAccountStore) SetTempUnschedulable(
	_ context.Context, accountID int64, until time.Time, reason string,
) error {
	if s.err != nil {
		return s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cooldown[accountID] = cooldownRecord{until: until, reason: reason}
	return nil
}

func (s *fakeReclaudeAccountStore) UpdateAccountExtra(
	_ context.Context, accountID int64, updates map[string]any,
) error {
	if s.err != nil {
		return s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.extra[accountID] = updates
	return nil
}

func (s *fakeReclaudeAccountStore) SetError(
	_ context.Context, accountID int64, message string,
) error {
	if s.err != nil {
		return s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errorMessages[accountID] = message
	return nil
}

func (s *fakeReclaudeAccountStore) GetAccount(_ context.Context, accountID int64) (*Account, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.accounts[accountID]
	if !ok {
		return nil, errors.New("account not found")
	}
	return account, nil
}

type recordingAlerter struct {
	mu     sync.Mutex
	alerts []ReclaudeAccountAction
}

func (a *recordingAlerter) AlertReclaude(action ReclaudeAccountAction) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.alerts = append(a.alerts, action)
}

func (a *recordingAlerter) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.alerts)
}

func reclaudeActuatorFixture(t *testing.T) (*ReclaudeAccountService, *fakeReclaudeAccountStore, *recordingAlerter) {
	t.Helper()
	account := &Account{
		ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude,
		Extra: map[string]any{ExtraKeyReclaudeIdentityEpoch: float64(4)},
	}
	store := newFakeReclaudeAccountStore(account)
	alerter := &recordingAlerter{}
	return NewReclaudeAccountService(store, alerter), store, alerter
}

func TestReclaudeAccountService_Cooldown(t *testing.T) {
	t.Run("按 RetryAfterSec 写冷却，理由可区分", func(t *testing.T) {
		// Arrange
		service, store, _ := reclaudeActuatorFixture(t)

		// Act
		service.ApplyReclaudeAction(ReclaudeAccountAction{
			Kind: ReclaudeActionCooldown, AccountID: 9, RetryAfterSec: 45,
		})

		// Assert
		record := store.cooldown[9]
		require.WithinDuration(t, time.Now().Add(45*time.Second), record.until, 2*time.Second)
		require.Equal(t, ReclaudeRateLimitedReason, record.reason)
		require.NotEqual(t, ReclaudeOfflineReason, record.reason,
			"冷却理由必须与模拟关机可区分，否则运维分不清是作息还是被限流")
	})

	t.Run("没给 RetryAfterSec 时用保守兜底", func(t *testing.T) {
		service, store, _ := reclaudeActuatorFixture(t)

		service.ApplyReclaudeAction(ReclaudeAccountAction{Kind: ReclaudeActionCooldown, AccountID: 9})

		require.True(t, store.cooldown[9].until.After(time.Now()))
	})

	t.Run("限流不告警（它是常态流控）", func(t *testing.T) {
		service, _, alerter := reclaudeActuatorFixture(t)

		service.ApplyReclaudeAction(ReclaudeAccountAction{
			Kind: ReclaudeActionCooldown, AccountID: 9, RetryAfterSec: 10,
		})

		require.Zero(t, alerter.count())
	})
}

func TestReclaudeAccountService_AccountSwitched(t *testing.T) {
	t.Run("推进身份纪元", func(t *testing.T) {
		service, store, _ := reclaudeActuatorFixture(t)

		service.ApplyReclaudeAction(ReclaudeAccountAction{
			Kind: ReclaudeActionAccountSwitched, AccountID: 9, NewAccountMaskedEmail: "a***@b.com",
		})

		require.Equal(t, int64(5), store.extra[9][ExtraKeyReclaudeIdentityEpoch],
			"纪元没推进 ⇒ 旧的 prev_request_id 与 thinking 签名会继续被用到新上游账号上")
	})

	// 🔴 合成 account_uuid 绝不能跟着换 —— 换了上游就会看到
	// 「一台设备突然换了人」，那比换号本身更致命。
	t.Run("绝不更换合成 account_uuid", func(t *testing.T) {
		service, store, _ := reclaudeActuatorFixture(t)

		service.ApplyReclaudeAction(ReclaudeAccountAction{
			Kind: ReclaudeActionAccountSwitched, AccountID: 9, NewAccountMaskedEmail: "a***@b.com",
		})

		require.NotContains(t, store.extra[9], CredKeyReclaudeSyntheticAccountUUID)
	})

	t.Run("记录最近换号时间供管理端展示", func(t *testing.T) {
		service, store, _ := reclaudeActuatorFixture(t)

		service.ApplyReclaudeAction(ReclaudeAccountAction{
			Kind: ReclaudeActionAccountSwitched, AccountID: 9, NewAccountMaskedEmail: "a***@b.com",
		})

		require.Contains(t, store.extra[9], ExtraKeyReclaudeLastSwitchedAt)
		require.Equal(t, "a***@b.com", store.extra[9][ExtraKeyReclaudeBoundEmail])
	})

	t.Run("换号必须告警", func(t *testing.T) {
		service, _, alerter := reclaudeActuatorFixture(t)

		service.ApplyReclaudeAction(ReclaudeAccountAction{
			Kind: ReclaudeActionAccountSwitched, AccountID: 9,
		})

		require.Equal(t, 1, alerter.count())
	})

	t.Run("不中断当前请求：写库失败只告警", func(t *testing.T) {
		service, store, alerter := reclaudeActuatorFixture(t)
		store.err = errors.New("db down")

		require.NotPanics(t, func() {
			service.ApplyReclaudeAction(ReclaudeAccountAction{
				Kind: ReclaudeActionAccountSwitched, AccountID: 9,
			})
		})
		require.Positive(t, alerter.count())
	})
}

func TestReclaudeAccountService_Alerts(t *testing.T) {
	t.Run("订阅到期告警", func(t *testing.T) {
		service, _, alerter := reclaudeActuatorFixture(t)

		service.ApplyReclaudeAction(ReclaudeAccountAction{
			Kind: ReclaudeActionSubscriptionExpiring, AccountID: 9, DaysLeft: 3,
		})

		require.Equal(t, 1, alerter.count())
	})

	// 未知 Kind 意味着协议变了，绝不能静默丢弃。
	t.Run("未知事件告警", func(t *testing.T) {
		service, _, alerter := reclaudeActuatorFixture(t)

		service.ApplyReclaudeAction(ReclaudeAccountAction{
			Kind: ReclaudeActionUnknownEvent, AccountID: 9, RawKind: "brand_new",
		})

		require.Equal(t, 1, alerter.count())
	})

	t.Run("依赖缺席时不 panic", func(t *testing.T) {
		require.NotPanics(t, func() {
			NewReclaudeAccountService(nil, nil).ApplyReclaudeAction(
				ReclaudeAccountAction{Kind: ReclaudeActionCooldown, AccountID: 9})
		})
	})
}
