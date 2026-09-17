//go:build unit

package service

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	testDeviceHex   = "464DA8518A6CB7F7C307C0BEF761FD9041887457D8A2F466A818F59934C43005"
	testDeviceLower = "464da8518a6cb7f7c307c0bef761fd9041887457d8a2f466a818f59934c43005"
	testSessionUUID = "3e938e51-c7b0-43ab-90f4-253c7503bc5e"
)

// deviceLimitCacheStub 记录调用并按配置返回，用于验证调度侧设备限制逻辑。
type deviceLimitCacheStub struct {
	mu           sync.Mutex
	allowed      bool
	isNew        bool
	registerErr  error
	registered   []int64
	unregistered map[int64][]string
	affinity     map[int64]struct{} // ActiveDeviceAccounts 返回的"已登记本设备"账号
}

func newDeviceLimitCacheStub(allowed, isNew bool) *deviceLimitCacheStub {
	return &deviceLimitCacheStub{allowed: allowed, isNew: isNew, unregistered: map[int64][]string{}, affinity: map[int64]struct{}{}}
}

func (s *deviceLimitCacheStub) RegisterDevice(_ context.Context, accountID int64, _ string, _ int, _ time.Duration) (bool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registered = append(s.registered, accountID)
	if s.registerErr != nil {
		return false, false, s.registerErr
	}
	return s.allowed, s.isNew, nil
}

func (s *deviceLimitCacheStub) UnregisterDevice(_ context.Context, accountID int64, deviceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unregistered[accountID] = append(s.unregistered[accountID], deviceID)
	return nil
}

func (s *deviceLimitCacheStub) GetActiveDeviceCountBatch(context.Context, []int64, map[int64]time.Duration) (map[int64]int, error) {
	return map[int64]int{}, nil
}

func (s *deviceLimitCacheStub) ClearDevices(context.Context, int64) error { return nil }

func (s *deviceLimitCacheStub) ActiveDeviceAccounts(_ context.Context, _ string, accountIDs []int64, _ map[int64]time.Duration) (map[int64]struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int64]struct{})
	for _, id := range accountIDs {
		if _, ok := s.affinity[id]; ok {
			out[id] = struct{}{}
		}
	}
	return out, nil
}

// sessionRegisterStub 只实现 RegisterSession，用于驱动会话被拒的分支。
type sessionRegisterStub struct {
	SessionLimitCache
	allowed bool
}

func (s *sessionRegisterStub) RegisterSession(context.Context, int64, string, int, time.Duration) (bool, error) {
	return s.allowed, nil
}

func newDeviceLimitTestAccount(maxDevices int) *Account {
	return &Account{
		ID:       42,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"max_devices": maxDevices},
	}
}

func legacyUserID(device string) string {
	return "user_" + device + "_account_00000000-0000-0000-0000-000000000000_session_" + testSessionUUID
}

func ctxWithDevice(t *testing.T, device string) (context.Context, *DeviceLimitRequest) {
	t.Helper()
	req := NewDeviceLimitRequest(legacyUserID(device))
	return WithDeviceLimitRequest(context.Background(), req), req
}

func TestExtractDeviceLimitID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"legacy_lowercases", legacyUserID(testDeviceHex), testDeviceLower},
		{"json_valid", `{"device_id":"` + testDeviceLower + `","account_uuid":"","session_id":"` + testSessionUUID + `"}`, testDeviceLower},
		{"json_uppercase_normalized", `{"device_id":"` + testDeviceHex + `","account_uuid":"","session_id":"` + testSessionUUID + `"}`, testDeviceLower},
		{"json_non_hex_rejected", `{"device_id":"my-client-id","account_uuid":"","session_id":"` + testSessionUUID + `"}`, ""},
		{"json_short_rejected", `{"device_id":"` + testDeviceLower[:63] + `","account_uuid":"","session_id":"` + testSessionUUID + `"}`, ""},
		{"empty", "", ""},
		{"garbage", "not-a-user-id", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, ExtractDeviceLimitID(tc.in))
		})
	}
}

func TestAccountDeviceLimitGetters(t *testing.T) {
	var nilAcc *Account
	require.Equal(t, 0, nilAcc.GetMaxDevices())
	require.Equal(t, DefaultDeviceWindowMinutes, nilAcc.GetDeviceWindowMinutes())

	cases := []struct {
		name       string
		extra      map[string]any
		wantMax    int
		wantWindow int
	}{
		{"nil_extra", nil, 0, DefaultDeviceWindowMinutes},
		{"unset", map[string]any{}, 0, DefaultDeviceWindowMinutes},
		{"int_values", map[string]any{"max_devices": 2, "device_window_minutes": 60}, 2, 60},
		{"json_float_values", map[string]any{"max_devices": float64(3), "device_window_minutes": float64(120)}, 3, 120},
		{"string_values", map[string]any{"max_devices": "1", "device_window_minutes": "30"}, 1, 30},
		{"negative_max_disabled", map[string]any{"max_devices": -1}, 0, DefaultDeviceWindowMinutes},
		{"zero_window_falls_back", map[string]any{"max_devices": 1, "device_window_minutes": 0}, 1, DefaultDeviceWindowMinutes},
		{"garbage_window_falls_back", map[string]any{"max_devices": 1, "device_window_minutes": "abc"}, 1, DefaultDeviceWindowMinutes},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			acc := &Account{Extra: tc.extra}
			require.Equal(t, tc.wantMax, acc.GetMaxDevices())
			require.Equal(t, tc.wantWindow, acc.GetDeviceWindowMinutes())
		})
	}
}

