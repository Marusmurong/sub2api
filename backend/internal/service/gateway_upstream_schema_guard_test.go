package service

import (
	"testing"

	"github.com/tidwall/gjson"
)

// /v1/messages/count_tokens 是严格 schema：出现它不认识的字段直接 400
// `Extra inputs are not permitted`。我们为对齐真实 CLI 而补的 max_tokens /
// temperature 恰好不被该端点接受，因此 count_tokens 路径必须不补。
// 对应上游未合并的 PR #4913。
func TestNormalizeSkipsMessagesOnlyFieldsForCountTokens(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)

	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-opus-5", claudeOAuthNormalizeOptions{
		stripSystemCacheControl: true,
		skipMessagesOnlyFields:  true,
	})

	if gjson.GetBytes(out, "max_tokens").Exists() {
		t.Errorf("count_tokens 路径不应补 max_tokens: %s", out)
	}
	if gjson.GetBytes(out, "temperature").Exists() {
		t.Errorf("count_tokens 路径不应补 temperature: %s", out)
	}
	// tools 仍应补齐——count_tokens 接受 tools
	if !gjson.GetBytes(out, "tools").IsArray() {
		t.Errorf("tools 应仍被补为数组: %s", out)
	}
}

// /v1/messages 路径不受影响，仍补齐两字段以对齐真实 CLI。
func TestNormalizeStillFillsMessagesOnlyFieldsByDefault(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)

	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-opus-5", claudeOAuthNormalizeOptions{})

	if got := gjson.GetBytes(out, "max_tokens").Int(); got != claudeCodeDefaultMaxTokens {
		t.Errorf("max_tokens = %d, want %d", got, claudeCodeDefaultMaxTokens)
	}
	// temperature 不再补齐：实测真实 2.1.220 的两个入口都不发这个字段，
	// 补一个真实客户端从不发送的字段等于给每个请求盖我们自己的戳。
	if gjson.GetBytes(out, "temperature").Exists() {
		t.Error("不应补 temperature —— 真实 Claude Code 不发该字段")
	}
	// output_config 同理不补，但原因不同：它不是「真 CC 不发」，而是「补上去会 400」。
	// effort 并非所有模型都接受，2026-08-03 生产上补了当天就打出
	// "This model does not support the effort parameter."
	if gjson.GetBytes(out, "output_config").Exists() {
		t.Error("不应补 output_config —— 不支持 effort 的模型会直接 400")
	}
}

// 客户端自己带了值时，两条路径都应透传而非删除。
func TestNormalizeKeepsClientProvidedFieldsOnCountTokens(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)

	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-opus-5", claudeOAuthNormalizeOptions{
		skipMessagesOnlyFields: true,
	})

	if got := gjson.GetBytes(out, "max_tokens").Int(); got != 100 {
		t.Errorf("客户端自带的 max_tokens 被改动: %d", got)
	}
}

// 上游对 defer_loading=true 且带 cache_control 的 tool 直接 400：
// `Tool 'X' cannot have both defer_loading=true and cache_control set`。
// 对应上游未合并的 issue #4990 / PR #5004。
func TestApplyToolsLastCacheBreakpointAvoidsDeferredTools(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantCCIndex int // 期望带 cache_control 的 tool 下标；-1 表示都不带
	}{
		{
			name:        "末位是延迟加载 → 断点前移到可承载的 tool",
			body:        `{"tools":[{"name":"a"},{"name":"b","defer_loading":true}]}`,
			wantCCIndex: 0,
		},
		{
			name:        "连续多个延迟加载 → 继续前移",
			body:        `{"tools":[{"name":"a"},{"name":"b","defer_loading":true},{"name":"c","defer_loading":true}]}`,
			wantCCIndex: 0,
		},
		{
			name:        "全部延迟加载 → 不打断点，宁可少缓存也不要 400",
			body:        `{"tools":[{"name":"a","defer_loading":true},{"name":"b","defer_loading":true}]}`,
			wantCCIndex: -1,
		},
		{
			name:        "无延迟加载 → 落在末位（原行为）",
			body:        `{"tools":[{"name":"a"},{"name":"b"}]}`,
			wantCCIndex: 1,
		},
		{
			name:        "defer_loading=false 视为可承载",
			body:        `{"tools":[{"name":"a"},{"name":"b","defer_loading":false}]}`,
			wantCCIndex: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := applyToolsLastCacheBreakpoint([]byte(tt.body))
			tools := gjson.GetBytes(out, "tools").Array()

			for i, tool := range tools {
				hasCC := tool.Get("cache_control").Exists()
				if i == tt.wantCCIndex && !hasCC {
					t.Errorf("tools[%d] 应带 cache_control: %s", i, out)
				}
				if i != tt.wantCCIndex && hasCC {
					t.Errorf("tools[%d] 不应带 cache_control: %s", i, out)
				}
				// 绝不允许同时出现 defer_loading=true 与 cache_control
				if tool.Get("defer_loading").Bool() && hasCC {
					t.Errorf("tools[%d] 同时带 defer_loading 与 cache_control，上游会 400: %s", i, out)
				}
			}
		})
	}
}
