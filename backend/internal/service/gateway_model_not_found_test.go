//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 退役模型的本地拦截。
//
// 不拦的话，一次请求会被故障转移依次打到多个账号上，每个都往上游发一次不存在的模型名、
// 每个都被冷却 30 分钟——生产上 claude-3-5-haiku-20241022 就同时出现在账号 15/17/36 的
// 冷却记录里。记忆必须是全局的（与账号无关），否则起不到这个作用。

type fakeModelNotFoundCache struct {
	marked  map[string]time.Duration
	present map[string]bool
	getErr  error
	setErr  error
}

func newFakeModelNotFoundCache() *fakeModelNotFoundCache {
	return &fakeModelNotFoundCache{
		marked:  map[string]time.Duration{},
		present: map[string]bool{},
	}
}

func (f *fakeModelNotFoundCache) key(platform, model string) string { return platform + "|" + model }

func (f *fakeModelNotFoundCache) IsModelNotFound(_ context.Context, platform, model string) (bool, error) {
	if f.getErr != nil {
		return false, f.getErr
	}
	return f.present[f.key(platform, model)], nil
}

func (f *fakeModelNotFoundCache) MarkModelNotFound(_ context.Context, platform, model string, ttl time.Duration) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.marked[f.key(platform, model)] = ttl
	f.present[f.key(platform, model)] = true
	return nil
}

// GatewayCache 的其余方法：本测试用不到，给出足够的空实现让类型断言成立。
func (f *fakeModelNotFoundCache) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return 0, nil
}

// GetSignatureOwnerAccountID 在这些用例里不参与断言：签名归属只影响
// 转发前的 thinking 剥离判定，返回 0 表示无记录。
func (f *fakeModelNotFoundCache) GetSignatureOwnerAccountID(context.Context, int64, string) (int64, error) {
	return 0, nil
}
func (f *fakeModelNotFoundCache) SetSessionAccountID(context.Context, int64, string, int64, time.Duration) error {
	return nil
}
func (f *fakeModelNotFoundCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}
func (f *fakeModelNotFoundCache) DeleteSessionAccountID(context.Context, int64, string) error {
	return nil
}

func newModelNotFoundSvc() (*GatewayService, *fakeModelNotFoundCache) {
	cache := newFakeModelNotFoundCache()
	return &GatewayService{cache: cache}, cache
}

func TestRememberMissingModel_RecordsGloballyNotPerAccount(t *testing.T) {
	svc, cache := newModelNotFoundSvc()
	acct := &Account{ID: 15, Platform: PlatformAnthropic}

	svc.rememberMissingModel(context.Background(), acct, "claude-3-5-haiku-20241022")

	require.Equal(t, modelNotFoundTTL, cache.marked["anthropic|claude-3-5-haiku-20241022"])

	// 另一个账号的同一模型请求应当直接命中——这正是"不再喷遍账号池"的关键
	require.True(t, svc.IsKnownMissingModel(context.Background(),
		PlatformAnthropic, "claude-3-5-haiku-20241022"))
}

// 客户端拼错的名字（生产观测到 Opus-5 / claude-Opus-5）同样要能命中，且大小写无关。
func TestIsKnownMissingModel_CaseInsensitive(t *testing.T) {
	svc, _ := newModelNotFoundSvc()
	acct := &Account{ID: 4, Platform: PlatformAnthropic}

	svc.rememberMissingModel(context.Background(), acct, "Opus-5")

	require.True(t, svc.IsKnownMissingModel(context.Background(), PlatformAnthropic, "opus-5"))
	require.True(t, svc.IsKnownMissingModel(context.Background(), PlatformAnthropic, "  OPUS-5 "))
}

// 平台隔离：anthropic 上不存在不代表 openai 上不存在。
func TestIsKnownMissingModel_ScopedByPlatform(t *testing.T) {
	svc, _ := newModelNotFoundSvc()
	svc.rememberMissingModel(context.Background(),
		&Account{ID: 1, Platform: PlatformAnthropic}, "some-model")

	require.True(t, svc.IsKnownMissingModel(context.Background(), PlatformAnthropic, "some-model"))
	require.False(t, svc.IsKnownMissingModel(context.Background(), PlatformOpenAI, "some-model"))
}

// 账号自带 model_mapping 且改了名时，404 说的是"映射后那个名字在这个账号上不存在"，
// 不能推广成全局事实——别的账号可能映射到别处、或压根不映射。
func TestRememberMissingModel_SkipsWhenAccountRemapsTheModel(t *testing.T) {
	svc, cache := newModelNotFoundSvc()
	acct := &Account{ID: 7, Platform: PlatformAnthropic, Credentials: map[string]any{
		"model_mapping": map[string]any{"claude-opus-4-8": "some-internal-alias"},
	}}

	svc.rememberMissingModel(context.Background(), acct, "claude-opus-4-8")

	require.Empty(t, cache.marked, "映射改名的 404 不得globalize")
	require.False(t, svc.IsKnownMissingModel(context.Background(), PlatformAnthropic, "claude-opus-4-8"))
}

// 恒等映射（白名单模式写的就是 model→model）不算改名，应当照常记录。
func TestRememberMissingModel_IdentityMappingStillRecorded(t *testing.T) {
	svc, cache := newModelNotFoundSvc()
	acct := &Account{ID: 8, Platform: PlatformAnthropic, Credentials: map[string]any{
		"model_mapping": map[string]any{"claude-3-5-haiku-20241022": "claude-3-5-haiku-20241022"},
	}}

	svc.rememberMissingModel(context.Background(), acct, "claude-3-5-haiku-20241022")

	require.NotEmpty(t, cache.marked)
}

// 缓存故障必须失败开放：拦截是优化不是正确性要求，退回改动前行为即可。
func TestIsKnownMissingModel_FailsOpenOnCacheError(t *testing.T) {
	svc, cache := newModelNotFoundSvc()
	cache.present["anthropic|m"] = true
	cache.getErr = errors.New("redis down")

	require.False(t, svc.IsKnownMissingModel(context.Background(), PlatformAnthropic, "m"))
}

func TestModelNotFound_NoStoreIsSilentlyDisabled(t *testing.T) {
	svc := &GatewayService{} // cache 为 nil
	require.False(t, svc.IsKnownMissingModel(context.Background(), PlatformAnthropic, "m"))
	svc.rememberMissingModel(context.Background(), &Account{Platform: PlatformAnthropic}, "m") // 不得 panic
}
