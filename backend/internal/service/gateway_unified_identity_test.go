//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 统一出口身份：一个上游账号服务多个下游客户，上游必须只看到**一个**客户端。
//
// 生产抓包实测（改动前，7 分钟）：单个账号出现 3 种 cc_entrypoint（cli / local-agent /
// sdk-ts）、4 种 beta 集合大小。根因是"下游像 Claude Code 就透传它的身份"。

// newUnifiedIdentityTestContext 造一个"真 Claude Code 客户端"的请求：
// 改动前这种请求会走透传，把下面这些头原样带给上游。
func newUnifiedIdentityTestContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	for k, v := range map[string]string{
		"User-Agent":                  "claude-cli/2.1.100 (external, cli)",
		"anthropic-beta":              "claude-code-20250219,oauth-2025-04-20,advisor-tool-2026-03-01,afk-mode-2026-01-31",
		"accept-language":             "de-DE,de;q=0.9",
		"sec-fetch-mode":              "cors",
		"x-client-request-id":         "downstream-customer-request-id",
		"x-stainless-os":              "Windows",
		"x-stainless-arch":            "x64",
		"x-stainless-runtime-version": "v20.1.0",
		"x-stainless-retry-count":     "7",
		"x-app":                       "vscode",
	} {
		c.Request.Header.Set(k, v)
	}
	return c
}

func newUnifiedIdentityService() *GatewayService {
	return &GatewayService{cfg: &config.Config{
		Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize},
	}}
}

func newUnifiedIdentityAccount() *Account {
	return &Account{ID: 4242, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
}

// ===== A1/A2：伪装路径不得透传任何客户端身份头 =====

func TestUnifiedIdentity_MimicDropsClientIdentityHeaders(t *testing.T) {
	svc := newUnifiedIdentityService()
	c := newUnifiedIdentityTestContext(t)
	body := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`)

	req, _, err := svc.buildUpstreamRequest(context.Background(), c, newUnifiedIdentityAccount(),
		body, "tok", "oauth", "claude-opus-4-8", true, true)
	require.NoError(t, err)

	// 这些是纯粹的下游客户特征，出口一律不该出现
	for _, h := range []string{"accept-language", "sec-fetch-mode"} {
		require.Empty(t, getHeaderRaw(req.Header, h), "%s 不得透传：这是下游客户的机器特征", h)
	}
	require.NotEqual(t, "downstream-customer-request-id", getHeaderRaw(req.Header, "x-client-request-id"),
		"x-client-request-id 必须由我们生成，不能沿用客户端的")

	// 平台头必须是我们的固定值，而不是客户端的 Windows/x64/v20.1.0
	require.Equal(t, claude.DefaultHeaders["X-Stainless-OS"], getHeaderRaw(req.Header, "x-stainless-os"))
	require.Equal(t, claude.DefaultHeaders["X-Stainless-Arch"], getHeaderRaw(req.Header, "x-stainless-arch"))
	require.Equal(t, claude.DefaultHeaders["X-App"], getHeaderRaw(req.Header, "x-app"))
	require.Equal(t, claude.DefaultHeaders["User-Agent"], getHeaderRaw(req.Header, "user-agent"),
		"UA 必须是账号统一身份，不能是客户端自报的版本")
}

func TestUnifiedIdentity_CountTokensAlsoDropsClientHeaders(t *testing.T) {
	svc := newUnifiedIdentityService()
	c := newUnifiedIdentityTestContext(t)
	body := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`)

	req, _, err := svc.buildCountTokensRequest(context.Background(), c, newUnifiedIdentityAccount(),
		body, "tok", "oauth", "claude-opus-4-8", true)
	require.NoError(t, err)

	// count_tokens 与 messages 打同一批账号，漏了它等于留后门
	for _, h := range []string{"accept-language", "sec-fetch-mode"} {
		require.Empty(t, getHeaderRaw(req.Header, h), "count_tokens 的 %s 同样不得透传", h)
	}
	require.NotEqual(t, "downstream-customer-request-id", getHeaderRaw(req.Header, "x-client-request-id"))
}

// ===== A3：beta 固定集合 + 功能白名单 =====

