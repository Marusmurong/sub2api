package service

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// 真实 CLI 的 system 形态是 [billing, 身份块, ...调用方 system]，调用方的 system
// 留在 system 数组里。此前实现把它搬成一对写死常量的伪造对话注入 messages 开头，
// 既是本仓独有形态，又让 messages[0] 变成我们自己造的文本，导致 cc_version 的 fp
// 与最终 body 不一致。详见 docs/CC_2.1.220_EGRESS_SPEC.md §7。
func TestRewriteSystemKeepsClientInstructionsInSystemArray(t *testing.T) {
	const clientSystem = "You are a helpful translation assistant. Always answer in Chinese."
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"你好，请帮我把这段话翻译成英文"}]}`)

	out := rewriteSystemForNonClaudeCodeWithPromptBlocks(body, clientSystem, "", "")

	system := gjson.GetBytes(out, "system")
	if !system.IsArray() {
		t.Fatalf("system 不是数组: %s", system.Raw)
	}

	// 客户端指令必须出现在 system 尾块里
	last := system.Array()[len(system.Array())-1].Get("text").String()
	if !strings.Contains(last, clientSystem) {
		t.Errorf("客户端 system 未作为尾块保留，尾块为: %q", last)
	}

	// billing block 仍是首块
	if !strings.HasPrefix(system.Array()[0].Get("text").String(), billingHeaderPrefix) {
		t.Errorf("首块不是 billing block: %q", system.Array()[0].Get("text").String())
	}
}

func TestRewriteSystemNoLongerInjectsFakeConversation(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"你好，请帮我把这段话翻译成英文"}]}`)

	out := rewriteSystemForNonClaudeCodeWithPromptBlocks(
		body, "You are a helpful translation assistant.", "", "")

	raw := string(out)
	for _, marker := range []string{
		"[System Instructions]",
		"Understood. I will follow these instructions.",
	} {
		if strings.Contains(raw, marker) {
			t.Errorf("仍在注入写死常量 %q", marker)
		}
	}

	// messages 必须原封不动：首条仍是客户端的真实消息
	messages := gjson.GetBytes(out, "messages")
	if n := len(messages.Array()); n != 1 {
		t.Fatalf("messages 数量被改动: %d，期望 1", n)
	}
	if got := messages.Array()[0].Get("role").String(); got != "user" {
		t.Errorf("messages[0].role = %q, want user", got)
	}
	if got := extractFirstUserText(out); got != "你好，请帮我把这段话翻译成英文" {
		t.Errorf("首条 user 文本被改动: %q", got)
	}
}

// messages 不再被改写后，fp 的计算输入与最终发出的 body 一致。
func TestRewriteSystemKeepsFingerprintConsistentWithFinalBody(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"你好，请帮我把这段话翻译成英文"}]}`)

	out := rewriteSystemForNonClaudeCodeWithPromptBlocks(
		body, "You are a helpful translation assistant.", "", "")

	// 按最终 body 重算的 fp，应与 block 里已写入的一致
	want := computeClaudeCodeFingerprint(out, "2.1.220")
	billing := gjson.GetBytes(out, "system.0.text").String()

	if !strings.Contains(billing, "cc_version=2.1.220."+want) {
		t.Errorf("billing block 的 fp 与最终 body 不一致\n block = %q\n want fp = %s", billing, want)
	}
}

// 客户端 system 里自带的 Claude Code 身份句要剥掉（注入的 system 已有一份），
// 但其后的自定义指令必须保留。
func TestRewriteSystemStripsIdentityPrefixButKeepsInstructions(t *testing.T) {
	const clientSystem = "You are Claude Code, Anthropic's official CLI for Claude.\n\nAlways answer in Chinese."
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)

	out := rewriteSystemForNonClaudeCodeWithPromptBlocks(body, clientSystem, "", "")

	system := gjson.GetBytes(out, "system").Array()
	last := system[len(system)-1].Get("text").String()

	if !strings.Contains(last, "Always answer in Chinese.") {
		t.Errorf("客户端自定义指令丢失: %q", last)
	}
	if strings.Contains(last, "You are Claude Code, Anthropic's official CLI for Claude.") {
		t.Errorf("尾块仍残留身份句: %q", last)
	}
}
