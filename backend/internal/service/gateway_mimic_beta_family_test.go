//go:build unit

package service

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
)

// 期望值来自 2026-09-07 本机真实 Claude Code 2.1.263 抓包（三族各一次）。
// 这里测的是网关出口的最终字符串：模板取族、账号门控、fast-mode、白名单透传。
func TestMimicBeta_PerModelFamilyMatchesRealCLI(t *testing.T) {
	s := &GatewayService{}
	opus := "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,per-turn-control-2026-07-01,advisor-tool-2026-03-01,effort-2025-11-24,extended-cache-ttl-2025-04-11"
	sonnet := "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,advisor-tool-2026-03-01,effort-2025-11-24,extended-cache-ttl-2025-04-11"
	haiku := "oauth-2025-04-20,interleaved-thinking-2025-05-14,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,claude-code-20250219,advisor-tool-2026-03-01,extended-cache-ttl-2025-04-11"

	cases := []struct {
		name  string
		model string
		hdr   http.Header
		body  string
		gates claude.MimicryBetaGates
		want  string
	}{
		{"fable 走 opus 族，门控默认关", "claude-fable-5-1", nil, `{}`, claude.MimicryBetaGates{}, opus},
		{"opus 账号开 fallback-credit → 与抓包完全一致", "claude-opus-5", nil, `{}`, claude.MimicryBetaGates{FallbackCredit: true},
			strings.Replace(opus, "effort-2025-11-24,", "effort-2025-11-24,fallback-credit-2026-06-01,", 1)},
		{"sonnet 10 个", "claude-sonnet-5", nil, `{}`, claude.MimicryBetaGates{}, sonnet},
		{"haiku 8 个，claude-code 在第 6 位", "claude-haiku-4-5", nil, `{}`, claude.MimicryBetaGates{}, haiku},
		{"客户端头里的非白名单 beta 全部丢弃", "claude-sonnet-5", http.Header{"Anthropic-Beta": {"foo-2026,redact-thinking-2026-02-12"}}, `{}`, claude.MimicryBetaGates{}, sonnet},
		{"speed:fast 追加 fast-mode 在末尾", "claude-sonnet-5", nil, `{"speed":"fast"}`, claude.MimicryBetaGates{}, sonnet + ",fast-mode-2026-02-01"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := s.computeBaseAnthropicBetaWithGates("oauth", true, tc.model, tc.hdr, []byte(tc.body), nil, tc.gates)
			require.True(t, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestAccount_MimicryBetaGates(t *testing.T) {
	oauth := func(extra map[string]any) *Account {
		return &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: extra}
	}
	require.Equal(t, claude.MimicryBetaGates{}, oauth(nil).MimicryBetaGates(), "无 extra → 全关")
	require.Equal(t, claude.MimicryBetaGates{FallbackCredit: true},
		oauth(map[string]any{"mimic_beta_fallback_credit": true}).MimicryBetaGates())
	require.Equal(t, claude.MimicryBetaGates{Context1M: true},
		oauth(map[string]any{"mimic_beta_context_1m": "true"}).MimicryBetaGates(), "字符串 true 也算开")
	require.Equal(t, claude.MimicryBetaGates{},
		oauth(map[string]any{"mimic_beta_context_1m": "yes"}).MimicryBetaGates(), "其它字符串不算开")
	apiKey := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Extra: map[string]any{"mimic_beta_fallback_credit": true}}
	require.Equal(t, claude.MimicryBetaGates{}, apiKey.MimicryBetaGates(), "非 OAuth 账号忽略")
}

// 账号门控打开 fallback-credit 后，beta 对称的 sanitize 会保留 fallback_credit_token；
// 伪装路径必须另行无条件剥掉这两个字段。
func TestStripFallbackFieldsForMimic(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","fallbacks":"default","fallback_credit_token":"tok","messages":[]}`)
	beta := "fallback-credit-2026-06-01,oauth-2025-04-20"
	afterSanitize, _ := sanitizeAnthropicBodyForBetaTokens(body, beta)
	require.Contains(t, string(afterSanitize), "fallback_credit_token", "前置条件：beta 对称 sanitize 会保留它")

	out, changed := stripFallbackFieldsForMimic(afterSanitize)
	require.True(t, changed)
	require.NotContains(t, string(out), "fallback_credit_token")
	require.NotContains(t, string(out), `"fallbacks"`)
	require.Contains(t, string(out), `"model":"claude-opus-5"`)

	_, changed = stripFallbackFieldsForMimic(out)
	require.False(t, changed, "没有字段时不改 body")
}
