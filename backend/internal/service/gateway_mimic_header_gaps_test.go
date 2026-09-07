//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 2026-09-07 上游暴露面审计（docs/UPSTREAM_EXPOSURE_AUDIT_2026-09-07.html）H-2 / H-4 / M-3：
// 三处与真实 Claude Code 出站形态的确定性差异。
//
//   H-2  真实 CLI 每个请求都带 X-Claude-Code-Session-Id（EGRESS_SPEC §6：`kt()`，
//        与 metadata.user_id.session_id 同源），伪装路径从不发。
//   H-4  重试时 X-Stainless-Retry-Count 恒为 "0"，真实 SDK 第 n 次重试发 n。
//   M-3  body 带 speed:"fast" 时真实 CLI 一定带 fast-mode beta，伪装集合里没有。

func newMimicGapContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.100 (external, cli)")
	// 客户端自带的 retry-count 必须被丢弃，不能影响我们的值
	c.Request.Header.Set("x-stainless-retry-count", "7")
	return c
}

const mimicGapSessionBody = `{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}],` +
	`"metadata":{"user_id":"{\"device_id\":\"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd\",` +
	`\"account_uuid\":\"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee\",\"session_id\":\"6fb64a13-ce54-499e-8ae1-63aca9e5d2d6\"}"}}`

// ===== H-2 =====

func TestMimicHeaders_SessionIDHeaderMatchesBodySession(t *testing.T) {
	svc := newUnifiedIdentityService()
	c := newMimicGapContext(t)

	req, wire, err := svc.buildUpstreamRequest(context.Background(), c, newUnifiedIdentityAccount(),
		[]byte(mimicGapSessionBody), "tok", "oauth", "claude-opus-4-8", true, true)
	require.NoError(t, err)

	want := ParseMetadataUserID(gjson.GetBytes(wire, "metadata.user_id").String())
	require.NotNil(t, want)
	require.NotEmpty(t, want.SessionID)
	require.Equal(t, want.SessionID, getHeaderRaw(req.Header, "X-Claude-Code-Session-Id"),
		"真实 CLI 的 X-Claude-Code-Session-Id 与 metadata.user_id.session_id 同源，出站两者必须一致")
}

func TestMimicHeaders_SessionIDHeaderAlsoOnCountTokens(t *testing.T) {
	svc := newUnifiedIdentityService()
	c := newMimicGapContext(t)

	req, wire, err := svc.buildCountTokensRequest(context.Background(), c, newUnifiedIdentityAccount(),
		[]byte(mimicGapSessionBody), "tok", "oauth", "claude-opus-4-8", true)
	require.NoError(t, err)

	want := ParseMetadataUserID(gjson.GetBytes(wire, "metadata.user_id").String())
	require.NotNil(t, want)
	require.Equal(t, want.SessionID, getHeaderRaw(req.Header, "X-Claude-Code-Session-Id"),
		"count_tokens 与 messages 打同一批账号，缺一边就是形态不一致")
}

func TestMimicHeaders_SessionIDHeaderAbsentWhenBodyHasNoSession(t *testing.T) {
	svc := newUnifiedIdentityService()
	c := newMimicGapContext(t)
	body := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`)

	req, _, err := svc.buildUpstreamRequest(context.Background(), c, newUnifiedIdentityAccount(),
		body, "tok", "oauth", "claude-opus-4-8", true, true)
	require.NoError(t, err)
	require.Empty(t, getHeaderRaw(req.Header, "X-Claude-Code-Session-Id"),
		"body 里没有 session 时不能凭空造一个：头与 body 不一致比缺头更糟")
}

func TestMimicHeaders_NonMimicPathKeepsOldSyncBehaviour(t *testing.T) {
	// api_key 账号不走伪装：没有客户端头时不应凭空注入
	svc := newUnifiedIdentityService()
	c := newMimicGapContext(t)
	acct := &Account{ID: 4243, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	req, _, err := svc.buildUpstreamRequest(context.Background(), c, acct,
		[]byte(mimicGapSessionBody), "tok", "apikey", "claude-opus-4-8", true, false)
	require.NoError(t, err)
	require.Empty(t, getHeaderRaw(req.Header, "X-Claude-Code-Session-Id"))
}

// ===== H-4（2026-09-07 本机抓包后反转）=====
//
// 真实 2.1.257 主查询客户端建 SDK 时 `maxRetries:0`（KF({maxRetries:0,...source:querySource})），
// 重试由 CLI 自己的循环发起全新 SDK 调用，因此 X-Stainless-Retry-Count 恒为 "0"——
// 对本机假 529 端点连发 3 次，全部 retry-count=0、body 逐字节相同。此前按尝试序号递增
// 的实现反而让我们不像真实客户端，故回退：任何情况下都发 "0"。
func TestMimicHeaders_RetryCountIsAlwaysZero(t *testing.T) {
	svc := newUnifiedIdentityService()
	body := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`)
	for i := 0; i < 3; i++ {
		c := newMimicGapContext(t)
		req, _, err := svc.buildUpstreamRequest(context.Background(), c, newUnifiedIdentityAccount(),
			body, "tok", "oauth", "claude-opus-4-8", true, true)
		require.NoError(t, err)
		require.Equal(t, "0", getHeaderRaw(req.Header, "x-stainless-retry-count"),
			"第 %d 次构建：真实 CLI 主查询 maxRetries=0，重试头永远是 0", i+1)
	}
}

