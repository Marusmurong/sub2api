//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 出口 system 必须与真实 Claude Code 同形：[billing, 身份块, ...调用方 system]
// （逆向自 2.1.220 二进制，见 docs/CC_2.1.220_EGRESS_SPEC.md §7）。
//
// 扩充段只是"调用方没有 system 时"的填充物。调用方自带 system 时再插一段，就多出
// 一块真实 CLI 从不发送的固定文本——生产抓包实测 42/71 组请求曾是这个 4 块形态。

func systemBlockTexts(t *testing.T, out []byte) []string {
	t.Helper()
	arr := gjson.GetBytes(out, "system").Array()
	texts := make([]string, 0, len(arr))
	for _, b := range arr {
		texts = append(texts, b.Get("text").String())
	}
	return texts
}

const probeBody = `{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`

func TestSystemShape_ClientWithSystemGetsNoExpansionBlock(t *testing.T) {
	clientSystem := []any{
		map[string]any{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
		map[string]any{"type": "text", "text": "客户端的项目指令：永远用中文回答。",
			"cache_control": map[string]any{"type": "ephemeral"}},
	}

	texts := systemBlockTexts(t, rewriteSystemForNonClaudeCodeWithPromptBlocks(
		[]byte(probeBody), clientSystem, "", ""))

	require.Len(t, texts, 3, "必须恰好 3 块，与真实 CC 同形")
	require.Contains(t, texts[0], "x-anthropic-billing-header:")
	require.Equal(t, claudeCodeSystemPrompt, texts[1])
	require.Equal(t, "客户端的项目指令：永远用中文回答。", texts[2],
		"第 3 块应直接是调用方 system，中间不得夹入扩充段")

	joined := strings.Join(texts, "\n")
	require.NotContains(t, joined, claudeCodeSystemPromptExpansion,
		"调用方自带 system 时不得注入扩充段——那是真实 CLI 从不发送的文本")
}

func TestSystemShape_ClientWithoutSystemKeepsExpansionBlock(t *testing.T) {
	// 第三方客户端（opencode 等）没有 system：仅两块会在体量上明显异于真实 CLI，
	// 此时扩充段是有意的填充，不能一并去掉。
	texts := systemBlockTexts(t, rewriteSystemForNonClaudeCodeWithPromptBlocks(
		[]byte(probeBody), nil, "", ""))

	require.Len(t, texts, 3)
	require.Contains(t, texts[0], "x-anthropic-billing-header:")
	require.Equal(t, claudeCodeSystemPrompt, texts[1])
	require.Equal(t, claudeCodeSystemPromptExpansion, texts[2], "无 system 时应补扩充段")
}

// 客户端 system 只有身份句、剥完为空 —— 等价于"没有 system"，仍应补扩充段。
func TestSystemShape_ClientSystemOnlyIdentityFallsBackToExpansion(t *testing.T) {
	clientSystem := []any{
		map[string]any{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
	}
	texts := systemBlockTexts(t, rewriteSystemForNonClaudeCodeWithPromptBlocks(
		[]byte(probeBody), clientSystem, "", ""))

	require.Len(t, texts, 3)
	require.Equal(t, claudeCodeSystemPromptExpansion, texts[2])
}

// admin 在后台保存 block 配置时写回的是展开后的字面文本，不是模板名。
// 只按模板名匹配会让"调用方自带 system 时省掉扩充段"静默失效、4 块形态悄悄回来。
func TestSystemShape_ExpansionSkippedEvenWhenConfigInlinesLiteralText(t *testing.T) {
	inlined := `[
		{"enabled":true,"type":"text","text":"{billing_header}"},
		{"enabled":true,"type":"text","text":"` + claudeCodeSystemPrompt + `"},
		{"enabled":true,"type":"text","text":` + mustJSONString(claudeCodeSystemPromptExpansion) + `,
		 "cache_control":{"type":"ephemeral","ttl":"5m"}}
	]`
	clientSystem := []any{
		map[string]any{"type": "text", "text": "客户端的项目指令"},
	}

	texts := systemBlockTexts(t, rewriteSystemForNonClaudeCodeWithPromptBlocks(
		[]byte(probeBody), clientSystem, "", inlined))

	require.Len(t, texts, 3, "字面文本写法同样要被识别为扩充段并跳过")
	require.Equal(t, "客户端的项目指令", texts[2])
}