func TestCheckAndRegisterDevice_PassThroughWithoutTouchingCache(t *testing.T) {
	ctxDevice, _ := ctxWithDevice(t, testDeviceHex)
	ctxNoDevice, _ := ctxWithDevice(t, "not-hex")

	apiKeyAcc := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Extra: map[string]any{"max_devices": 1}}
	disabledAcc := &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	enabledAcc := newDeviceLimitTestAccount(1)

	cases := []struct {
		name    string
		ctx     context.Context
		account *Account
		cache   DeviceLimitCache
	}{
		{"api_key_account", ctxDevice, apiKeyAcc, newDeviceLimitCacheStub(false, false)},
		{"limit_disabled", ctxDevice, disabledAcc, newDeviceLimitCacheStub(false, false)},
		{"no_request_context", context.Background(), enabledAcc, newDeviceLimitCacheStub(false, false)},
		{"invalid_device_id", ctxNoDevice, enabledAcc, newDeviceLimitCacheStub(false, false)},
		{"nil_account", ctxDevice, nil, newDeviceLimitCacheStub(false, false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &GatewayService{deviceLimitCache: tc.cache}
			require.True(t, svc.checkAndRegisterDevice(tc.ctx, tc.account))
			require.Empty(t, tc.cache.(*deviceLimitCacheStub).registered, "不适用场景不应触碰缓存")
		})
	}

	t.Run("nil_cache", func(t *testing.T) {
		svc := &GatewayService{}
		require.True(t, svc.checkAndRegisterDevice(ctxDevice, enabledAcc))
	})
}

func TestCheckAndRegisterDevice_RejectsNewDeviceWhenFull(t *testing.T) {
	ctx, req := ctxWithDevice(t, testDeviceHex)
	cache := newDeviceLimitCacheStub(false, false)
	svc := &GatewayService{deviceLimitCache: cache}

	require.False(t, svc.checkAndRegisterDevice(ctx, newDeviceLimitTestAccount(1)))
	require.Equal(t, []int64{42}, cache.registered)
	require.Empty(t, req.NewlyRegisteredAccountIDs())
}

func TestCheckAndRegisterDevice_FailOpenOnCacheError(t *testing.T) {
	ctx, req := ctxWithDevice(t, testDeviceHex)
	cache := newDeviceLimitCacheStub(false, false)
	cache.registerErr = errors.New("redis down")
	svc := &GatewayService{deviceLimitCache: cache}

	require.True(t, svc.checkAndRegisterDevice(ctx, newDeviceLimitTestAccount(1)))
	require.Empty(t, req.NewlyRegisteredAccountIDs())
}

func TestCheckAndRegisterDevice_TracksOnlyNewRegistrations(t *testing.T) {
	t.Run("new_device_tracked", func(t *testing.T) {
		ctx, req := ctxWithDevice(t, testDeviceHex)
		svc := &GatewayService{deviceLimitCache: newDeviceLimitCacheStub(true, true)}
		require.True(t, svc.checkAndRegisterDevice(ctx, newDeviceLimitTestAccount(1)))
		require.Equal(t, []int64{42}, req.NewlyRegisteredAccountIDs())
	})
	t.Run("existing_device_not_tracked", func(t *testing.T) {
		ctx, req := ctxWithDevice(t, testDeviceHex)
		svc := &GatewayService{deviceLimitCache: newDeviceLimitCacheStub(true, false)}
		require.True(t, svc.checkAndRegisterDevice(ctx, newDeviceLimitTestAccount(1)))
		require.Empty(t, req.NewlyRegisteredAccountIDs())
	})
}

// 设备登记成功但会话被拒：必须撤销本次新登记的设备，否则账号被一个从未服务的设备白占整个窗口。
func TestCheckAndRegisterSession_RollsBackNewDeviceWhenSessionRejected(t *testing.T) {
	ctx, req := ctxWithDevice(t, testDeviceHex)
	deviceCache := newDeviceLimitCacheStub(true, true)
	svc := &GatewayService{
		deviceLimitCache:  deviceCache,
		sessionLimitCache: &sessionRegisterStub{allowed: false},
	}
	acc := newDeviceLimitTestAccount(1)
	acc.Extra["max_sessions"] = 1

	require.False(t, svc.checkAndRegisterSession(ctx, acc, "session-hash"))
	require.Equal(t, []string{testDeviceLower}, deviceCache.unregistered[42], "新登记的设备应被撤销")
	require.Empty(t, req.NewlyRegisteredAccountIDs())
}