// ===== 本机抓包对齐（docs/UPSTREAM_EXPOSURE_AUDIT_2026-09-07.html 附录）=====
//
// 样本：~/.npm-global 下 2.1.257 Bun 原生 arm64 二进制，直连本机假端点，
// OAuth（钥匙串 Max 登录）与 API-key 两种模式各抓一份，头集合一致。
func TestMimicHeaders_MatchRealClaudeCode2_1_257Capture(t *testing.T) {
	svc := newUnifiedIdentityService()
	c := newMimicGapContext(t)
	req, _, err := svc.buildUpstreamRequest(context.Background(), c, newUnifiedIdentityAccount(),
		[]byte(mimicGapSessionBody), "tok", "oauth", "claude-opus-4-8", true, true)
	require.NoError(t, err)

	want := map[string]string{
		"X-Stainless-Package-Version": "0.112.1",
		"X-Stainless-Runtime-Version": "v26.3.0",
		"X-Stainless-Runtime":         "node",
		"X-Stainless-OS":              "MacOS",
		"X-Stainless-Arch":            "arm64",
		"X-Stainless-Lang":            "js",
		"X-Stainless-Timeout":         "600",
		"X-Stainless-Retry-Count":     "0",
		"Accept":                      "application/json",
		"Accept-Encoding":             "gzip, deflate, br, zstd",
		"Connection":                  "keep-alive",
		"x-app":                       "cli",
		"anthropic-version":           "2023-06-01",
		"anthropic-dangerous-direct-browser-access": "true",
	}
	for k, v := range want {
		require.Equal(t, v, getHeaderRaw(req.Header, k), "header %s", k)
		// 线上大小写也要一致：真实客户端就是这个写法
		_, rawOK := req.Header[k]
		require.True(t, rawOK, "header %s 的 wire casing 应为 %q", k, k)
	}
	require.Empty(t, getHeaderRaw(req.Header, "x-client-request-id"),
		"真实 2.1.257 不发 x-client-request-id（defaultHeaders 与 SDK 核心头里都没有），多一个头就是多一个指纹")
	require.True(t, strings.HasPrefix(getHeaderRaw(req.Header, "User-Agent"), "claude-cli/"+claude.CLIVersion()+" (external, cli)"))
}

// ===== M-3 =====

func TestMimicBetas_FastModeFollowsBodySpeed(t *testing.T) {
	svc := newUnifiedIdentityService()

	betaOf := func(t *testing.T, body string) []string {
		t.Helper()
		c := newMimicGapContext(t)
		req, _, err := svc.buildUpstreamRequest(context.Background(), c, newUnifiedIdentityAccount(),
			[]byte(body), "tok", "oauth", "claude-opus-4-8", true, true)
		require.NoError(t, err)
		return strings.Split(getHeaderRaw(req.Header, "anthropic-beta"), ",")
	}

	t.Run("speed:fast → 带 fast-mode，且排在最后", func(t *testing.T) {
		got := betaOf(t, `{"model":"claude-opus-4-8","speed":"fast","messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, claude.BetaFastMode, got[len(got)-1])
		require.Len(t, got, len(claude.FullClaudeCodeMimicryBetas())+1)
	})
	t.Run("无 speed → 不带", func(t *testing.T) {
		got := betaOf(t, `{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`)
		require.NotContains(t, got, claude.BetaFastMode)
	})
	t.Run("speed:standard → 不带", func(t *testing.T) {
		got := betaOf(t, `{"model":"claude-opus-4-8","speed":"standard","messages":[{"role":"user","content":"hi"}]}`)
		require.NotContains(t, got, claude.BetaFastMode)
	})
	t.Run("speed 字段本身仍透传上游", func(t *testing.T) {
		c := newMimicGapContext(t)
		_, wire, err := svc.buildUpstreamRequest(context.Background(), c, newUnifiedIdentityAccount(),
			[]byte(`{"model":"claude-opus-4-8","speed":"fast","messages":[{"role":"user","content":"hi"}]}`),
			"tok", "oauth", "claude-opus-4-8", true, true)
		require.NoError(t, err)
		require.Equal(t, "fast", gjson.GetBytes(wire, "speed").String(), "保持原生透传，补 beta 而不是剥字段")
	})
}

func TestMimicryBetasForRequest_FastModeOrdering(t *testing.T) {
	require.Equal(t, claude.FullClaudeCodeMimicryBetas(), claude.MimicryBetasForRequest("", false),
		"不带 fast 时与原固定集合完全一致，不能引入回归")
	got := claude.MimicryBetasForRequest("context-1m-2025-08-07", true)
	require.Equal(t, claude.BetaFastMode, got[len(got)-1])
	require.Contains(t, got, claude.BetaContext1M)
	require.Len(t, got, len(claude.FullClaudeCodeMimicryBetas())+2)
}
