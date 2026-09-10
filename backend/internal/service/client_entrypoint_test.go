//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
)

// 造一个带 billing 行的 system,模拟真实 Claude Code 家族客户端的请求体。
func bodyWithEntrypoint(ep string) []byte {
	if ep == "" {
		return []byte(`{"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],"messages":[]}`)
	}
	return []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.266.abc; cc_entrypoint=` + ep +
		`; cch=00000;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],"messages":[]}`)
}

func TestResolveClientEntrypoint(t *testing.T) {
	patch := claude.CLIPatchVersion()
	cases := []struct {
		name       string
		ua         string
		bodyEP     string
		wantProd   string
		wantSuffix string
	}{
		{"VS Code 扩展", "claude-cli/2.1.266 (external, claude-vscode, agent-sdk/0.3.266)", "claude-vscode",
			"claude-vscode", "(external, claude-vscode, agent-sdk/0.3." + patch + ")"},
		{"Agent SDK (ts)", "claude-cli/2.1.258 (external, sdk-ts, agent-sdk/0.3.258)", "sdk-ts",
			"sdk-ts", "(external, sdk-ts, agent-sdk/0.3." + patch + ")"},
		{"sdk-cli 裸形态", "claude-cli/2.1.251 (external, sdk-cli)", "sdk-cli",
			"sdk-cli", "(external, sdk-cli)"},
		{"真 CLI", "claude-cli/2.1.263 (external, cli)", "cli", "cli", "(external, cli)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveClientEntrypoint(tc.ua, bodyWithEntrypoint(tc.bodyEP))
			require.Equal(t, tc.wantProd, got.Product)
			require.Equal(t, tc.wantSuffix, got.UASuffix)
		})
	}
}

// 安全性质:UA 与下游自报的 cc_entrypoint 对不上时,一律回落 cli。
// 我们绝不发出一个"没在真实客户端上同时观察到"的 (UA 后缀, cc_entrypoint) 组合。
func TestResolveClientEntrypointFallsBackWhenUnconfirmed(t *testing.T) {
	cli := defaultClientEntrypoint()
	for _, tc := range []struct {
		name   string
		ua     string
		bodyEP string
	}{
		{"下游没带 billing 块", "claude-cli/2.1.266 (external, claude-vscode, agent-sdk/0.3.266)", ""},
		{"UA 与 cc_entrypoint 不符", "claude-cli/2.1.266 (external, claude-vscode, agent-sdk/0.3.266)", "sdk-ts"},
		{"未知产品(不在实测名单里)", "claude-cli/2.1.266 (external, claude-jetbrains, agent-sdk/0.3.266)", "claude-jetbrains"},
		{"sdk-py 属于 0.2.x 家族,刻意排除", "claude-cli/2.1.200 (external, sdk-py, agent-sdk/0.2.139)", "sdk-py"},
		{"非 claude-cli UA", "Go-http-client/1.1", "cli"},
		{"UA 无括号后缀", "claude-cli/2.1.263", "cli"},
		{"缺 external 前缀", "claude-cli/2.1.263 (internal, cli)", "cli"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, cli, ResolveClientEntrypoint(tc.ua, bodyWithEntrypoint(tc.bodyEP)))
		})
	}
}

// agent-sdk 版本按我们锁定的 patch 钉死,不跟着下游漂——否则一个账号会先后
// 冒出十几个 SDK 版本,本身就是多人使用特征。
func TestResolveClientEntrypointPinsAgentSDKVersion(t *testing.T) {
	want := "(external, claude-vscode, agent-sdk/0.3." + claude.CLIPatchVersion() + ")"
	for _, ua := range []string{
		"claude-cli/2.1.216 (external, claude-vscode, agent-sdk/0.3.216)",
		"claude-cli/2.1.267 (external, claude-vscode, agent-sdk/0.3.267)",
	} {
		require.Equal(t, want, ResolveClientEntrypoint(ua, bodyWithEntrypoint("claude-vscode")).UASuffix)
	}
}

// billing 块只解析 system,不看 messages:正文里可能引用这些字样。
func TestClientDeclaredCCEntrypointIgnoresMessages(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"hi"}],` +
		`"messages":[{"role":"user","content":"x-anthropic-billing-header: cc_version=1.0.0.x; cc_entrypoint=sdk-ts;"}]}`)
	require.Equal(t, "", clientDeclaredCCEntrypoint(body))
}

// billing 块与出站 UA 必须同源:同一次判定喂给两边。
func TestBillingBlockUsesResolvedEntrypoint(t *testing.T) {
	body := bodyWithEntrypoint("claude-vscode")
	e := ResolveClientEntrypoint("claude-cli/2.1.266 (external, claude-vscode, agent-sdk/0.3.266)", body)

	text, err := buildBillingAttributionText(body, claude.CLIVersion(), e.Product)
	require.NoError(t, err)
	require.Contains(t, text, "cc_entrypoint=claude-vscode;")

	// 空 entrypoint 保持改动前行为
	text, err = buildBillingAttributionText(body, claude.CLIVersion(), "")
	require.NoError(t, err)
	require.Contains(t, text, "cc_entrypoint=cli;")
}
