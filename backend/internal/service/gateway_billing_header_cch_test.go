package service

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// 真实 2.1.220 的 k7n 在 firstParty + 首方 baseURL 下恒发 " cch=00000;"。
// sub2api 此前 mimic 与透传两条路径都不发，导致该字段出现率为 0。
// 详见 docs/CC_2.1.220_EGRESS_SPEC.md §3。
func TestEnsureBillingHeaderCCH(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "缺失时在 cc_entrypoint 段后补齐",
			in:   "x-anthropic-billing-header: cc_version=2.1.220.7bd; cc_entrypoint=cli;",
			want: "x-anthropic-billing-header: cc_version=2.1.220.7bd; cc_entrypoint=cli; cch=00000;",
		},
		{
			name: "已存在则不重复添加",
			in:   "x-anthropic-billing-header: cc_version=2.1.220.7bd; cc_entrypoint=cli; cch=00000;",
			want: "x-anthropic-billing-header: cc_version=2.1.220.7bd; cc_entrypoint=cli; cch=00000;",
		},
		{
			name: "客户端已带非零 cch 时保持原值",
			in:   "x-anthropic-billing-header: cc_version=2.1.220.7bd; cc_entrypoint=cli; cch=d8726;",
			want: "x-anthropic-billing-header: cc_version=2.1.220.7bd; cc_entrypoint=cli; cch=d8726;",
		},
		{
			name: "cch 插在 cc_entrypoint 之后、后续字段之前",
			in:   "x-anthropic-billing-header: cc_version=2.1.220.7bd; cc_entrypoint=cli; cc_is_subagent=true;",
			want: "x-anthropic-billing-header: cc_version=2.1.220.7bd; cc_entrypoint=cli; cch=00000; cc_is_subagent=true;",
		},
		{
			name: "没有 cc_entrypoint 段时不凭空造字段",
			in:   "x-anthropic-billing-header: cc_version=2.1.220.7bd;",
			want: "x-anthropic-billing-header: cc_version=2.1.220.7bd;",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ensureBillingHeaderCCH(tt.in); got != tt.want {
				t.Errorf("ensureBillingHeaderCCH()\n got  = %q\n want = %q", got, tt.want)
			}
		})
	}
}

func billingBody(t *testing.T, billingText, firstUserText string) []byte {
	t.Helper()
	body := []byte(`{"system":[{"type":"text","text":""},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],"messages":[{"role":"user","content":""}]}`)
	out, ok := setJSONValueBytes(body, "system.0.text", billingText)
	if !ok {
		t.Fatal("构造 billing block 失败")
	}
	out, ok = setJSONValueBytes(out, "messages.0.content", firstUserText)
	if !ok {
		t.Fatal("构造 messages 失败")
	}
	return out
}

func TestNormalizeBillingHeaderBlockAddsCCHOnBothPaths(t *testing.T) {
	const billing = "x-anthropic-billing-header: cc_version=2.1.220.abc; cc_entrypoint=cli;"
	const ua = "claude-cli/2.1.220 (external, cli)"

	for _, mimic := range []bool{true, false} {
		body := billingBody(t, billing, "hello world this is a test message")
		out := normalizeBillingHeaderBlock(body, ua, mimic)
		got := gjson.GetBytes(out, "system.0.text").String()
		if !strings.Contains(got, " cch=00000;") {
			t.Errorf("mimic=%v 时未补齐 cch: %q", mimic, got)
		}
	}
}

// 透传路径不得改动客户端算好的 fp——其取样口径含 transcript 的 isMeta 过滤，
// 我们无法从 API body 复现，重算反而可能引入偏差。
func TestNormalizeBillingHeaderBlockKeepsClientFingerprintOnPassthrough(t *testing.T) {
	const billing = "x-anthropic-billing-header: cc_version=2.1.220.abc; cc_entrypoint=cli;"
	body := billingBody(t, billing, "hello world this is a test message")

	out := normalizeBillingHeaderBlock(body, "claude-cli/2.1.220 (external, cli)", false)

	got := gjson.GetBytes(out, "system.0.text").String()
	if !strings.Contains(got, "cc_version=2.1.220.abc") {
		t.Errorf("透传路径的 fp 被改写了: %q", got)
	}
}

// mimic 路径必须按最终 body 重算 fp：block 构造后 messages 仍会被 dateline 归一化等
// 步骤改写，真实 CLI 是按最终 messages 算 fp 的。
func TestNormalizeBillingHeaderBlockRecomputesFingerprintOnMimic(t *testing.T) {
	const billing = "x-anthropic-billing-header: cc_version=2.1.220.abc; cc_entrypoint=cli;"
	const firstText = "你好，请帮我把这段话翻译成英文"

	body := billingBody(t, billing, firstText)
	out := normalizeBillingHeaderBlock(body, "claude-cli/2.1.220 (external, cli)", true)

	want := computeClaudeCodeFingerprint(body, "2.1.220")
	got := gjson.GetBytes(out, "system.0.text").String()

	if strings.Contains(got, "cc_version=2.1.220.abc") {
		t.Fatalf("mimic 路径未重算 fp: %q", got)
	}
	if !strings.Contains(got, "cc_version=2.1.220."+want) {
		t.Errorf("fp 与按最终 body 计算的结果不符\n got  = %q\n want fp = %s", got, want)
	}
}
