package service

import (
	"strings"
	"testing"
)

// 跨账号复用会话时，历史 thinking 块的签名由前一个账号签发，当前账号必然拒绝
// （生产 2026-07-28 每天约 595 次 "Invalid `signature` in `thinking` block"）。
// 这些用例覆盖"何时该剥离"的判定，剥离本身复用既有的 FilterThinkingBlocksForRetry。

func TestStripThinkingForAccountSwitch_Condition(t *testing.T) {
	const body = `{"model":"claude-opus-4-8","messages":[
		{"role":"assistant","content":[{"type":"thinking","thinking":"reasoning here","signature":"sig-from-account-A"}]},
		{"role":"user","content":"continue"}]}`

	tests := []struct {
		name        string
		bound       int64
		selected    int64
		wantApplied bool
		reason      string
	}{
		{
			name: "账号切换时剥离", bound: 12, selected: 13,
			wantApplied: true, reason: "签名由账号 12 签发,账号 13 必然拒绝",
		},
		{
			name: "同账号不动", bound: 12, selected: 12,
			wantApplied: false, reason: "签名有效,且剥离会打掉 prompt 缓存命中",
		},
		{
			name: "无粘性绑定不动", bound: 0, selected: 13,
			wantApplied: false, reason: "首次请求,无历史账号",
		},
		{
			name: "绑定为负值不动", bound: -1, selected: 13,
			wantApplied: false, reason: "防御非法值",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, applied := stripThinkingForAccountSwitch([]byte(body), "claude-opus-4-8", tt.bound, tt.selected)

			if applied != tt.wantApplied {
				t.Fatalf("applied = %v, want %v (%s)", applied, tt.wantApplied, tt.reason)
			}
			if !applied && string(got) != body {
				t.Errorf("未触发时必须原样返回 body")
			}
			if applied && strings.Contains(string(got), "sig-from-account-A") {
				t.Errorf("触发后不得残留跨账号签名: %s", got)
			}
		})
	}
}

func TestStripThinkingForAccountSwitch_PreservesNonThinking(t *testing.T) {
	// 无 thinking 块的请求即使账号切换也不该被改动
	body := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}]}`)

	got, applied := stripThinkingForAccountSwitch(body, "claude-opus-4-8", 12, 13)

	if applied {
		t.Errorf("applied = true, want false（无 thinking 块无需剥离）")
	}
	if string(got) != string(body) {
		t.Errorf("body 不应被改动: %s", got)
	}
}

func TestStripThinkingForAccountSwitch_PassbackRequiredUpstream(t *testing.T) {
	// passback-required 上游（DeepSeek/Kimi/GLM 等）要求历史 thinking 原样回传，
	// 剥离反而制造 400。FilterThinkingBlocksForRetry 内部按 ShouldApplyRetryFilters 把关，
	// 这里确认该约束在账号切换路径上同样成立。
	body := []byte(`{"model":"deepseek-chat","messages":[
		{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"s"}]}]}`)

	_, applied := stripThinkingForAccountSwitch(body, "deepseek-chat", 12, 13)

	if applied {
		t.Errorf("applied = true, want false（passback-required 上游不得剥离）")
	}
}

func TestStripThinkingForAccountSwitch_EmptyAndInvalidBody(t *testing.T) {
	if _, applied := stripThinkingForAccountSwitch(nil, "claude-opus-4-8", 12, 13); applied {
		t.Errorf("nil body: applied = true, want false")
	}
	if _, applied := stripThinkingForAccountSwitch([]byte(`not json`), "claude-opus-4-8", 12, 13); applied {
		t.Errorf("invalid json: applied = true, want false")
	}
}