func TestMimicryBetas_DropsClientIdentityBetasKeepsWhitelistedFeature(t *testing.T) {
	fixed := claude.FullClaudeCodeMimicryBetas()

	t.Run("客户端 beta 全部丢弃", func(t *testing.T) {
		got := claude.MimicryBetasWithClientFeatures(
			"claude-code-20250219,advisor-tool-2026-03-01,afk-mode-2026-01-31,mid-conversation-system-2026-04-07")
		require.Equal(t, fixed, got, "白名单外的客户端 beta 一个都不能进")
	})

	t.Run("白名单内的功能 beta 保留", func(t *testing.T) {
		got := claude.MimicryBetasWithClientFeatures("context-1m-2025-08-07,afk-mode-2026-01-31")
		require.Contains(t, got, claude.BetaContext1M, "1M 上下文是功能开关，丢了会让长请求直接超限")
		require.NotContains(t, got, "afk-mode-2026-01-31")
		require.Len(t, got, len(fixed)+1)
	})

	t.Run("顺序稳定且与客户端到达顺序无关", func(t *testing.T) {
		a := claude.MimicryBetasWithClientFeatures("context-1m-2025-08-07,zzz-2026-01-01")
		b := claude.MimicryBetasWithClientFeatures("zzz-2026-01-01,context-1m-2025-08-07")
		require.Equal(t, a, b, "顺序本身也是指纹，不能随客户端排列变化")
		// context-1m 的规范位置在 oauth 之后
		require.Equal(t, claude.BetaOAuth, a[1])
		require.Equal(t, claude.BetaContext1M, a[2])
	})

	t.Run("空输入退化为固定集合", func(t *testing.T) {
		require.Equal(t, fixed, claude.MimicryBetasWithClientFeatures(""))
	})
}

func TestUnifiedIdentity_OutgoingBetaIsFixedRegardlessOfClient(t *testing.T) {
	svc := newUnifiedIdentityService()
	c := newUnifiedIdentityTestContext(t) // 客户端带了 advisor-tool / afk-mode
	body := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`)

	req, _, err := svc.buildUpstreamRequest(context.Background(), c, newUnifiedIdentityAccount(),
		body, "tok", "oauth", "claude-opus-4-8", true, true)
	require.NoError(t, err)

	beta := getHeaderRaw(req.Header, "anthropic-beta")
	require.Equal(t, strings.Join(claude.FullClaudeCodeMimicryBetas(), ","), beta,
		"出口 beta 必须与客户端无关；抓包中单账号 4 种集合大小就是从这里来的")
}

// ===== A4：剥离客户端自带的 billing block =====

func TestUnifiedIdentity_StripsClientBillingBlockKeepsClientInstructions(t *testing.T) {
	clientSystem := []any{
		map[string]any{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.100.abc; cc_entrypoint=sdk-ts; cc_workload=agent; cc_is_subagent=true;"},
		map[string]any{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
		map[string]any{"type": "text", "text": "客户自己的项目指令：永远用中文回答。"},
	}
	body := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`)

	out := rewriteSystemForNonClaudeCodeWithPromptBlocks(body, clientSystem, "", "")
	blocks := gjson.GetBytes(out, "system").Array()
	require.NotEmpty(t, blocks)

	var billingCount, identityCount int
	var all strings.Builder
	for _, b := range blocks {
		txt := b.Get("text").String()
		all.WriteString(txt)
		if strings.Contains(strings.ToLower(txt), billingHeaderPrefix) {
			billingCount += strings.Count(strings.ToLower(txt), billingHeaderPrefix)
		}
		identityCount += strings.Count(txt, claudeCodeSystemPrompt)
	}

	require.Equal(t, 1, billingCount,
		"有且只有一份 billing block；两份互相矛盾的身份声明正是抓包里 sdk-ts 的来源")
	require.Equal(t, 1, identityCount, "身份句也只能有一份")

	joined := all.String()
	require.NotContains(t, joined, "cc_entrypoint=sdk-ts", "客户端的 entrypoint 不得外泄")
	require.NotContains(t, joined, "cc_workload", "我们从不生成的字段同样是客户端特征")
	require.NotContains(t, joined, "cc_is_subagent")
	require.Contains(t, joined, "客户自己的项目指令：永远用中文回答。",
		"客户的真实指令必须保留，丢了会让客户端配置静默失效")
	require.Contains(t, joined, "cc_entrypoint=cli", "应换成我们统一的 entrypoint")
}

// 客户端 billing block 挡在身份句前面时，旧实现的前缀匹配会整段落空，
// 导致身份句剥离在真实流量里基本失效。这里锁住修复。
func TestStripClaudeCodeIdentityPrefix_HandlesBillingBlockBeforeIdentity(t *testing.T) {
	got := stripClaudeCodeIdentityPrefix(
		"x-anthropic-billing-header: cc_version=2.1.100.abc; cc_entrypoint=sdk-ts;\n\n" +
			"You are Claude Code, Anthropic's official CLI for Claude.\n\n" +
			"保留我。")
	require.Equal(t, "保留我。", got)
}

func TestStripClaudeCodeIdentityPrefix_NoBillingBlockUnchanged(t *testing.T) {
	// 没有 billing block 时行为不变（防止本次改动误伤既有路径）
	require.Equal(t, "保留我。", stripClaudeCodeIdentityPrefix(
		"You are Claude Code, Anthropic's official CLI for Claude.\n\n保留我。"))
	require.Equal(t, "纯客户端指令", stripClaudeCodeIdentityPrefix("纯客户端指令"))
}
