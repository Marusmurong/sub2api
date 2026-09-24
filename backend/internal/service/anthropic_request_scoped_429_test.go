//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 2026-09-24 生产实测（account 175, req_011CfN3vdPBLd7U31ye1LzX2）：一次 2.7MB 的长上下文
// 请求被拒，账号 5h 用量 4%、7d 用量 26%，却被按聚合 reset 头锁到 10-01，限流 6 天多。
const longContextCredits429Body = `{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for long context requests."},"request_id":"req_011CfN3vdPBLd7U31ye1LzX2"}`

func longContextCredits429Headers(reset time.Time) http.Header {
	h := http.Header{}
	h.Set("X-Should-Retry", "false")
	h.Set("Anthropic-Ratelimit-Unified-Overage-Disabled-Reason", "org_level_disabled")
	h.Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(reset.Unix(), 10))
	return h
}

func TestIsAnthropicRequestScoped429(t *testing.T) {
	anthropic := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	reset := time.Now().Add(6 * 24 * time.Hour)

	rejected5h := longContextCredits429Headers(reset)
	rejected5h.Set("anthropic-ratelimit-unified-5h-status", "rejected")
	rejected5h.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(time.Now().Add(2*time.Hour).Unix(), 10))

	cases := []struct {
		name    string
		account *Account
		status  int
		headers http.Header
		body    string
		want    bool
	}{
		{name: "生产实测形态：长上下文需额度", account: anthropic, status: 429, headers: longContextCredits429Headers(reset), body: longContextCredits429Body, want: true},
		{name: "消息大小写不敏感", account: anthropic, status: 429, headers: http.Header{}, body: `{"type":"error","error":{"type":"rate_limit_error","message":"USAGE CREDITS ARE REQUIRED FOR LONG CONTEXT requests"}}`, want: true},
		{name: "共享窗口明确 rejected 时仍是真限流", account: anthropic, status: 429, headers: rejected5h, body: longContextCredits429Body, want: false},
		{name: "模型级 credits_required 不在此列（既有语义）", account: anthropic, status: 429, headers: http.Header{}, body: `{"type":"error","error":{"details":{"error_code":"credits_required"},"message":"Usage credits are required for this model."}}`, want: false},
		{name: "普通 429", account: anthropic, status: 429, headers: http.Header{}, body: `{"type":"error","error":{"type":"rate_limit_error","message":"Error"}}`, want: false},
		{name: "非 429", account: anthropic, status: 400, headers: http.Header{}, body: longContextCredits429Body, want: false},
		{name: "非 Anthropic 平台", account: &Account{Platform: PlatformOpenAI}, status: 429, headers: http.Header{}, body: longContextCredits429Body, want: false},
		{name: "nil 账号", account: nil, status: 429, headers: http.Header{}, body: longContextCredits429Body, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isAnthropicRequestScoped429(tc.account, tc.status, tc.headers, []byte(tc.body))
			require.Equal(t, tc.want, got)
		})
	}
}

func TestHandleUpstreamError_AnthropicLongContextCreditsDoesNotCoolAccount(t *testing.T) {
	repo := &anthropicWindowLimitRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{ID: 175, Type: AccountTypeOAuth, Platform: PlatformAnthropic}
	headers := longContextCredits429Headers(time.Now().Add(6 * 24 * time.Hour))

	shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, headers, []byte(longContextCredits429Body), "claude-sonnet-4-6")

	require.False(t, shouldDisable)
	require.Zero(t, repo.rateLimitCalls, "请求级拒绝不得把整个账号限流到计费周期重置点")
	require.Zero(t, repo.sessionWindowCalls, "请求级拒绝不得改写 session window")
	require.Zero(t, repo.tempUnschedCalls)
	require.Zero(t, repo.modelRateLimitCalls)
}

func TestHandleUpstreamError_AnthropicLongContextCreditsWithRejectedWindowStillLimits(t *testing.T) {
	repo := &anthropicWindowLimitRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{ID: 175, Type: AccountTypeOAuth, Platform: PlatformAnthropic}
	reset5h := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	headers := longContextCredits429Headers(time.Now().Add(6 * 24 * time.Hour))
	headers.Set("anthropic-ratelimit-unified-5h-status", "rejected")
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "1.0")
	headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(reset5h.Unix(), 10))

	svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, headers, []byte(longContextCredits429Body), "claude-sonnet-4-6")

	require.Equal(t, 1, repo.rateLimitCalls, "窗口真耗尽时必须照常限流")
	require.Equal(t, reset5h, repo.lastRateLimitReset)
}

func TestGatewayService_Forward_AnthropicLongContextCreditsReturnsToClientWithoutFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)

	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     longContextCredits429Headers(time.Now().Add(6 * 24 * time.Hour)),
		Body:       io.NopCloser(strings.NewReader(longContextCredits429Body)),
	}}
	repo := &anthropicWindowLimitRepo{}
	svc := newForwardPartialUsageServiceForTest(upstream)
	svc.rateLimitService = NewRateLimitService(repo, nil, nil, nil, nil)
	account := newAnthropicOAuthAccountForPartialUsageTest()

	result, err := svc.Forward(context.Background(), c, account, parsed)

	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "换号也一样被拒，不得 failover")
	require.Zero(t, repo.rateLimitCalls, "不得冷却账号")
	require.Zero(t, repo.sessionWindowCalls)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, "false", rec.Header().Get("X-Should-Retry"), "告诉客户端别原样重试")
	require.Contains(t, rec.Body.String(), "Usage credits are required for long context requests.")
	require.Contains(t, rec.Body.String(), `"rate_limit_error"`)
}
