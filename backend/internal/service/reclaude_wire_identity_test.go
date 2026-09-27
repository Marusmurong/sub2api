//go:build unit

package service

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// E2-L1 端到端：reclaude 账号的出站 UA、billing 块 cc_version / cc_entrypoint 三处同源。
//
// 09-26 sub 实发：UA (external, sdk-cli)、块 cc_entrypoint=cli、身份块 "official CLI"
// —— 三重矛盾。2026-09-28 翻回 sdk-cli：8x 存活黄金基准证明内嵌 claude headless
// 发的就是 UA (external, sdk-cli) + cc_entrypoint=sdk-cli + cc_turn_origin=sdk +
// Agent SDK 身份块，四处全 sdk-cli 才自洽。
func TestReclaudeWireIdentityIsSelfConsistent(t *testing.T) {
	svc := newWireIdentityService()
	acct := &Account{
		ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude,
		Credentials: map[string]any{
			CredKeyReclaudeClientPlatform: "linux/amd64",
			CredKeyReclaudeMachineEnv:     `{"node_version":"v26.3.0","arch":"x64","cli_version":"2.1.282"}`,
		},
	}
	// 下游哪怕带 cli 迹象,reclaude 也钉 sdk-cli（内嵌 claude 跑 headless `claude -p`）。
	body := []byte(`{"model":"claude-opus-5-5","system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.280.8c9; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],"messages":[{"role":"user","content":"hi"}]}`)
	c := newWireIdentityContext(t)
	c.Request.Header.Set("user-agent", "claude-cli/2.1.280 (external, cli)")

	// forward 阶段的入口判定（gateway_forward.go）在这里复刻。
	ctx := WithClientEntrypoint(context.Background(),
		resolveClientEntrypointForAccount(acct, c.Request.UserAgent(), body))

	req, _, err := svc.buildUpstreamRequest(ctx, c, acct, body, "", "oauth", "claude-opus-5-5", true, true)
	require.NoError(t, err)
	out, err := io.ReadAll(req.Body)
	require.NoError(t, err)

	require.Equal(t, "claude-cli/2.1.282 (external, sdk-cli)", getHeaderRaw(req.Header, "user-agent"))
	require.Contains(t, string(out), "cc_version=2.1.282.")
	require.Contains(t, string(out), "cc_entrypoint=sdk-cli;")
	require.NotContains(t, string(out), "cc_entrypoint=cli;")
	// sdk-cli 独有：cc_turn_origin=sdk + Agent SDK 身份块。
	require.Contains(t, string(out), "cc_turn_origin=sdk;")
	require.Contains(t, string(out), "You are a Claude agent, built on Anthropic's Claude Agent SDK.")
}
