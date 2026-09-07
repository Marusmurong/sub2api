//go:build unit

package claude

import (
	"reflect"
	"testing"
)

// 期望值全部来自 2026-09-07 本机真实 Claude Code 2.1.263（macOS arm64 Bun 原生二进制）
// 对本地假端点的抓包：opus/fable、sonnet、haiku 三族各跑一次 -p "hi"。
// 顺序照抄线上 anthropic-beta 头，不能重排——集合的成分、大小、顺序都是客户端指纹。
//
// context-1m 与 fallback-credit 是账号级门控（1M 上下文权限 / 额外用量信用），
// 真实 CLI 只在账号具备时才发；网关默认不发，账号 extra 打标记才发。
// context-1m 的位置取自 2.1.257 抓包（oauth 之后、interleaved-thinking 之前）。

var (
	real2_1_263OpusBetas = []string{
		"claude-code-20250219",
		"oauth-2025-04-20",
		"interleaved-thinking-2025-05-14",
		"thinking-token-count-2026-05-13",
		"context-management-2025-06-27",
		"prompt-caching-scope-2026-01-05",
		"mid-conversation-system-2026-04-07",
		"per-turn-control-2026-07-01",
		"advisor-tool-2026-03-01",
		"effort-2025-11-24",
		"fallback-credit-2026-06-01", // 门控：该抓包账号具备
		"extended-cache-ttl-2025-04-11",
	}
	real2_1_263SonnetBetas = []string{
		"claude-code-20250219",
		"oauth-2025-04-20",
		"interleaved-thinking-2025-05-14",
		"thinking-token-count-2026-05-13",
		"context-management-2025-06-27",
		"prompt-caching-scope-2026-01-05",
		"mid-conversation-system-2026-04-07",
		"advisor-tool-2026-03-01",
		"effort-2025-11-24",
		"extended-cache-ttl-2025-04-11",
	}
	real2_1_263HaikuBetas = []string{
		"oauth-2025-04-20",
		"interleaved-thinking-2025-05-14",
		"thinking-token-count-2026-05-13",
		"context-management-2025-06-27",
		"prompt-caching-scope-2026-01-05",
		"claude-code-20250219",
		"advisor-tool-2026-03-01",
		"extended-cache-ttl-2025-04-11",
	}
)

func without(list []string, drop string) []string {
	out := make([]string, 0, len(list))
	for _, b := range list {
		if b != drop {
			out = append(out, b)
		}
	}
	return out
}

func TestMimicryBetasForModel_MatchesRealClaudeCode2_1_263(t *testing.T) {
	cases := []struct {
		name  string
		model string
		gates MimicryBetaGates
		want  []string
	}{
		{"opus 默认不发门控 beta", "claude-opus-5", MimicryBetaGates{}, without(real2_1_263OpusBetas, BetaFallbackCreditLegacy)},
		{"fable 与 opus 同族", "claude-fable-5-1", MimicryBetaGates{}, without(real2_1_263OpusBetas, BetaFallbackCreditLegacy)},
		{"opus 账号开了 fallback-credit → 与抓包逐项一致", "claude-fable-5-1", MimicryBetaGates{FallbackCredit: true}, real2_1_263OpusBetas},
		{"sonnet 10 个", "claude-sonnet-5", MimicryBetaGates{}, real2_1_263SonnetBetas},
		{"sonnet 带日期后缀同族", "claude-sonnet-5-20260701", MimicryBetaGates{}, real2_1_263SonnetBetas},
		{"haiku 8 个且 claude-code 排第 6", "claude-haiku-4-5", MimicryBetaGates{}, real2_1_263HaikuBetas},
		{"bedrock 形式的 haiku 也按族", "us.anthropic.claude-haiku-4-5-v1", MimicryBetaGates{}, real2_1_263HaikuBetas},
		{"未知模型按 opus 族", "claude-unknown-9", MimicryBetaGates{}, without(real2_1_263OpusBetas, BetaFallbackCreditLegacy)},
		{"sonnet 上的 fallback-credit 门控不生效（抓包 sonnet 不带）", "claude-sonnet-5", MimicryBetaGates{FallbackCredit: true}, real2_1_263SonnetBetas},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MimicryBetasForModel(tc.model, "", false, tc.gates)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("model=%s\n got  %v\n want %v", tc.model, got, tc.want)
			}
		})
	}
}

func TestMimicryBetasForModel_Context1MSlot(t *testing.T) {
	// 账号门控打开：context-1m 插在 oauth 之后、interleaved-thinking 之前。
	got := MimicryBetasForModel("claude-opus-5", "", false, MimicryBetaGates{Context1M: true})
	if got[1] != BetaOAuth || got[2] != BetaContext1M || got[3] != BetaInterleavedThinking {
		t.Fatalf("context-1m 位置错误: %v", got)
	}
	// 客户端头里带了 context-1m（功能白名单）也进同一个槽位，不重复。
	got2 := MimicryBetasForModel("claude-opus-5", "foo-beta,"+BetaContext1M, false, MimicryBetaGates{Context1M: true})
	if !reflect.DeepEqual(got, got2) {
		t.Fatalf("客户端透传 + 门控不应重复:\n %v\n %v", got, got2)
	}
	// 白名单之外的客户端 beta 一律丢弃。
	for _, b := range got2 {
		if b == "foo-beta" {
			t.Fatal("白名单外的客户端 beta 不得透传")
		}
	}
}

func TestMimicryBetasForModel_FastModeAppendedLast(t *testing.T) {
	got := MimicryBetasForModel("claude-sonnet-5", "", true, MimicryBetaGates{})
	if got[len(got)-1] != BetaFastMode || len(got) != len(real2_1_263SonnetBetas)+1 {
		t.Fatalf("fast-mode 应追加在末尾: %v", got)
	}
}

// 旧入口保持兼容：无模型信息时等价于 opus 族、无门控。
func TestMimicryBetasForRequest_DefaultsToOpusFamily(t *testing.T) {
	if !reflect.DeepEqual(MimicryBetasForRequest("", false), MimicryBetasForModel("claude-opus-5", "", false, MimicryBetaGates{})) {
		t.Fatal("MimicryBetasForRequest 应等价于 opus 族默认集合")
	}
	if !reflect.DeepEqual(FullClaudeCodeMimicryBetas(), without(real2_1_263OpusBetas, BetaFallbackCreditLegacy)) {
		t.Fatalf("FullClaudeCodeMimicryBetas 应为 opus 族非门控集合: %v", FullClaudeCodeMimicryBetas())
	}
}
