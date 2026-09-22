package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeLeaderLock struct {
	mu       sync.Mutex
	acquired bool
	granted  bool
	released bool
}

func (l *fakeLeaderLock) TryAcquireLeaderLock(_ context.Context, _, _ string, _ time.Duration) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.acquired = true
	return l.granted, nil
}

func (l *fakeLeaderLock) ReleaseLeaderLock(_ context.Context, _, _ string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.released = true
	return nil
}

type fakeReclaudeAccountLister struct {
	accounts []*Account
	err      error
}

func (l *fakeReclaudeAccountLister) ListReclaudeAccounts(context.Context) ([]*Account, error) {
	return l.accounts, l.err
}

func schedulerFixture(t *testing.T, granted bool, accounts ...*Account) (
	*ReclaudeScheduler, *fakeLeaderLock, *fakeReclaudeAccountStore, *recordingHeartbeatProbe,
) {
	t.Helper()
	lock := &fakeLeaderLock{granted: granted}
	store := newFakeReclaudeAccountStore(accounts...)
	probe := &recordingHeartbeatProbe{}
	scheduler := NewReclaudeScheduler(
		&fakeReclaudeAccountLister{accounts: accounts},
		store,
		NewReclaudeHeartbeatRunner(probe),
		lock,
		"instance-under-test",
	)
	return scheduler, lock, store, probe
}

func onlineReclaudeAccount(id int64) *Account {
	return &Account{ID: id, Platform: PlatformAnthropic, Type: AccountTypeReclaude}
}

// 🔴 多副本下每个进程各跑一份 ticker ⇒ 每台「设备」每分钟发 6 个请求、
// 来自 3 个 TCP 会话 —— 对端看到的是一台机器同时开着 3 个 daemon。
func TestReclaudeScheduler_LeaderLock(t *testing.T) {
	t.Run("拿到锁才心跳", func(t *testing.T) {
		scheduler, lock, _, probe := schedulerFixture(t, true, onlineReclaudeAccount(9))

		scheduler.RunOnce(context.Background())

		require.True(t, lock.acquired)
		require.NotEmpty(t, probe.snapshot())
	})

	t.Run("没拿到锁一个请求都不发", func(t *testing.T) {
		scheduler, lock, _, probe := schedulerFixture(t, false, onlineReclaudeAccount(9))

		scheduler.RunOnce(context.Background())

		require.True(t, lock.acquired)
		require.Empty(t, probe.snapshot(), "非 leader 副本也在发心跳")
	})

	t.Run("跑完释放锁", func(t *testing.T) {
		scheduler, lock, _, _ := schedulerFixture(t, true, onlineReclaudeAccount(9))

		scheduler.RunOnce(context.Background())

		require.True(t, lock.released)
	})
}

func TestReclaudeScheduler_OfflineWindow(t *testing.T) {
	// 模拟关机必须**同时**停心跳与停调度：只停一样都等于没关机。
	t.Run("窗口内写冷却且不心跳", func(t *testing.T) {
		account := &Account{
			ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude,
			Extra: map[string]any{
				ExtraKeyReclaudeOfflineWindow: map[string]any{
					"timezone": "UTC", "start_hour": float64(0), "duration_hours": float64(23),
				},
			},
		}
		scheduler, _, store, probe := schedulerFixture(t, true, account)

		scheduler.RunOnceAt(context.Background(), time.Date(2026, 9, 21, 3, 0, 0, 0, time.UTC))

		require.Empty(t, probe.snapshot(), "关机期间还在心跳，设备页面的「最近使用」会照常刷新")
		require.Equal(t, ReclaudeOfflineReason, store.cooldown[9].reason)
		require.True(t, store.cooldown[9].until.After(time.Date(2026, 9, 21, 3, 0, 0, 0, time.UTC)))
	})

	t.Run("窗口外不写冷却且正常心跳", func(t *testing.T) {
		account := &Account{
			ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude,
			Extra: map[string]any{
				ExtraKeyReclaudeOfflineWindow: map[string]any{
					"timezone": "UTC", "start_hour": float64(0), "duration_hours": float64(2),
				},
			},
		}
		scheduler, _, store, probe := schedulerFixture(t, true, account)

		scheduler.RunOnceAt(context.Background(), time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC))

		require.NotEmpty(t, probe.snapshot())
		require.NotContains(t, store.cooldown, int64(9))
	})

	// 冷却理由必须与限流、网关不可用可区分，否则运维看到停调度分不清成因。
	t.Run("关机理由与其它冷却成因互不相同", func(t *testing.T) {
		require.NotEqual(t, ReclaudeOfflineReason, ReclaudeRateLimitedReason)
		require.NotEqual(t, ReclaudeOfflineReason, ReclaudeGatewayUnavailableReason)
	})
}

func TestReclaudeScheduler_Robustness(t *testing.T) {
	t.Run("一个账号心跳失败不影响其它账号", func(t *testing.T) {
		scheduler, _, _, probe := schedulerFixture(t, true,
			onlineReclaudeAccount(9), onlineReclaudeAccount(10))

		scheduler.RunOnce(context.Background())

		// 两个账号各打两个端点
		require.Len(t, probe.snapshot(), 4)
	})

	t.Run("账号列表读取失败不 panic", func(t *testing.T) {
		scheduler := NewReclaudeScheduler(
			&fakeReclaudeAccountLister{err: context.DeadlineExceeded},
			newFakeReclaudeAccountStore(),
			NewReclaudeHeartbeatRunner(&recordingHeartbeatProbe{}),
			&fakeLeaderLock{granted: true},
			"instance",
		)

		require.NotPanics(t, func() { scheduler.RunOnce(context.Background()) })
	})

	t.Run("依赖缺席时不 panic", func(t *testing.T) {
		require.NotPanics(t, func() {
			NewReclaudeScheduler(nil, nil, nil, nil, "").RunOnce(context.Background())
		})
	})
}
