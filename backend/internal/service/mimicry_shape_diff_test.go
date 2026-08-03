//go:build unit

package service

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/tidwall/gjson"
)

// 把伪装产出的 body 与真实 Claude Code 2.1.220 的形态做整体对比，而不是逐字段猜。
//
// 真实形态实测自 2.1.220 发出的请求（cc_entrypoint=cli 与 sdk-cli 两个入口一致）：
//
//	顶层字段: context_management max_tokens messages metadata model
//	          output_config stream system thinking tools
//	system  : 3 块 —— [0] billing header（无 cache_control）
//	                   [1] 身份块（cache_control: ephemeral）
//	                   [2] 约 1 万字符的 agent 指令（cache_control: ephemeral）
//
// 这条测试的价值不在断言某个具体值，而在**任何一侧多出或缺少字段时立刻可见**——
// 此前 temperature 多发了、output_config 少发了、max_tokens 值错了，都是逐个偶然
// 发现的，代价是每次一轮部署。
func TestMimicryShape_TopLevelFieldsMatchRealClaudeCode(t *testing.T) {
	// 真实 2.1.220 的顶层字段全集。stream 由调用方决定，thinking 仅在开启时出现，
	// 二者不作为「必须存在」断言。
	realTopLevel := map[string]bool{
		"context_management": true,
		"max_tokens":         true,
		"messages":           true,
		"metadata":           true,
		"model":              true,
		"output_config":      true,
		"stream":             true,
		"system":             true,
		"thinking":           true,
		"tools":              true,
	}

	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)
	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-opus-5", claudeOAuthNormalizeOptions{
		injectMetadata: true,
		metadataUserID: "probe",
	})

	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("产出的 body 不是合法 JSON: %v", err)
	}

	var extra []string
	for k := range got {
		if !realTopLevel[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("产出了真实 Claude Code 不发送的顶层字段: %v\n"+
			"每一个都是盖在请求上的自有标记，会被上游用来区分客户端", extra)
	}

	// 必发字段：缺任何一个都说明伪装不完整。
	//
	// 这里不含 system —— 它的三块结构（计费头 / 身份块 / agent 指令）由
	// rewriteSystemForNonClaudeCode* 在更外层构造，不是本函数的职责；断言它会让这条
	// 测试测错对象。
	for _, must := range []string{"max_tokens", "output_config", "tools", "metadata"} {
		if _, ok := got[must]; !ok {
			t.Errorf("缺少真实 Claude Code 必发的顶层字段: %s", must)
		}
	}
}

// 采样参数必须与真实客户端一致：一个都不发。
func TestMimicryShape_NoSamplingParams(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)
	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-opus-5", claudeOAuthNormalizeOptions{})

	for _, field := range []string{"temperature", "top_p", "top_k"} {
		if gjson.GetBytes(out, field).Exists() {
			t.Errorf("注入了采样参数 %s —— 真实 2.1.220 的两个入口都不发", field)
		}
	}
}

// 关键默认值必须与实测一致。
func TestMimicryShape_DefaultsMatchMeasured(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)
	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-opus-5", claudeOAuthNormalizeOptions{})

	if got := gjson.GetBytes(out, "max_tokens").Int(); got != claudeCodeDefaultMaxTokens {
		t.Errorf("max_tokens = %d，实测真实值为 %d", got, claudeCodeDefaultMaxTokens)
	}
	if got := gjson.GetBytes(out, "output_config.effort").String(); got != "high" {
		t.Errorf("output_config.effort = %q，实测真实值为 high", got)
	}
	// tools 保持透传，客户端没带就是空数组。曾在这里注入过 8 个核心工具，2026-08-03 的
	// 生产抓包证伪了它的前提（真 CC 的结构化输出调用发的就是空数组），而注入的真名还会
	// 被后续的工具名混淆器改成 invoke_Bas01 这类假名，反成三方铁证。
	if !gjson.GetBytes(out, "tools").IsArray() {
		t.Error("tools 字段缺失 —— 应补一个空数组以维持形态稳定")
	}
	if gjson.GetBytes(out, "tool_choice").Exists() {
		t.Error("注入了 tool_choice —— 真实 Claude Code 从不发送该字段")
	}
}

// 客户端显式给的值一律不得被覆盖：伪装是补齐缺失，不是改写调用方的意图。
func TestMimicryShape_DoesNotOverrideClientValues(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],` +
		`"max_tokens":123,"output_config":{"effort":"low"},"temperature":0.7}`)
	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-opus-5", claudeOAuthNormalizeOptions{})

	if got := gjson.GetBytes(out, "max_tokens").Int(); got != 123 {
		t.Errorf("客户端的 max_tokens 被覆盖为 %d", got)
	}
	if got := gjson.GetBytes(out, "output_config.effort").String(); got != "low" {
		t.Errorf("客户端的 output_config 被覆盖为 %q", got)
	}
	if got := gjson.GetBytes(out, "temperature").Float(); got != 0.7 {
		t.Errorf("客户端的 temperature 被改成了 %v —— 我们只是不主动补，不该删改", got)
	}
}
