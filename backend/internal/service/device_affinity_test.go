//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

// deviceAffinityFixture 三个 Anthropic OAuth 账号走负载感知路径：
//   - 账号 1：限 1 台设备，优先级 3（最差）
//   - 账号 2：限 1 台设备，优先级 2
//   - 账号 3：不限设备，优先级 1（最优，常规选号必选它）
//
// 用 deviceLimitCacheStub.affinity 模拟"本设备已在账号 1 登记"。
type deviceAffinityFixture struct {
	svc              *GatewayService
	ctx              context.Context
	groupID          int64
	cache            *mockGatewayCacheForPlatform
	concurrencyCache *mockConcurrencyCache
	deviceCache      *deviceLimitCacheStub
}

func newDeviceAffinityFixture(t *testing.T, routing map[string][]int64) *deviceAffinityFixture {
	t.Helper()
	groupID := int64(10)

	accountRepo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Priority: 3, Status: StatusActive, Schedulable: true, Concurrency: 5, Extra: map[string]any{"max_devices": 1, "device_window_minutes": 60}},
			{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Priority: 2, Status: StatusActive, Schedulable: true, Concurrency: 5, Extra: map[string]any{"max_devices": 1, "device_window_minutes": 60}},
			{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range accountRepo.accounts {
		accountRepo.accountsByID[accountRepo.accounts[i].ID] = &accountRepo.accounts[i]
	}

	group := &Group{ID: groupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true, ModelRoutingEnabled: len(routing) > 0, ModelRouting: routing}
	groupRepo := &mockGroupRepoForGateway{groups: map[int64]*Group{groupID: group}}

	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 45 * time.Second
	cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{}}
	concurrencyCache := &mockConcurrencyCache{}
	deviceCache := newDeviceLimitCacheStub(true, false)

	svc := &GatewayService{
		accountRepo:        accountRepo,
		groupRepo:          groupRepo,
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
		deviceLimitCache:   deviceCache,
	}
	return &deviceAffinityFixture{
		svc:              svc,
		ctx:              context.WithValue(context.Background(), ctxkey.Group, group),
		groupID:          groupID,
		cache:            cache,
		concurrencyCache: concurrencyCache,
		deviceCache:      deviceCache,
	}
}

func (f *deviceAffinityFixture) ctxWithDevice() (context.Context, *DeviceLimitRequest) {
	req := NewDeviceLimitRequest(legacyUserID(testDeviceHex))
	return WithDeviceLimitRequest(f.ctx, req), req
}

func TestDeviceAffinity_PrefersAccountWhereDeviceIsRegistered(t *testing.T) {
	t.Parallel()
	f := newDeviceAffinityFixture(t, nil)
	f.deviceCache.affinity[1] = struct{}{}
	ctx, _ := f.ctxWithDevice()

	result, err := f.svc.SelectAccountWithLoadAwareness(ctx, &f.groupID, "new-conversation", "claude-fable-5-1", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, int64(1), result.Account.ID, "设备已登记的账号即使优先级最差也应优先")
	require.True(t, result.Acquired)
	require.Equal(t, int64(1), f.cache.sessionBindings["new-conversation"], "新对话应粘到亲和账号")
}

func TestDeviceAffinity_NoDeviceContextFallsBackToNormalSelection(t *testing.T) {
	t.Parallel()
	f := newDeviceAffinityFixture(t, nil)
	f.deviceCache.affinity[1] = struct{}{}

	result, err := f.svc.SelectAccountWithLoadAwareness(f.ctx, &f.groupID, "s", "claude-fable-5-1", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(3), result.Account.ID, "无设备上下文时按优先级选最优账号")
}

