//go:build unit

package handler

import "testing"

// max_tokens=1 探针必须在本地被接掉,不能落到上游账号上。
//
// 上游 4e5d67df3 放宽了 claude_code_only 校验,让非 haiku 的探针"当普通 1-token
// 请求"进号池;对我们那是每个账号平白多一批探针请求(占并发槽、进 RPM、写用量)。
func TestMaxTokensOneProbeInterception(t *testing.T) {
	const ccSystem = `[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}]`

	cases := []struct {
		name      string
		body      string
		model     string
		maxTokens int
		want      bool
	}{
		{"haiku 探针(历史形态)", `{"messages":[{"role":"user","content":"x"}]}`, "claude-haiku-4-5-20251001", 1, true},
		{"opus 探针(2.1.26x 新形态,无 system)", `{"messages":[{"role":"user","content":"x"}]}`, "claude-opus-5", 1, true},
		{"sonnet 探针(无 system)", `{"messages":[{"role":"user","content":"x"}]}`, "claude-sonnet-5", 1, true},
		{"haiku 带 system 仍拦(既有行为不变)", `{"system":` + ccSystem + `,"messages":[]}`, "claude-haiku-4-5-20251001", 1, true},
		{"非 haiku 带 system 不拦(可能是真要 1 token)", `{"system":` + ccSystem + `,"messages":[]}`, "claude-opus-5", 1, false},
		{"max_tokens 不是 1 不拦", `{"messages":[{"role":"user","content":"x"}]}`, "claude-opus-5", 4096, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMaxTokensOneProbeRequest([]byte(tc.body), tc.model, tc.maxTokens); got != tc.want {
				t.Fatalf("isMaxTokensOneProbeRequest = %v, want %v", got, tc.want)
			}
		})
	}
}

// 非 Claude Code 客户端不走这条拦截:detectInterceptType 的 isClaudeCodeClient 门槛还在。
func TestMaxTokensOneProbeRequiresClaudeCodeClient(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"x"}]}`)
	if got := detectInterceptType(body, "claude-opus-5", 1, false); got != InterceptTypeNone {
		t.Fatalf("非 CC 客户端不应被拦截, got %v", got)
	}
	if got := detectInterceptType(body, "claude-opus-5", 1, true); got != InterceptTypeMaxTokensOneHaiku {
		t.Fatalf("CC 客户端的 max_tokens=1 探针应被拦截, got %v", got)
	}
}
