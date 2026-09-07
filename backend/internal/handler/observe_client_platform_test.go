//go:build unit

package handler

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// fakePlatformStatsCache 同时实现 RepeatPayloadCache（handler 字段类型）与
// ClientPlatformStatsCache（观察期按断言取用）。
type fakePlatformStatsCache struct {
	incr     map[string]int
	sessions map[string]string
}

func newFakePlatformStatsCache() *fakePlatformStatsCache {
	return &fakePlatformStatsCache{incr: map[string]int{}, sessions: map[string]string{}}
}

func (f *fakePlatformStatsCache) IncrementRepeatCount(context.Context, service.RepeatPayloadScope, int64, string, time.Duration) (int64, error) {
	return 0, nil
}

func (f *fakePlatformStatsCache) IncrClientPlatformStat(_ context.Context, day, field string) error {
	f.incr[day+"/"+field]++
	return nil
}

func (f *fakePlatformStatsCache) IncrClientIdentityStat(_ context.Context, day, field string) error {
	f.incr[day+"/identity/"+field]++
	return nil
}

func (f *fakePlatformStatsCache) ReadClientPlatformStats(_ context.Context, _ string) (map[string]int64, error) {
	return map[string]int64{}, nil
}

func (f *fakePlatformStatsCache) RecordSessionClientPlatform(_ context.Context, sessionHash, platform string, _ time.Duration) (string, error) {
	if prev, ok := f.sessions[sessionHash]; ok {
		return prev, nil
	}
	f.sessions[sessionHash] = platform
	return "", nil
}

func newPlatformTestContext(t *testing.T, os, arch string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	if os != "" {
		c.Request.Header.Set("X-Stainless-OS", os)
	}
	if arch != "" {
		c.Request.Header.Set("X-Stainless-Arch", arch)
	}
	return c
}

func TestObserveClientPlatform_CountsByPlatformSourceAndClient(t *testing.T) {
	cache := newFakePlatformStatsCache()
	h := &GatewayHandler{repeatPayloadCache: cache}
	day := time.Now().UTC().Format("2006-01-02")

	body := []byte(`{"system":"<env>\nPlatform: win32\n</env>"}`)
	winCtx := newPlatformTestContext(t, "Windows", "x64")
	winCtx.Request.Header.Set("User-Agent", "claude-cli/2.1.263 (external, cli)")
	winCtx.Request.Header.Set("X-Stainless-Runtime", "node")
	winCtx.Request.Header.Set("X-Stainless-Runtime-Version", "v26.3.0")
	winCtx.Request.Header.Set("X-Stainless-Package-Version", "0.112.1")
	h.observeClientPlatform(winCtx, body, "sess-a", true, zap.NewNop())
	h.observeClientPlatform(newPlatformTestContext(t, "", ""), []byte(`{"system":"plain"}`), "sess-b", false, zap.NewNop())
	// 非 CC 但头里有 OS：平台计数记，身份组合不记
	h.observeClientPlatform(newPlatformTestContext(t, "Linux", "x64"), []byte(`{}`), "sess-c", false, zap.NewNop())

	require.Equal(t, 1, cache.incr[day+"/windows-x64|env_block|cc"])
	require.Equal(t, 1, cache.incr[day+"/unknown|none|other"])
	require.Equal(t, 1, cache.incr[day+"/identity/windows-x64|node|v26.3.0|0.112.1|2.1.263"], "真 CC 的头身份组合要记下来")
	require.Equal(t, 1, cache.incr[day+"/linux-x64|header|other"])
	for k := range cache.incr {
		require.NotContains(t, k, "/identity/linux-x64", "非 CC 客户端不记身份组合")
	}
	require.Equal(t, "windows-x64", cache.sessions["sess-a"])
	_, recorded := cache.sessions["sess-b"]
	require.False(t, recorded, "未知平台不记会话")
}

func TestObserveClientPlatform_CountsSessionFlip(t *testing.T) {
	cache := newFakePlatformStatsCache()
	h := &GatewayHandler{repeatPayloadCache: cache}
	day := time.Now().UTC().Format("2006-01-02")

	h.observeClientPlatform(newPlatformTestContext(t, "MacOS", "arm64"), []byte(`{}`), "sess-x", true, zap.NewNop())
	h.observeClientPlatform(newPlatformTestContext(t, "MacOS", "arm64"), []byte(`{}`), "sess-x", true, zap.NewNop())
	require.Equal(t, 0, cache.incr[day+"/flip|macos-arm64->macos-arm64"], "同平台不算 flip")

	h.observeClientPlatform(newPlatformTestContext(t, "Linux", "x64"), []byte(`{}`), "sess-x", true, zap.NewNop())
	require.Equal(t, 1, cache.incr[day+"/flip|macos-arm64->linux-x64"])
	require.Equal(t, "macos-arm64", cache.sessions["sess-x"], "首次判定不被覆盖")
}

// 判定结果要落到请求上下文供调度层读取；会话已有首次判定时以首次为准。
func TestObserveClientPlatform_WritesPlatformIntoRequestContext(t *testing.T) {
	cache := newFakePlatformStatsCache()
	h := &GatewayHandler{repeatPayloadCache: cache}

	c1 := newPlatformTestContext(t, "MacOS", "arm64")
	h.observeClientPlatform(c1, []byte(`{}`), "sess-ctx", true, zap.NewNop())
	require.Equal(t, service.ClientPlatformMacOSArm64, service.ClientPlatformFromContext(c1.Request.Context()))

	// 同一会话后来判成 linux：上下文里仍是首次的 macos（会话内不重判）
	c2 := newPlatformTestContext(t, "Linux", "x64")
	h.observeClientPlatform(c2, []byte(`{}`), "sess-ctx", true, zap.NewNop())
	require.Equal(t, service.ClientPlatformMacOSArm64, service.ClientPlatformFromContext(c2.Request.Context()))

	// 未知平台不写上下文
	c3 := newPlatformTestContext(t, "", "")
	h.observeClientPlatform(c3, []byte(`{}`), "sess-none", false, zap.NewNop())
	require.Equal(t, service.ClientPlatformUnknown, service.ClientPlatformFromContext(c3.Request.Context()))

	// 没有计数存储时也要写上下文（分池不依赖观察计数是否可用）
	h2 := &GatewayHandler{}
	c4 := newPlatformTestContext(t, "Windows", "x64")
	h2.observeClientPlatform(c4, []byte(`{}`), "sess-nocache", true, zap.NewNop())
	require.Equal(t, service.ClientPlatformWindowsX64, service.ClientPlatformFromContext(c4.Request.Context()))
}

func TestObserveClientPlatform_NoCacheOrNilContextIsSafe(t *testing.T) {
	h := &GatewayHandler{}
	require.NotPanics(t, func() {
		h.observeClientPlatform(newPlatformTestContext(t, "MacOS", "arm64"), []byte(`{}`), "s", true, zap.NewNop())
		h.observeClientPlatform(nil, nil, "", false, nil)
	})
}
