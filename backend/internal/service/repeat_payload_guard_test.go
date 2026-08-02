//go:build unit

package service

import (
	"fmt"
	"strings"
	"testing"
)

func mustParseForFingerprint(t *testing.T, body string) *ParsedRequest {
	t.Helper()
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), "")
	if err != nil {
		t.Fatalf("解析请求失败: %v", err)
	}
	return parsed
}

// 同一份 payload 重复提交必须算出同一个指纹——这是整个拦截的前提。
func TestRepeatPayloadFingerprint_IdenticalPayloadIsStable(t *testing.T) {
	body := `{"model":"claude-fable-5","system":"You are a coding agent.","messages":[` +
		`{"role":"user","content":"把这份语料改写成训练样本"}]}`

	first, ok := RepeatPayloadFingerprint(mustParseForFingerprint(t, body))
	if !ok {
		t.Fatal("首次取指纹失败")
	}
	for i := 0; i < 5; i++ {
		got, ok := RepeatPayloadFingerprint(mustParseForFingerprint(t, body))
		if !ok {
			t.Fatalf("第 %d 次取指纹失败", i+2)
		}
		if got != first {
			t.Fatalf("第 %d 次指纹变了: %s != %s", i+2, got, first)
		}
	}
}

// 真实对话逐轮增长时指纹必须改变，否则正常 Claude Code 用户会被当成刷号。
//
// 这是本功能最重要的一条防误伤断言：滥用形态的特征正是 input_tokens 恒定不变，
// 而真实对话每轮在尾部追加内容。
func TestRepeatPayloadFingerprint_ChangesAsConversationGrows(t *testing.T) {
	turns := []string{
		`{"system":"You are a coding agent.","messages":[` +
			`{"role":"user","content":"帮我看下这个 bug"}]}`,

		`{"system":"You are a coding agent.","messages":[` +
			`{"role":"user","content":"帮我看下这个 bug"},` +
			`{"role":"assistant","content":[{"type":"text","text":"好的"}]},` +
			`{"role":"user","content":"继续"}]}`,

		`{"system":"You are a coding agent.","messages":[` +
			`{"role":"user","content":"帮我看下这个 bug"},` +
			`{"role":"assistant","content":[{"type":"text","text":"好的"}]},` +
			`{"role":"user","content":"继续"},` +
			`{"role":"assistant","content":[{"type":"text","text":"修好了"}]},` +
			`{"role":"user","content":"再看一处"}]}`,
	}

	seen := make(map[string]int, len(turns))
	for i, body := range turns {
		got, ok := RepeatPayloadFingerprint(mustParseForFingerprint(t, body))
		if !ok {
			t.Fatalf("turn %d 取指纹失败", i+1)
		}
		if prev, dup := seen[got]; dup {
			t.Fatalf("turn %d 与 turn %d 指纹相同 (%s)，对话增长必须改变指纹，否则真实用户会被误判",
				i+1, prev+1, got)
		}
		seen[got] = i
	}
}

// system 里的 billing block 每次请求都变（含 cc_prev_req），不得影响指纹——
// 否则永远不会命中，等于没做这个功能。
func TestRepeatPayloadFingerprint_IgnoresVolatileSystemBlock(t *testing.T) {
	const messages = `,"messages":[{"role":"user","content":"同一份 payload"}]}`

	bodyA := `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220.df2; cch=00000; cc_prev_req=req_011AAA;"}]` + messages
	bodyB := `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220.df2; cch=00000; cc_prev_req=req_011BBB;"}]` + messages

	fpA, okA := RepeatPayloadFingerprint(mustParseForFingerprint(t, bodyA))
	fpB, okB := RepeatPayloadFingerprint(mustParseForFingerprint(t, bodyB))
	if !okA || !okB {
		t.Fatal("取指纹失败")
	}
	if fpA != fpB {
		t.Fatalf("system 中的易变 billing block 影响了指纹: %s != %s", fpA, fpB)
	}
}

// messages 内容不同必须得到不同指纹。
func TestRepeatPayloadFingerprint_DiffersOnDifferentMessages(t *testing.T) {
	fpA, _ := RepeatPayloadFingerprint(mustParseForFingerprint(t,
		`{"messages":[{"role":"user","content":"第一份语料"}]}`))
	fpB, _ := RepeatPayloadFingerprint(mustParseForFingerprint(t,
		`{"messages":[{"role":"user","content":"第二份语料"}]}`))
	if fpA == fpB {
		t.Fatalf("不同 messages 得到了相同指纹: %s", fpA)
	}
}

// 取不到 messages 时必须返回 false，让调用方放行而不是拿空指纹去计数——
// 否则所有无 messages 的请求会共用同一个计数桶，互相打死。
func TestRepeatPayloadFingerprint_UnavailableCases(t *testing.T) {
	tests := []struct {
		name   string
		parsed *ParsedRequest
		wantOK bool
	}{
		{name: "nil parsed", parsed: nil, wantOK: false},
		{name: "无 messages 字段", parsed: mustParseForFingerprint(t, `{"model":"claude-fable-5"}`), wantOK: false},
		// 空数组是合法 JSON，允许有指纹；这条只保证不 panic、行为确定。
		{name: "空 messages 数组", parsed: mustParseForFingerprint(t, `{"messages":[]}`), wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := RepeatPayloadFingerprint(tt.parsed)
			if ok != tt.wantOK {
				t.Fatalf("可用性 = %v，期望 %v（指纹 %q）", ok, tt.wantOK, got)
			}
			if !ok && got != "" {
				t.Fatalf("不可用时应返回空指纹，得到 %q", got)
			}
		})
	}
}

// 滥用形态回归：固定 payload 反复提交 22 次，指纹必须 22 次全同。
// 数字取自 2026-08-01 账号 claude-e9b4a11a 的实测（17:04–17:59，input_tokens 恒为 266715）。
func TestRepeatPayloadFingerprint_ObservedAbuseShapeCollapsesToOneFingerprint(t *testing.T) {
	body := `{"model":"claude-fable-5","messages":[{"role":"user","content":"` +
		strings.Repeat("语料", 4096) + `"}]}`

	fingerprints := make(map[string]struct{})
	for i := 0; i < 22; i++ {
		got, ok := RepeatPayloadFingerprint(mustParseForFingerprint(t, body))
		if !ok {
			t.Fatalf("第 %d 次取指纹失败", i+1)
		}
		fingerprints[got] = struct{}{}
	}
	if len(fingerprints) != 1 {
		t.Fatalf("22 次相同提交产生了 %d 个不同指纹，应当只有 1 个", len(fingerprints))
	}
}

// scope 常量不得重复，否则 count_tokens 的计数会污染 /v1/messages。
func TestRepeatPayloadScopes_AreDistinct(t *testing.T) {
	if RepeatPayloadScopeMessages == RepeatPayloadScopeCountTokens {
		t.Fatal("messages 与 count_tokens 必须使用不同的命名空间")
	}
}

func BenchmarkRepeatPayloadFingerprint(b *testing.B) {
	body := fmt.Sprintf(`{"messages":[{"role":"user","content":%q}]}`, strings.Repeat("x", 256*1024))
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), "")
	if err != nil {
		b.Fatalf("解析失败: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := RepeatPayloadFingerprint(parsed); !ok {
			b.Fatal("指纹不可用")
		}
	}
}
