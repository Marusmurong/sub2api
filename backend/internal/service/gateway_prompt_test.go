package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestIsClaudeCodeClient(t *testing.T) {
	// 合法的 legacy 格式 metadata.user_id（64位 hex + account uuid + session uuid）
	legacyUserID := "user_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2_account_550e8400-e29b-41d4-a716-446655440000_session_123e4567-e89b-12d3-a456-426614174000"
	// 合法的 JSON 格式 metadata.user_id（2.1.78+ 版本）
	jsonUserID := `{"device_id":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2","account_uuid":"550e8400-e29b-41d4-a716-446655440000","session_id":"123e4567-e89b-12d3-a456-426614174000"}`

	tests := []struct {
		name           string
		userAgent      string
		metadataUserID string
		want           bool
	}{
		{
			name:           "Claude Code client with legacy user_id",
			userAgent:      "claude-cli/1.0.62 (darwin; arm64)",
			metadataUserID: legacyUserID,
			want:           true,
		},
		{
			name:           "Claude Code client with JSON user_id",
			userAgent:      "claude-cli/2.1.92 (external, cli)",
			metadataUserID: jsonUserID,
			want:           true,
		},
		{
			name:           "Claude Code case insensitive UA",
			userAgent:      "Claude-CLI/2.0.0",
			metadataUserID: legacyUserID,
			want:           true,
		},
		{
			name:           "Missing metadata user_id",
			userAgent:      "claude-cli/1.0.0",
			metadataUserID: "",
			want:           false,
		},
		{
			name:           "Claude CLI UA with invalid user_id format",
			userAgent:      "claude-cli/2.0.0",
			metadataUserID: "fake-user-id-12345",
			want:           false,
		},
		{
			name:           "Different user agent with valid user_id",
			userAgent:      "curl/7.68.0",
			metadataUserID: legacyUserID,
			want:           false,
		},
		{
			name:           "Empty user agent",
			userAgent:      "",
			metadataUserID: legacyUserID,
			want:           false,
		},
		{
			name:           "Similar but not Claude CLI",
			userAgent:      "claude-api/1.0.0",
			metadataUserID: legacyUserID,
			want:           false,
		},
		{
			name:           "Opencode spoofing UA with arbitrary user_id",
			userAgent:      "claude-cli/2.1.92",
			metadataUserID: "session_abc",
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isClaudeCodeClient(tt.userAgent, tt.metadataUserID)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestSystemIncludesClaudeCodePrompt(t *testing.T) {
	tests := []struct {
		name   string
		system any
		want   bool
	}{
		{
			name:   "nil system",
			system: nil,
			want:   false,
		},
		{
			name:   "empty string",
			system: "",
			want:   false,
		},
		{
			name:   "string with Claude Code prompt",
			system: claudeCodeSystemPrompt,
			want:   true,
		},
		{
			name:   "string with different content",
			system: "You are a helpful assistant.",
			want:   false,
		},
		{
			name:   "empty array",
			system: []any{},
			want:   false,
		},
		{
			name: "array with Claude Code prompt",
			system: []any{
				map[string]any{
					"type": "text",
					"text": claudeCodeSystemPrompt,
				},
			},
			want: true,
		},
		{
			name: "array with Claude Code prompt in second position",
			system: []any{
				map[string]any{"type": "text", "text": "First prompt"},
				map[string]any{"type": "text", "text": claudeCodeSystemPrompt},
			},
			want: true,
		},
		{
			name: "array without Claude Code prompt",
			system: []any{
				map[string]any{"type": "text", "text": "Custom prompt"},
			},
			want: false,
		},
		{
			name: "array with partial match (should not match)",
			system: []any{
				map[string]any{"type": "text", "text": "You are Claude"},
			},
			want: false,
		},
		// json.RawMessage cases (conversion path: ForwardAsResponses / ForwardAsChatCompletions)
		{
			name:   "json.RawMessage string with Claude Code prompt",
			system: json.RawMessage(`"` + claudeCodeSystemPrompt + `"`),
			want:   true,
		},
		{
			name:   "json.RawMessage string without Claude Code prompt",
			system: json.RawMessage(`"You are a helpful assistant"`),
			want:   false,
		},
		{
			name:   "json.RawMessage nil (empty)",
			system: json.RawMessage(nil),
			want:   false,
		},
		{
			name:   "json.RawMessage empty string",
			system: json.RawMessage(`""`),
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := systemIncludesClaudeCodePrompt(tt.system)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestInjectClaudeCodePrompt(t *testing.T) {
	claudePrefix := strings.TrimSpace(claudeCodeSystemPrompt)

	tests := []struct {
		name           string
		body           string
		system         any
		wantSystemLen  int
		wantFirstText  string
		wantSecondText string
	}{
		{
			name:          "nil system",
			body:          `{"model":"claude-3"}`,
			system:        nil,
			wantSystemLen: 1,
			wantFirstText: claudeCodeSystemPrompt,
		},
		{
			name:          "empty string system",
			body:          `{"model":"claude-3"}`,
			system:        "",
			wantSystemLen: 1,
			wantFirstText: claudeCodeSystemPrompt,
		},
		{
			name:           "string system",
			body:           `{"model":"claude-3"}`,
			system:         "Custom prompt",
			wantSystemLen:  2,
			wantFirstText:  claudeCodeSystemPrompt,
			wantSecondText: claudePrefix + "\n\nCustom prompt",
		},
		{
			name:          "string system equals Claude Code prompt",
			body:          `{"model":"claude-3"}`,
			system:        claudeCodeSystemPrompt,
			wantSystemLen: 1,
			wantFirstText: claudeCodeSystemPrompt,
		},
		{
			name:   "array system",
			body:   `{"model":"claude-3"}`,
			system: []any{map[string]any{"type": "text", "text": "Custom"}},
			// Claude Code + Custom = 2
			wantSystemLen:  2,
			wantFirstText:  claudeCodeSystemPrompt,
			wantSecondText: claudePrefix + "\n\nCustom",
		},
		{
			name: "array system with existing Claude Code prompt (should dedupe)",
			body: `{"model":"claude-3"}`,
			system: []any{
				map[string]any{"type": "text", "text": claudeCodeSystemPrompt},
				map[string]any{"type": "text", "text": "Other"},
			},
			// Claude Code at start + Other = 2 (deduped)
			wantSystemLen:  2,
			wantFirstText:  claudeCodeSystemPrompt,
			wantSecondText: claudePrefix + "\n\nOther",
		},
		{
			name:          "empty array",
			body:          `{"model":"claude-3"}`,
			system:        []any{},
			wantSystemLen: 1,
			wantFirstText: claudeCodeSystemPrompt,
		},
		// json.RawMessage cases (conversion path: ForwardAsResponses / ForwardAsChatCompletions)
		{
			name:           "json.RawMessage string system",
			body:           `{"model":"claude-3","system":"Custom prompt"}`,
			system:         json.RawMessage(`"Custom prompt"`),
			wantSystemLen:  2,
			wantFirstText:  claudeCodeSystemPrompt,
			wantSecondText: claudePrefix + "\n\nCustom prompt",
		},
		{
			name:          "json.RawMessage nil system",
			body:          `{"model":"claude-3"}`,
			system:        json.RawMessage(nil),
			wantSystemLen: 1,
			wantFirstText: claudeCodeSystemPrompt,
		},
		{
			name:          "json.RawMessage Claude Code prompt (should not duplicate)",
			body:          `{"model":"claude-3","system":"` + claudeCodeSystemPrompt + `"}`,
			system:        json.RawMessage(`"` + claudeCodeSystemPrompt + `"`),
			wantSystemLen: 1,
			wantFirstText: claudeCodeSystemPrompt,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := injectClaudeCodePrompt([]byte(tt.body), tt.system)

			var parsed map[string]any
			err := json.Unmarshal(result, &parsed)
			require.NoError(t, err)

			system, ok := parsed["system"].([]any)
			require.True(t, ok, "system should be an array")
			require.Len(t, system, tt.wantSystemLen)

			first, ok := system[0].(map[string]any)
			require.True(t, ok)
			require.Equal(t, tt.wantFirstText, first["text"])
			require.Equal(t, "text", first["type"])

			// Check cache_control
			cc, ok := first["cache_control"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "ephemeral", cc["type"])

			if tt.wantSecondText != "" && len(system) > 1 {
				second, ok := system[1].(map[string]any)
				require.True(t, ok)
				require.Equal(t, tt.wantSecondText, second["text"])
			}
		})
	}
}

func TestRewriteSystemForNonClaudeCode(t *testing.T) {
	// 客户端 system 现在作为 system **尾块**保留（对齐真实 CLI 的
	// [billing, 身份块, ...调用方 system] 形态），不再搬成伪造的 user/assistant 消息对。
	// 因此 messages 恒等于原始条数，system 块数为 3（无客户端 system）或 4（有）。
	tests := []struct {
		name              string
		body              string
		system            any
		wantSystemText    string // system array 第二个 block（身份块）的 text
		wantMessagesLen   int    // messages 数组长度：应与原始 body 一致
		wantSystemBlocks  int    // system 块数
		wantTailBlockText string // 尾块（客户端 system）的 text，空表示无尾块
	}{
		{
			name:             "nil system - no tail block",
			body:             `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system:           nil,
			wantSystemText:   claudeCodeSystemPrompt,
			wantMessagesLen:  1,
			wantSystemBlocks: 3,
		},
		{
			name:             "empty string system - no tail block",
			body:             `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system:           "",
			wantSystemText:   claudeCodeSystemPrompt,
			wantMessagesLen:  1,
			wantSystemBlocks: 3,
		},
		{
			name:              "custom string system - kept as system tail block",
			body:              `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system:            "You are a personal assistant running inside OpenClaw.",
			wantSystemText:    claudeCodeSystemPrompt,
			wantMessagesLen:   1,
			wantSystemBlocks:  4,
			wantTailBlockText: "You are a personal assistant running inside OpenClaw.",
		},
		{
			name:             "system equals Claude Code prompt - identity stripped, no tail block",
			body:             `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system:           claudeCodeSystemPrompt,
			wantSystemText:   claudeCodeSystemPrompt,
			wantMessagesLen:  1,
			wantSystemBlocks: 3,
		},
		{
			name: "array system with custom blocks - text joined into tail block",
			body: `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system: []any{
				map[string]any{"type": "text", "text": "First instruction"},
				map[string]any{"type": "text", "text": "Second instruction"},
			},
			wantSystemText:    claudeCodeSystemPrompt,
			wantMessagesLen:   1,
			wantSystemBlocks:  4,
			wantTailBlockText: "First instruction\n\nSecond instruction",
		},
		{
			name:             "empty array system - no tail block",
			body:             `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system:           []any{},
			wantSystemText:   claudeCodeSystemPrompt,
			wantMessagesLen:  1,
			wantSystemBlocks: 3,
		},
		{
			name:              "json.RawMessage string system",
			body:              `{"model":"claude-3","system":"Custom prompt","messages":[{"role":"user","content":"hello"}]}`,
			system:            json.RawMessage(`"Custom prompt"`),
			wantSystemText:    claudeCodeSystemPrompt,
			wantMessagesLen:   1,
			wantSystemBlocks:  4,
			wantTailBlockText: "Custom prompt",
		},
		{
			name:             "json.RawMessage nil system",
			body:             `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system:           json.RawMessage(nil),
			wantSystemText:   claudeCodeSystemPrompt,
			wantMessagesLen:  1,
			wantSystemBlocks: 3,
		},
		{
			name:              "original messages left untouched",
			body:              `{"model":"claude-3","messages":[{"role":"user","content":"msg1"},{"role":"assistant","content":"resp1"},{"role":"user","content":"msg2"}]}`,
			system:            "Be helpful",
			wantSystemText:    claudeCodeSystemPrompt,
			wantMessagesLen:   3, // 原始 3 条，不再注入
			wantSystemBlocks:  4,
			wantTailBlockText: "Be helpful",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := rewriteSystemForNonClaudeCode([]byte(tt.body), tt.system)

			var parsed map[string]any
			err := json.Unmarshal(result, &parsed)
			require.NoError(t, err)

			// system 应为 array 格式，对齐真实 Claude Code CLI 的形态：
			//   [0] billing attribution block (x-anthropic-billing-header: cc_version=...;)
			//   [1] Claude Code 身份前缀 block (不带 cache_control)
			//   [2] 工具无关的通用提示词扩充 block (带 cache_control，作为缓存断点)
			//   [3] 客户端原 system（存在时），对应真实 CLI 的 "...调用方 system"
			systemArr, ok := parsed["system"].([]any)
			require.True(t, ok, "system should be an array, got %T", parsed["system"])
			require.Len(t, systemArr, tt.wantSystemBlocks)

			billingBlock, ok := systemArr[0].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "text", billingBlock["type"])
			require.Contains(t, billingBlock["text"], "x-anthropic-billing-header:")
			require.Contains(t, billingBlock["text"], "cc_version=")
			require.Contains(t, billingBlock["text"], "cc_entrypoint=cli")
			// cch 段不在本层注入：它由 normalizeBillingHeaderBlock 在转发前统一补齐，
			// 这样 mimic 与透传两条路径共用同一处收口。
			// （注意：真实 2.1.220 仍然发送 cch，只是值固定为 00000，
			//   见 docs/CC_2.1.220_EGRESS_SPEC.md §3。）
			require.NotContains(t, billingBlock["text"], "cch=")

			systemBlock, ok := systemArr[1].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "text", systemBlock["type"])
			require.Equal(t, tt.wantSystemText, systemBlock["text"])
			_, hasCC := systemBlock["cache_control"]
			require.False(t, hasCC, "身份前缀 block 不应带 cache_control（断点落在扩充块）")

			expansionBlock, ok := systemArr[2].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "text", expansionBlock["type"])
			require.Equal(t, claudeCodeSystemPromptExpansion, expansionBlock["text"])
			cc, ok := expansionBlock["cache_control"].(map[string]any)
			require.True(t, ok, "expansion block should have cache_control")
			require.Equal(t, "ephemeral", cc["type"])

			// 客户端 system 作为尾块保留
			if tt.wantTailBlockText != "" {
				tailBlock, ok := systemArr[len(systemArr)-1].(map[string]any)
				require.True(t, ok)
				require.Equal(t, "text", tailBlock["type"])
				require.Equal(t, tt.wantTailBlockText, tailBlock["text"])
			}

			// messages 必须原封不动——不再注入伪造的 user/assistant 对。
			// 这既是真实 CLI 的形态，也保证 cc_version 的 fp（按首条 user 消息计算）
			// 与最终发出的 body 一致。
			messages, ok := parsed["messages"].([]any)
			require.True(t, ok, "messages should be an array")
			require.Len(t, messages, tt.wantMessagesLen)

			raw := string(result)
			require.NotContains(t, raw, "[System Instructions]")
			require.NotContains(t, raw, "Understood. I will follow these instructions.")
		})
	}
}

func TestRewriteSystemForNonClaudeCodeWithPrompt_UsesCustomExpansionPrompt(t *testing.T) {
	body := []byte(`{"model":"claude-3","system":"Project instructions","messages":[{"role":"user","content":"hello"}]}`)
	customPrompt := "Custom Claude OAuth expansion prompt"

	result := rewriteSystemForNonClaudeCodeWithPrompt(body, "Project instructions", customPrompt)

	system := gjson.GetBytes(result, "system")
	require.True(t, system.IsArray())
	// [billing, 身份块, 自定义扩充块, 客户端 system 尾块]
	require.Len(t, system.Array(), 4)
	require.Equal(t, customPrompt, system.Array()[2].Get("text").String())
	require.Equal(t, "ephemeral", system.Array()[2].Get("cache_control.type").String())
	require.Equal(t, "Project instructions", system.Array()[3].Get("text").String())
}

// 客户端 system 上的 cache_control 断点（含其自选 TTL）在搬进 system 尾块时必须保留。
func TestRewriteSystemForNonClaudeCode_PreservesCacheControlOnTailBlock(t *testing.T) {
	body := []byte(`{"model":"claude-3","system":[{"type":"text","text":"Stable project instructions","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":"hello"}]}`)
	system := []any{
		map[string]any{
			"type":          "text",
			"text":          "Stable project instructions",
			"cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"},
		},
	}

	result := rewriteSystemForNonClaudeCode(body, system)

	tail := gjson.GetBytes(result, "system.3")
	require.Equal(t, "Stable project instructions", tail.Get("text").String())
	require.Equal(t, "ephemeral", tail.Get("cache_control.type").String())
	require.Equal(t, "1h", tail.Get("cache_control.ttl").String())
}

func TestRewriteSystemForNonClaudeCode_LeavesTailBlockUncachedWithoutSystemBreakpoint(t *testing.T) {
	body := []byte(`{"model":"claude-3","system":[{"type":"text","text":"Project instructions"}],"messages":[{"role":"user","content":"hello"}]}`)
	system := []any{
		map[string]any{"type": "text", "text": "Project instructions"},
	}

	result := rewriteSystemForNonClaudeCode(body, system)

	require.Equal(t, "Project instructions", gjson.GetBytes(result, "system.3.text").String())
	require.False(t, gjson.GetBytes(result, "system.3.cache_control").Exists())
}

func TestRewriteSystemForNonClaudeCodeWithPromptBlocks_UsesConfiguredBlocks(t *testing.T) {
	body := []byte(`{"model":"claude-3","system":"Project instructions","messages":[{"role":"user","content":"hello"}]}`)
	blocks := `{
		"blocks": [
			{"type":"text","text":"prefix {cc_version}.{fp}","cache_control":true},
			{"enabled":false,"type":"text","text":"disabled"},
			{"type":"text","text":"{claude_code_system_prompt}"},
			{"type":"text","text":"tail","cache_control":{"type":"ephemeral","ttl":"1h"}}
		]
	}`

	result := rewriteSystemForNonClaudeCodeWithPromptBlocks(body, "Project instructions", "", blocks)

	system := gjson.GetBytes(result, "system")
	require.True(t, system.IsArray())
	arr := system.Array()
	// 3 个启用的配置块 + 客户端 system 尾块
	require.Len(t, arr, 4)
	require.Contains(t, arr[0].Get("text").String(), "prefix "+claude.CLICurrentVersion+".")
	require.Equal(t, "ephemeral", arr[0].Get("cache_control.type").String())
	require.Equal(t, claude.DefaultCacheControlTTL, arr[0].Get("cache_control.ttl").String())
	require.Equal(t, claudeCodeSystemPrompt, arr[1].Get("text").String())
	require.False(t, arr[1].Get("cache_control").Exists())
	require.Equal(t, "tail", arr[2].Get("text").String())
	require.Equal(t, "1h", arr[2].Get("cache_control.ttl").String())
	require.Equal(t, "Project instructions", arr[3].Get("text").String())
}

// TestStripClaudeCodeIdentityPrefix 覆盖身份前缀剥离的各种形态。
func TestStripClaudeCodeIdentityPrefix(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"空串", "", ""},
		{"纯身份句", "You are Claude Code, Anthropic's official CLI for Claude.", ""},
		{"纯身份句带空白", "  You are Claude Code, Anthropic's official CLI for Claude.  ", ""},
		{"AgentSDK纯身份句", "You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.", ""},
		{"身份句+自定义_分段", "You are Claude Code, Anthropic's official CLI for Claude.\n\nAnswer in French.", "Answer in French."},
		{"身份句+自定义_同段", "You are Claude Code, Anthropic's official CLI for Claude. Answer in French.", "Answer in French."},
		{"AgentSDK+自定义_分段", "You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.\n\nAnswer in French.", "Answer in French."},
		{"AgentSDK+自定义_同段", "You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK. Answer in French.", "Answer in French."},
		{"Explore变体+自定义", "You are a file search specialist for Claude Code.\n\nAnswer in French.", "Answer in French."},
		{"Compact变体+自定义", "You are a helpful AI assistant tasked with summarizing conversations.\n\nAnswer in French.", "Answer in French."},
		{"无关内容原样返回", "You are a personal assistant running inside OpenClaw.", "You are a personal assistant running inside OpenClaw."},
		{"无关内容多段原样返回", "Be helpful.\n\nBe concise.", "Be helpful.\n\nBe concise."},
		{"身份句+多段自定义", "You are Claude Code, Anthropic's official CLI for Claude.\n\nRule one.\n\nRule two.", "Rule one.\n\nRule two."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, stripClaudeCodeIdentityPrefix(tt.in))
		})
	}
}

// TestRewriteSystemPreservesInstructionsAfterCCPrefix 锁住回归：客户端 system 以 Claude Code
// 身份句开头时，其后的自定义指令必须被保留在 system 尾块，不得静默丢弃。
func TestRewriteSystemPreservesInstructionsAfterCCPrefix(t *testing.T) {
	const body = `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`

	tests := []struct {
		name             string
		system           any
		wantSystemBlocks int
		wantInstruction  string // 空串表示不应产生尾块
	}{
		{
			name:             "纯身份句不产生尾块",
			system:           "You are Claude Code, Anthropic's official CLI for Claude.",
			wantSystemBlocks: 3,
		},
		{
			name:             "身份句+自定义_同段",
			system:           "You are Claude Code, Anthropic's official CLI for Claude. Answer in French.",
			wantSystemBlocks: 4,
			wantInstruction:  "Answer in French.",
		},
		{
			name:             "身份句+自定义_分段",
			system:           "You are Claude Code, Anthropic's official CLI for Claude.\n\nAnswer in French.",
			wantSystemBlocks: 4,
			wantInstruction:  "Answer in French.",
		},
		{
			name:             "AgentSDK变体+自定义",
			system:           "You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.\n\nAnswer in French.",
			wantSystemBlocks: 4,
			wantInstruction:  "Answer in French.",
		},
		{
			name: "数组_身份块在前_自定义块在后",
			system: []any{
				map[string]any{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
				map[string]any{"type": "text", "text": "Answer in French."},
			},
			wantSystemBlocks: 4,
			wantInstruction:  "Answer in French.",
		},
		{
			name: "数组_自定义块在前_身份块在后",
			system: []any{
				map[string]any{"type": "text", "text": "Answer in French."},
				map[string]any{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
			},
			wantSystemBlocks: 4,
			wantInstruction:  "Answer in French.\n\nYou are Claude Code, Anthropic's official CLI for Claude.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := rewriteSystemForNonClaudeCode([]byte(body), tt.system)

			var parsed map[string]any
			require.NoError(t, json.Unmarshal(result, &parsed))

			systemArr, ok := parsed["system"].([]any)
			require.True(t, ok)
			require.Len(t, systemArr, tt.wantSystemBlocks)

			// messages 恒不变
			messages, ok := parsed["messages"].([]any)
			require.True(t, ok)
			require.Len(t, messages, 1)

			if tt.wantInstruction == "" {
				return
			}
			tailBlock, ok := systemArr[len(systemArr)-1].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "text", tailBlock["type"])
			require.Equal(t, tt.wantInstruction, tailBlock["text"])
		})
	}
}
