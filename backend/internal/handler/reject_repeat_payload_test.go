//go:build unit

package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// fakeRepeatPayloadCache 用内存计数替代 Redis，可注入错误以验证 fail-open。
type fakeRepeatPayloadCache struct {
	mu         sync.Mutex
	counts     map[string]int64
	err        error
	calls      int
	lastWindow time.Duration
}

func newFakeRepeatPayloadCache() *fakeRepeatPayloadCache {
	return &fakeRepeatPayloadCache{counts: map[string]int64{}}
}

func (f *fakeRepeatPayloadCache) IncrementRepeatCount(_ context.Context, scope service.RepeatPayloadScope, apiKeyID int64, fingerprint string, window time.Duration) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastWindow = window
	if f.err != nil {
		return 0, f.err
	}
	key := fmt.Sprintf("%s:%d:%s", scope, apiKeyID, fingerprint)
	f.counts[key]++
	return f.counts[key], nil
}

func (f *fakeRepeatPayloadCache) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func guardConfig(mode string) *config.Config {
	cfg := &config.Config{}
	cfg.Gateway.RepeatPayloadGuard = config.RepeatPayloadGuardConfig{
		Mode:                 mode,
		MinBodyBytes:         1024,
		WindowMinutes:        30,
		MessagesThreshold:    3,
		CountTokensThreshold: 10,
	}
	return cfg
}

// largeBody 造一个超过门槛的请求体，suffix 用于制造不同指纹。
func largeBody(t *testing.T, suffix string) *service.ParsedRequest {
	t.Helper()
	body := fmt.Sprintf(`{"model":"claude-fable-5","messages":[{"role":"user","content":%q}]}`,
		strings.Repeat("x", 4096)+suffix)
	parsed, err := service.ParseGatewayRequest(service.NewRequestBodyRef([]byte(body)), "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	return parsed
}

func newGuardContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return c, rec
}

