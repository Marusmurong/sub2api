package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeReclaudeAccountSource 是 ReclaudeAccountSource 的最小替身。
type fakeReclaudeAccountSource struct {
	listed         []Account
	listErr        error
	gotType        string
	gotStatus      string
	byIDsArg       []int64
	byIDs          []*Account
	byIDsErr       error
	getByIDArg     int64
	extraID        int64
	extraUpdates   map[string]any
	clearedErrorID int64
	schedulableID  int64
	schedulable    bool
	errorID        int64
	errorMessage   string
	unschedID      int64
	unschedUntil   time.Time
	unschedWhy     string
}

func (f *fakeReclaudeAccountSource) ListAllWithFilters(
	_ context.Context, _, accountType, status, _ string, _ int64, _ string,
) ([]Account, error) {
	f.gotType, f.gotStatus = accountType, status
	return f.listed, f.listErr
}

func (f *fakeReclaudeAccountSource) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	f.byIDsArg = ids
	return f.byIDs, f.byIDsErr
}

func (f *fakeReclaudeAccountSource) GetByID(_ context.Context, id int64) (*Account, error) {
	f.getByIDArg = id
	return &Account{ID: id}, nil
}

func (f *fakeReclaudeAccountSource) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	f.extraID, f.extraUpdates = id, updates
	return nil
}

func (f *fakeReclaudeAccountSource) ClearError(_ context.Context, id int64) error {
	f.clearedErrorID = id
	return nil
}

func (f *fakeReclaudeAccountSource) SetSchedulable(_ context.Context, id int64, schedulable bool) error {
	f.schedulableID, f.schedulable = id, schedulable
	return nil
}

func (f *fakeReclaudeAccountSource) SetError(_ context.Context, id int64, message string) error {
	f.errorID, f.errorMessage = id, message
	return nil
}

func (f *fakeReclaudeAccountSource) SetTempUnschedulable(
	_ context.Context, id int64, until time.Time, reason string,
) error {
	f.unschedID, f.unschedUntil, f.unschedWhy = id, until, reason
	return nil
}

func TestReclaudeAccountRepoAdapter_ListReclaudeAccounts(t *testing.T) {
	ctx := context.Background()

	t.Run("按类型过滤且不传 status 过滤", func(t *testing.T) {
		src := &fakeReclaudeAccountSource{
			listed: []Account{{ID: 7, Type: AccountTypeReclaude, Status: StatusActive}},
			byIDs:  []*Account{{ID: 7, Type: AccountTypeReclaude}},
		}

		_, err := NewReclaudeAccountRepoAdapter(src).ListReclaudeAccounts(ctx)

		require.NoError(t, err)
		require.Equal(t, AccountTypeReclaude, src.gotType)
		// 🔴 不能传 StatusActive：那个分支会顺带排掉 temp_unschedulable 与限流冷却中的账号，
		// 而这两种账号恰恰必须继续心跳，否则「一批设备同时静默」。
		require.Empty(t, src.gotStatus)
	})

	t.Run("只保留 active 账号", func(t *testing.T) {
		src := &fakeReclaudeAccountSource{
			listed: []Account{
				{ID: 1, Type: AccountTypeReclaude, Status: StatusActive},
				{ID: 2, Type: AccountTypeReclaude, Status: "disabled"},
				{ID: 3, Type: AccountTypeReclaude, Status: "error"},
			},
			byIDs: []*Account{{ID: 1, Type: AccountTypeReclaude}},
		}

		accounts, err := NewReclaudeAccountRepoAdapter(src).ListReclaudeAccounts(ctx)

		require.NoError(t, err)
		require.Equal(t, []int64{1}, src.byIDsArg)
		require.Len(t, accounts, 1)
	})

	t.Run("经 GetByIDs 二次取以预载 Proxy", func(t *testing.T) {
		// 🔴 列表查询不 WithProxy，而 probe 在 Proxy 为空时拒发（宁可不心跳也不走机房 IP）。
		// 直接返回列表结果 = 每一台设备都心跳失败。
		src := &fakeReclaudeAccountSource{
			listed: []Account{{ID: 5, Type: AccountTypeReclaude, Status: StatusActive}},
			byIDs:  []*Account{{ID: 5, Type: AccountTypeReclaude, Proxy: &Proxy{}}},
		}

		accounts, err := NewReclaudeAccountRepoAdapter(src).ListReclaudeAccounts(ctx)

		require.NoError(t, err)
		require.Len(t, accounts, 1)
		require.NotNil(t, accounts[0].Proxy)
	})

	t.Run("没有 active 账号时不查二次", func(t *testing.T) {
		src := &fakeReclaudeAccountSource{
			listed: []Account{{ID: 9, Type: AccountTypeReclaude, Status: "disabled"}},
		}

		accounts, err := NewReclaudeAccountRepoAdapter(src).ListReclaudeAccounts(ctx)

		require.NoError(t, err)
		require.Empty(t, accounts)
		require.Nil(t, src.byIDsArg)
	})

	t.Run("列表出错时透传", func(t *testing.T) {
		src := &fakeReclaudeAccountSource{listErr: errors.New("db down")}

		_, err := NewReclaudeAccountRepoAdapter(src).ListReclaudeAccounts(ctx)

		require.Error(t, err)
	})
}

func TestReclaudeAccountRepoAdapter_Store(t *testing.T) {
	ctx := context.Background()
	src := &fakeReclaudeAccountSource{}
	adapter := NewReclaudeAccountRepoAdapter(src)
	until := time.Now().Add(time.Hour)

	require.NoError(t, adapter.SetTempUnschedulable(ctx, 11, until, ReclaudeOfflineReason))
	require.Equal(t, int64(11), src.unschedID)
	require.Equal(t, ReclaudeOfflineReason, src.unschedWhy)

	require.NoError(t, adapter.UpdateAccountExtra(ctx, 12, map[string]any{"k": "v"}))
	require.Equal(t, int64(12), src.extraID)
	require.Equal(t, map[string]any{"k": "v"}, src.extraUpdates)

	require.NoError(t, adapter.SetError(ctx, 14, "revoked"))
	require.Equal(t, int64(14), src.errorID)
	require.Equal(t, "revoked", src.errorMessage)

	require.NoError(t, adapter.ClearAccountError(ctx, 15))
	require.Equal(t, int64(15), src.clearedErrorID)
	require.NoError(t, adapter.SetAccountSchedulable(ctx, 16, true))
	require.Equal(t, int64(16), src.schedulableID)
	require.True(t, src.schedulable)

	account, err := adapter.GetAccount(ctx, 13)
	require.NoError(t, err)
	require.Equal(t, int64(13), account.ID)
}

// 适配器必须同时满足 scheduler 与 actuator 的两个小接口 —— 否则接线时才炸。
func TestReclaudeAccountRepoAdapter_SatisfiesInterfaces(t *testing.T) {
	var _ ReclaudeAccountLister = (*ReclaudeAccountRepoAdapter)(nil)
	var _ ReclaudeAccountStore = (*ReclaudeAccountRepoAdapter)(nil)
	var _ ReclaudeSelfCheckStore = (*ReclaudeAccountRepoAdapter)(nil)
	// AccountRepository 必须结构性满足 ReclaudeAccountSource，接线时才能直接传进来。
	var _ ReclaudeAccountSource = (AccountRepository)(nil)
}
