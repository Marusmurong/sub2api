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

// ===== H-4 =====

func TestMimicHeaders_RetryCountFollowsUpstreamAttempt(t *testing.T) {
	svc := newUnifiedIdentityService()
	body := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`)

	cases := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"未标注尝试序号 → 首次请求", context.Background(), "0"},
		{"第 1 次尝试", WithUpstreamAttempt(context.Background(), 1), "0"},
		{"第 3 次尝试 → 第 2 次重试", WithUpstreamAttempt(context.Background(), 3), "2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newMimicGapContext(t)
			req, _, err := svc.buildUpstreamRequest(tc.ctx, c, newUnifiedIdentityAccount(),
				body, "tok", "oauth", "claude-opus-4-8", true, true)
			require.NoError(t, err)
			require.Equal(t, tc.want, getHeaderRaw(req.Header, "x-stainless-retry-count"))
		})
	}
}

// 循环内的补救重发也必须计数：五处出站都经 nextUpstreamAttemptCtx，序号按调用递增，
// 每个派生 ctx 读回的都是自己那一次的序号（而不是最后一次）。
func TestNextUpstreamAttemptCtx_IncrementsPerWireRequest(t *testing.T) {
	base := context.Background()
	n := 0
	c1 := nextUpstreamAttemptCtx(base, &n)
	c2 := nextUpstreamAttemptCtx(base, &n)
	c3 := nextUpstreamAttemptCtx(base, &n)
	require.Equal(t, 3, n)
	require.Equal(t, 1, upstreamAttemptFromContext(c1))
	require.Equal(t, 2, upstreamAttemptFromContext(c2))
	require.Equal(t, 3, upstreamAttemptFromContext(c3))
	require.Equal(t, 1, upstreamAttemptFromContext(base), "未打标的 ctx 视为首次")
	require.Equal(t, 1, upstreamAttemptFromContext(nextUpstreamAttemptCtx(base, nil)), "nil 计数器退化为首次，不能 panic")
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
