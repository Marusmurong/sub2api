//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

// 核心工具集必须能解析出预期的 8 个工具，且每个都带 name/description/input_schema。
//
// 少任何一项都会让注入出去的 tools 变成一个残缺形态——那比不注入更容易被识别。
func TestClaudeCodeCoreTools_Wellformed(t *testing.T) {
	raw := ClaudeCodeCoreToolsRaw()
	if len(raw) == 0 {
		t.Fatal("核心工具集为空：embed 的 JSON 解析失败")
	}

	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatalf("工具集不是合法 JSON 数组: %v", err)
	}

	want := []string{"Agent", "Bash", "Edit", "Read", "Write", "NotebookEdit", "WebFetch", "WebSearch"}
	if len(tools) != len(want) {
		t.Fatalf("工具数量 = %d，期望 %d", len(tools), len(want))
	}
	for i, tool := range tools {
		for _, key := range []string{"name", "description", "input_schema"} {
			if _, ok := tool[key]; !ok {
				t.Errorf("第 %d 个工具缺少字段 %q", i, key)
			}
		}
	}

	got := ClaudeCodeCoreToolNames()
	for i, name := range want {
		if i >= len(got) || got[i] != name {
			t.Fatalf("工具名序列 = %v，期望 %v", got, want)
		}
	}
}

// 客户端没带 tools 时，注入真实工具集并置 tool_choice=none。
//
// 置 none 是必须的：我们替客户端带了工具，但对方并不实现它们。不置的话模型会返回
// tool_use 块，下游解析不了直接故障。
func TestNormalizeClaudeOAuth_InjectsCoreToolsWhenAbsent(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`)

	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-sonnet-4-6", claudeOAuthNormalizeOptions{})

	tools := gjson.GetBytes(out, "tools")
	if !tools.IsArray() {
		t.Fatal("tools 不是数组")
	}
	if n := len(tools.Array()); n != 8 {
		t.Fatalf("注入了 %d 个工具，期望 8 个", n)
	}
	// 绝不能再是空数组——那是真实 Claude Code 从不产生的形态。
	if tools.Raw == "[]" {
		t.Fatal("tools 仍是空数组")
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "Agent" {
		t.Fatalf("首个工具 = %q，期望 Agent", got)
	}
	if got := gjson.GetBytes(out, "tool_choice.type").String(); got != "none" {
		t.Fatalf("tool_choice.type = %q，期望 none（否则下游会收到无法解析的 tool_use）", got)
	}
}

// 客户端自己带了 tools 时不得覆盖，也不得强加 tool_choice=none。
//
// 带 tools 的客户端是要用工具的，塞 none 会把它的正常功能打掉。
func TestNormalizeClaudeOAuth_KeepsClientTools(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],` +
		`"tools":[{"name":"my_tool","description":"d","input_schema":{"type":"object"}}]}`)

	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-sonnet-4-6", claudeOAuthNormalizeOptions{})

	tools := gjson.GetBytes(out, "tools").Array()
	if len(tools) != 1 {
		t.Fatalf("客户端工具被改动：数量 = %d，期望 1", len(tools))
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "my_tool" {
		t.Fatalf("客户端工具名被覆盖为 %q", got)
	}
	if gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatal("不得给自带 tools 的客户端强加 tool_choice —— 会打掉它的工具调用能力")
	}
}

// 客户端没带 tools 却带了 tool_choice 时，必须被覆盖成 none。
//
// 这条是本次改动里最容易出错的一处：客户端的 tool_choice（如 auto）本来指向一个不存在
// 的工具集，旧逻辑直接删掉它。注入工具之后若还保留 auto，模型就会调用这些工具并返回
// tool_use——下游没有实现它们，拿到就是解析失败。注入的前提是保证它们不会被调用。
func TestNormalizeClaudeOAuth_OverridesToolChoiceWhenInjecting(t *testing.T) {
	for _, given := range []string{`{"type":"auto"}`, `{"type":"any"}`, `{"type":"tool","name":"x"}`} {
		t.Run(given, func(t *testing.T) {
			body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],` +
				`"tool_choice":` + given + `}`)

			out, _ := normalizeClaudeOAuthRequestBody(body, "claude-sonnet-4-6", claudeOAuthNormalizeOptions{})

			if got := gjson.GetBytes(out, "tool_choice.type").String(); got != "none" {
				t.Fatalf("tool_choice.type = %q，期望 none —— 否则模型会调用我们注入的工具", got)
			}
			if n := len(gjson.GetBytes(out, "tools").Array()); n != 8 {
				t.Fatalf("工具数量 = %d，期望 8", n)
			}
		})
	}
}

// count_tokens 路径不补 max_tokens/temperature，但工具集仍应注入——
// 保证该端点的请求形态与 /v1/messages 一致。
func TestNormalizeClaudeOAuth_InjectsToolsOnCountTokensPath(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`)

	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-sonnet-4-6", claudeOAuthNormalizeOptions{
		skipMessagesOnlyFields: true,
	})

	if n := len(gjson.GetBytes(out, "tools").Array()); n != 8 {
		t.Fatalf("count_tokens 路径工具数量 = %d，期望 8", n)
	}
	if gjson.GetBytes(out, "max_tokens").Exists() {
		t.Fatal("count_tokens 路径不得补 max_tokens —— 该端点严格 schema，多一个字段就 400")
	}
}