func TestDeviceAffinity_UnregisteredDeviceUsesNormalSelection(t *testing.T) {
	t.Parallel()
	f := newDeviceAffinityFixture(t, nil)
	ctx, req := f.ctxWithDevice()

	result, err := f.svc.SelectAccountWithLoadAwareness(ctx, &f.groupID, "s", "claude-fable-5-1", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(3), result.Account.ID, "设备未在任何账号登记时走常规选号")
	require.Empty(t, req.NewlyRegisteredAccountIDs(), "落在不限设备的账号上不产生登记")
}

func TestDeviceAffinity_WaitsOnAffinityAccountWhenSlotsFull(t *testing.T) {
	t.Parallel()
	f := newDeviceAffinityFixture(t, nil)
	f.deviceCache.affinity[1] = struct{}{}
	f.concurrencyCache.acquireResults = map[int64]bool{1: false}
	ctx, _ := f.ctxWithDevice()

	result, err := f.svc.SelectAccountWithLoadAwareness(ctx, &f.groupID, "s", "claude-fable-5-1", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), result.Account.ID, "亲和账号槽满时应在它上面排队而不是换号")
	require.False(t, result.Acquired)
	require.NotNil(t, result.WaitPlan)
	require.Equal(t, int64(1), result.WaitPlan.AccountID)
}

func TestDeviceAffinity_FallsThroughWhenAffinityQueueIsFull(t *testing.T) {
	t.Parallel()
	f := newDeviceAffinityFixture(t, nil)
	f.deviceCache.affinity[1] = struct{}{}
	f.concurrencyCache.acquireResults = map[int64]bool{1: false}
	f.concurrencyCache.waitCounts = map[int64]int{1: f.svc.schedulingConfig().StickySessionMaxWaiting}
	ctx, _ := f.ctxWithDevice()

	result, err := f.svc.SelectAccountWithLoadAwareness(ctx, &f.groupID, "s", "claude-fable-5-1", nil, "", 0)
	require.NoError(t, err)
	require.NotEqual(t, int64(1), result.Account.ID, "亲和账号排队也满时才允许换号")
	require.True(t, result.Acquired)
}

func TestDeviceAffinity_StickySessionStillWinsOverAffinity(t *testing.T) {
	t.Parallel()
	f := newDeviceAffinityFixture(t, nil)
	f.deviceCache.affinity[1] = struct{}{}
	f.cache.sessionBindings["conv"] = 3
	ctx, _ := f.ctxWithDevice()

	result, err := f.svc.SelectAccountWithLoadAwareness(ctx, &f.groupID, "conv", "claude-fable-5-1", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(3), result.Account.ID, "已有粘性绑定的对话沿用原账号，不被亲和改写")
}

func TestDeviceAffinity_RoutingLayerPrefersAffinityAccount(t *testing.T) {
	t.Parallel()
	f := newDeviceAffinityFixture(t, map[string][]int64{"claude-fable-5-1": {3, 1}})
	f.deviceCache.affinity[1] = struct{}{}
	ctx, _ := f.ctxWithDevice()

	result, err := f.svc.SelectAccountWithLoadAwareness(ctx, &f.groupID, "s", "claude-fable-5-1", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), result.Account.ID, "模型路由候选中亲和账号应排在前面")
}

func TestPartitionByDeviceAffinity_StableAndNilSafe(t *testing.T) {
	a, b, c := &Account{ID: 1}, &Account{ID: 2}, &Account{ID: 3}
	in := []*Account{a, b, c}

	require.Equal(t, in, partitionByDeviceAffinity(in, nil))
	out := partitionByDeviceAffinity(in, map[int64]struct{}{3: {}, 2: {}})
	require.Equal(t, []int64{2, 3, 1}, []int64{out[0].ID, out[1].ID, out[2].ID})

	loads := []accountWithLoad{{account: a}, {account: b}, {account: c}}
	lo := partitionLoadsByDeviceAffinity(loads, map[int64]struct{}{3: {}})
	require.Equal(t, int64(3), lo[0].account.ID)
	require.Equal(t, int64(1), lo[1].account.ID)
}