// 阈值之内放行，越过阈值才拦——第 threshold+1 次命中。
func TestRejectRepeatPayload_BlocksOnlyAfterThreshold(t *testing.T) {
	cache := newFakeRepeatPayloadCache()
	h := &GatewayHandler{cfg: guardConfig(config.RepeatPayloadGuardModeBlock), repeatPayloadCache: cache}
	parsed := largeBody(t, "")

	// MessagesThreshold = 3：前 3 次放行。
	for i := 1; i <= 3; i++ {
		c, rec := newGuardContext()
		if h.rejectRepeatPayload(c, parsed, service.RepeatPayloadScopeMessages, 83, nil) {
			t.Fatalf("第 %d 次不应被拦（阈值 3）", i)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次不应写出响应，状态 %d", i, rec.Code)
		}
	}

	c, rec := newGuardContext()
	if !h.rejectRepeatPayload(c, parsed, service.RepeatPayloadScopeMessages, 83, nil) {
		t.Fatal("第 4 次应被拦")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("状态应为 429，实际 %d", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "1800" {
		t.Fatalf("Retry-After 应为 1800，实际 %q", got)
	}
	if !strings.Contains(rec.Body.String(), "rate_limit_error") {
		t.Fatalf("响应体应为 Anthropic 限流信封，实际 %s", rec.Body.String())
	}
	if limited, ok := c.Get(service.OpsClientBusinessLimitedReasonKey); !ok || limited != service.OpsClientBusinessLimitedReasonLocalPolicyDenied {
		t.Fatalf("应标记为本地策略拒绝，避免污染运维错误率，实际 %v/%v", limited, ok)
	}
}

// 不同指纹各自计数，互不影响。
func TestRejectRepeatPayload_DistinctPayloadsCountSeparately(t *testing.T) {
	cache := newFakeRepeatPayloadCache()
	h := &GatewayHandler{cfg: guardConfig(config.RepeatPayloadGuardModeBlock), repeatPayloadCache: cache}

	for i := 0; i < 10; i++ {
		c, _ := newGuardContext()
		parsed := largeBody(t, fmt.Sprintf("-%d", i))
		if h.rejectRepeatPayload(c, parsed, service.RepeatPayloadScopeMessages, 83, nil) {
			t.Fatalf("第 %d 份不同 payload 不应被拦", i+1)
		}
	}
}

// 同一份 payload 走不同入口不得互相污染：count_tokens 有独立命名空间与更宽阈值。
func TestRejectRepeatPayload_ScopesAreIsolated(t *testing.T) {
	cache := newFakeRepeatPayloadCache()
	h := &GatewayHandler{cfg: guardConfig(config.RepeatPayloadGuardModeBlock), repeatPayloadCache: cache}
	parsed := largeBody(t, "")

	// 先把 messages 打到超阈值。
	for i := 0; i < 4; i++ {
		c, _ := newGuardContext()
		h.rejectRepeatPayload(c, parsed, service.RepeatPayloadScopeMessages, 83, nil)
	}

	// count_tokens 阈值 10，同样的 payload 仍应放行。
	for i := 1; i <= 10; i++ {
		c, _ := newGuardContext()
		if h.rejectRepeatPayload(c, parsed, service.RepeatPayloadScopeCountTokens, 83, nil) {
			t.Fatalf("count_tokens 第 %d 次不应被拦（阈值 10，且不应被 messages 计数污染）", i)
		}
	}
}

// 不同 api_key 之间不得互相影响。
func TestRejectRepeatPayload_KeysAreIsolated(t *testing.T) {
	cache := newFakeRepeatPayloadCache()
	h := &GatewayHandler{cfg: guardConfig(config.RepeatPayloadGuardModeBlock), repeatPayloadCache: cache}
	parsed := largeBody(t, "")

	for i := 0; i < 4; i++ {
		c, _ := newGuardContext()
		h.rejectRepeatPayload(c, parsed, service.RepeatPayloadScopeMessages, 83, nil)
	}

	c, _ := newGuardContext()
	if h.rejectRepeatPayload(c, parsed, service.RepeatPayloadScopeMessages, 84, nil) {
		t.Fatal("另一个 api_key 不应受影响")
	}
}

// 体积门槛：小请求根本不该访问 Redis。
//
// 这是防误伤的关键——实测 Claude Code 的探测请求一天重复上千次，
// 不设门槛第一个被打死的就是它们。
func TestRejectRepeatPayload_SkipsSmallBodiesWithoutTouchingCache(t *testing.T) {
	cache := newFakeRepeatPayloadCache()
	h := &GatewayHandler{cfg: guardConfig(config.RepeatPayloadGuardModeBlock), repeatPayloadCache: cache}

	small, err := service.ParseGatewayRequest(
		service.NewRequestBodyRef([]byte(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":1}`)), "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	for i := 0; i < 100; i++ {
		c, _ := newGuardContext()
		if h.rejectRepeatPayload(c, small, service.RepeatPayloadScopeMessages, 83, nil) {
			t.Fatalf("小请求第 %d 次被拦，体积门槛失效", i+1)
		}
	}
	if cache.callCount() != 0 {
		t.Fatalf("小请求不应访问计数器，实际调用 %d 次", cache.callCount())
	}
}

// Redis 报错必须 fail-open：这个检测是增强防护，不能成为网关的新单点。
func TestRejectRepeatPayload_FailsOpenOnCacheError(t *testing.T) {
	cache := newFakeRepeatPayloadCache()
	cache.err = errors.New("redis down")
	h := &GatewayHandler{cfg: guardConfig(config.RepeatPayloadGuardModeBlock), repeatPayloadCache: cache}
	parsed := largeBody(t, "")

	for i := 0; i < 20; i++ {
		c, rec := newGuardContext()
		if h.rejectRepeatPayload(c, parsed, service.RepeatPayloadScopeMessages, 83, nil) {
			t.Fatalf("Redis 故障时第 %d 次被拦，应当 fail-open", i+1)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("Redis 故障时不应写出响应，状态 %d", rec.Code)
		}
	}
}

// cache 未注入时同样放行。
func TestRejectRepeatPayload_FailsOpenWhenCacheMissing(t *testing.T) {
	h := &GatewayHandler{cfg: guardConfig(config.RepeatPayloadGuardModeBlock)}
	c, _ := newGuardContext()
	if h.rejectRepeatPayload(c, largeBody(t, ""), service.RepeatPayloadScopeMessages, 83, nil) {
		t.Fatal("未注入计数器时应放行")
	}
}

// 模式开关：off 完全不检测，observe 命中也放行。
func TestRejectRepeatPayload_Modes(t *testing.T) {
	tests := []struct {
		name        string
		mode        string
		wantBlocked bool
		wantCalls   int
	}{
		{name: "off 不检测", mode: config.RepeatPayloadGuardModeOff, wantBlocked: false, wantCalls: 0},
		{name: "配置笔误按 off 处理", mode: "blcok", wantBlocked: false, wantCalls: 0},
		{name: "observe 命中也放行", mode: config.RepeatPayloadGuardModeObserve, wantBlocked: false, wantCalls: 6},
		{name: "block 命中即拦", mode: config.RepeatPayloadGuardModeBlock, wantBlocked: true, wantCalls: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := newFakeRepeatPayloadCache()
			h := &GatewayHandler{cfg: guardConfig(tt.mode), repeatPayloadCache: cache}
			parsed := largeBody(t, "")

			blocked := false
			for i := 0; i < 6; i++ {
				c, _ := newGuardContext()
				if h.rejectRepeatPayload(c, parsed, service.RepeatPayloadScopeMessages, 83, nil) {
					blocked = true
				}
			}
			if blocked != tt.wantBlocked {
				t.Fatalf("拦截 = %v，期望 %v", blocked, tt.wantBlocked)
			}
			if cache.callCount() != tt.wantCalls {
				t.Fatalf("计数器调用 %d 次，期望 %d", cache.callCount(), tt.wantCalls)
			}
		})
	}
}
