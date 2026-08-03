//go:build unit

package service

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestHoistLeadingSystemMessage_NoTopLevelSystem(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":"You are helpful."},{"role":"user","content":"hi"}]}`)

	out, changed := hoistLeadingSystemMessage(body)
	if !changed {
		t.Fatal("未提升 messages[0] 的 system")
	}
	if got := gjson.GetBytes(out, "system").String(); got != "You are helpful." {
		t.Errorf("顶层 system = %q", got)
	}
	if n := len(gjson.GetBytes(out, "messages").Array()); n != 1 {
		t.Errorf("messages 应剩 1 条，实际 %d", n)
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "user" {
		t.Errorf("剩下的首条消息 role = %q", got)
	}
}

// 顶层已有 system 时两边都不能丢，且提升的内容在前。
func TestHoistLeadingSystemMessage_MergesWithExistingString(t *testing.T) {
	body := []byte(`{"system":"Existing.","messages":[{"role":"system","content":"Hoisted."},{"role":"user","content":"hi"}]}`)

	out, changed := hoistLeadingSystemMessage(body)
	if !changed {
		t.Fatal("未提升")
	}
	if got := gjson.GetBytes(out, "system").String(); got != "Hoisted.\n\nExisting." {
		t.Errorf("合并结果 = %q", got)
	}
}

func TestHoistLeadingSystemMessage_MergesWithExistingArray(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"Existing."}],"messages":[{"role":"system","content":"Hoisted."},{"role":"user","content":"hi"}]}`)

	out, changed := hoistLeadingSystemMessage(body)
	if !changed {
		t.Fatal("未提升")
	}
	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 2 {
		t.Fatalf("system 块数 = %d，应为 2", len(blocks))
	}
	if got := blocks[0].Get("text").String(); got != "Hoisted." {
		t.Errorf("提升的块应排在最前，实际首块 = %q", got)
	}
	if got := blocks[1].Get("text").String(); got != "Existing." {
		t.Errorf("原有块被破坏: %q", got)
	}
}

// content 为文本块数组时同样可提升。
func TestHoistLeadingSystemMessage_ContentAsTextBlocks(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":[{"type":"text","text":"A"},{"type":"text","text":"B"}]},{"role":"user","content":"hi"}]}`)

	out, changed := hoistLeadingSystemMessage(body)
	if !changed {
		t.Fatal("未提升")
	}
	if got := gjson.GetBytes(out, "system").String(); got != "A\n\nB" {
		t.Errorf("system = %q", got)
	}
}

// 含非文本内容时必须原样返回：宁可让上游报真实原因，也不能悄悄丢掉一张图。
func TestHoistLeadingSystemMessage_LeavesNonTextUntouched(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":[{"type":"image","source":{}}]},{"role":"user","content":"hi"}]}`)

	out, changed := hoistLeadingSystemMessage(body)
	if changed {
		t.Fatal("含图片内容不应改写")
	}
	if len(gjson.GetBytes(out, "messages").Array()) != 2 {
		t.Error("消息被改动了")
	}
}

// 首条不是 system 时不得触碰（含中途出现 system 的情况）。
func TestHoistLeadingSystemMessage_OnlyFirstMessage(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"system","content":"mid"}]}`)

	if _, changed := hoistLeadingSystemMessage(body); changed {
		t.Fatal("只应处理 messages[0]")
	}
}

func TestNormalizeToolChoiceShape_StringForms(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`"auto"`, "auto"},
		{`"none"`, "none"},
		{`"any"`, "any"},
		{`"required"`, "any"}, // Anthropic 无 required，语义等价于 any
		{`"AUTO"`, "auto"},
	}
	for _, tc := range cases {
		body := []byte(`{"tool_choice":` + tc.in + `,"tools":[{"name":"a","input_schema":{}}]}`)
		out, changed := normalizeToolChoiceShape(body)
		if !changed {
			t.Errorf("%s 未被归一化", tc.in)
			continue
		}
		if got := gjson.GetBytes(out, "tool_choice.type").String(); got != tc.want {
			t.Errorf("%s → type=%q，期望 %q", tc.in, got, tc.want)
		}
	}
}

// 无法映射的值一律删除：缺省即 auto，删掉最多回到默认；猜错可能把语义改反。
func TestNormalizeToolChoiceShape_DropsUnmappable(t *testing.T) {
	for _, raw := range []string{`null`, `123`, `"whatever"`, `["auto"]`} {
		body := []byte(`{"tool_choice":` + raw + `,"tools":[]}`)
		out, changed := normalizeToolChoiceShape(body)
		if !changed {
			t.Errorf("%s 未被处理", raw)
			continue
		}
		if gjson.GetBytes(out, "tool_choice").Exists() {
			t.Errorf("%s 应被删除，实际保留", raw)
		}
	}
}

// 已经是对象的不得改动。
func TestNormalizeToolChoiceShape_LeavesObjectUntouched(t *testing.T) {
	body := []byte(`{"tool_choice":{"type":"tool","name":"Bash"}}`)
	out, changed := normalizeToolChoiceShape(body)
	if changed {
		t.Fatal("对象形式不应改动")
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "Bash" {
		t.Errorf("tool_choice 被破坏: %q", got)
	}
}

// 端到端：两个 OpenAI 方言问题同时存在时，归一化后应都被修正。
func TestNormalizeClaudeOAuthRequestBody_FixesOpenAIDialect(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","tool_choice":"auto",` +
		`"tools":[{"name":"a","input_schema":{}}],` +
		`"messages":[{"role":"system","content":"Be brief."},{"role":"user","content":"hi"}]}`)

	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-opus-5", claudeOAuthNormalizeOptions{})

	if gjson.GetBytes(out, "messages.0.role").String() != "user" {
		t.Error("system 消息未被提升出 messages")
	}
	if !gjson.GetBytes(out, "system").Exists() {
		t.Error("顶层 system 缺失")
	}
	if got := gjson.GetBytes(out, "tool_choice.type").String(); got != "auto" {
		t.Errorf("tool_choice.type = %q", got)
	}
}