// 设备原本就已登记（isNew=false）时，会话被拒不能把正在使用的设备踢掉。
func TestCheckAndRegisterSession_KeepsExistingDeviceWhenSessionRejected(t *testing.T) {
	ctx, _ := ctxWithDevice(t, testDeviceHex)
	deviceCache := newDeviceLimitCacheStub(true, false)
	svc := &GatewayService{
		deviceLimitCache:  deviceCache,
		sessionLimitCache: &sessionRegisterStub{allowed: false},
	}
	acc := newDeviceLimitTestAccount(1)
	acc.Extra["max_sessions"] = 1

	require.False(t, svc.checkAndRegisterSession(ctx, acc, "session-hash"))
	require.Empty(t, deviceCache.unregistered)
}

func TestCheckAndRegisterSession_DeviceRejectionShortCircuitsSession(t *testing.T) {
	ctx, _ := ctxWithDevice(t, testDeviceHex)
	deviceCache := newDeviceLimitCacheStub(false, false)
	svc := &GatewayService{
		deviceLimitCache:  deviceCache,
		sessionLimitCache: &sessionRegisterStub{allowed: true},
	}
	acc := newDeviceLimitTestAccount(1)
	acc.Extra["max_sessions"] = 1

	require.False(t, svc.checkAndRegisterSession(ctx, acc, "session-hash"))
	require.Empty(t, deviceCache.unregistered)
}

func TestCheckAndRegisterSession_BothPass(t *testing.T) {
	ctx, req := ctxWithDevice(t, testDeviceHex)
	svc := &GatewayService{
		deviceLimitCache:  newDeviceLimitCacheStub(true, true),
		sessionLimitCache: &sessionRegisterStub{allowed: true},
	}
	acc := newDeviceLimitTestAccount(1)
	acc.Extra["max_sessions"] = 1

	require.True(t, svc.checkAndRegisterSession(ctx, acc, "session-hash"))
	require.Equal(t, []int64{42}, req.NewlyRegisteredAccountIDs())
}

func TestReleaseUnservedDeviceRegistrations_ReleasesOnlyUnservedNewOnes(t *testing.T) {
	ctx, req := ctxWithDevice(t, testDeviceHex)
	cache := newDeviceLimitCacheStub(true, true)
	svc := &GatewayService{deviceLimitCache: cache}
	for _, id := range []int64{1, 2, 3} {
		acc := newDeviceLimitTestAccount(1)
		acc.ID = id
		require.True(t, svc.checkAndRegisterDevice(ctx, acc))
	}

	svc.ReleaseUnservedDeviceRegistrations(context.Background(), req, 2)

	released := make([]int64, 0, len(cache.unregistered))
	for id := range cache.unregistered {
		released = append(released, id)
	}
	sort.Slice(released, func(i, j int) bool { return released[i] < released[j] })
	require.Equal(t, []int64{1, 3}, released, "只释放未服务的账号")
	require.Equal(t, []int64{2}, req.NewlyRegisteredAccountIDs(), "服务中的账号保留登记")

	// 幂等：再次调用不重复释放
	svc.ReleaseUnservedDeviceRegistrations(context.Background(), req, 2)
	require.Len(t, cache.unregistered[1], 1)
	require.Len(t, cache.unregistered[3], 1)

	// 请求最终失败：servedAccountID=0 释放剩余全部
	svc.ReleaseUnservedDeviceRegistrations(context.Background(), req, 0)
	require.Len(t, cache.unregistered[2], 1)
	require.Empty(t, req.NewlyRegisteredAccountIDs())
}

func TestReleaseAccountDeviceRegistration_OnlyNewAndIdempotent(t *testing.T) {
	ctx, req := ctxWithDevice(t, testDeviceHex)
	cache := newDeviceLimitCacheStub(true, true)
	svc := &GatewayService{deviceLimitCache: cache}
	require.True(t, svc.checkAndRegisterDevice(ctx, newDeviceLimitTestAccount(1)))

	svc.ReleaseAccountDeviceRegistration(context.Background(), req, 42)
	svc.ReleaseAccountDeviceRegistration(context.Background(), req, 42)
	svc.ReleaseAccountDeviceRegistration(context.Background(), req, 99)

	require.Equal(t, []string{testDeviceLower}, cache.unregistered[42])
	require.Empty(t, cache.unregistered[99])
}

func TestDeviceLimitRequest_NilSafety(t *testing.T) {
	var req *DeviceLimitRequest
	require.Equal(t, "", req.DeviceID())
	require.Nil(t, req.NewlyRegisteredAccountIDs())

	ctx := WithDeviceLimitRequest(context.Background(), nil)
	require.Nil(t, deviceLimitRequestFromContext(ctx))
	require.Nil(t, deviceLimitRequestFromContext(nil))

	svc := &GatewayService{deviceLimitCache: newDeviceLimitCacheStub(true, true)}
	svc.ReleaseUnservedDeviceRegistrations(context.Background(), nil, 0)
	svc.ReleaseAccountDeviceRegistration(context.Background(), nil, 1)

	var nilSvc *GatewayService
	require.True(t, nilSvc.checkAndRegisterDevice(context.Background(), newDeviceLimitTestAccount(1)))
}
