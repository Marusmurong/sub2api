//go:build unit

package handler

import (
	"fmt"
	"strings"
	"testing"
)

// realProbeBody 复刻 2026-08-02 生产抓包拿到的真实请求，只替换末题。
// 保留 metadata 全零、标准 Claude Code system 块、max_tokens=50 等全部特征。
func realProbeBody(lastQuestion string) string {
	return `{"max_tokens":50,"messages":[{"content":"Calculate and respond with ONLY the number, nothing else.\n\n` +
		`Q: 3 + 5 = ?\nA: 8\n\nQ: 12 - 7 = ?\nA: 5\n\n` + lastQuestion + `\nA:","role":"user"}],` +
		`"metadata":{"user_id":"user_` + strings.Repeat("0", 64) +
		`_account_00000000-0000-0000-0000-000000000000_session_00000000-0000-0000-0000-000000000000"},` +
		`"model":"claude-haiku-4-5-20251001",` +
		`"system":[{"text":"You are Claude Code, Anthropic's official CLI for Claude.","type":"text"}]}`
}

// 抓到的四个真实末题都必须算对。
//
// 答对是硬要求，不是锦上添花：对方拿这道题验我们的上游有没有降智，
// 答错等于自证模型坏掉，比不拦更糟。
func TestDetectArithmeticProbe_AnswersObservedVariants(t *testing.T) {
	tests := []struct {
		question string
		want     string
	}{
		{question: "Q: 48 - 2 = ?", want: "46"},
		{question: "Q: 12 - 7 = ?", want: "5"},
		{question: "Q: 24 - 5 = ?", want: "19"},
		{question: "Q: 43 - 22 = ?", want: "21"},
	}
	for _, tt := range tests {
		t.Run(tt.question, func(t *testing.T) {
			got, ok := detectArithmeticProbe([]byte(realProbeBody(tt.question)))
			if !ok {
				t.Fatalf("真实探针未被识别: %s", tt.question)
			}
			if got != tt.want {
				t.Fatalf("答案 = %s，应为 %s", got, tt.want)
			}
		})
	}
}

// 必须取最后一道题。前两道是给格式的例题，已经带了答案；
// 取错会稳定回答 8，对方一眼看出是假的。
func TestDetectArithmeticProbe_UsesLastQuestionNotFirst(t *testing.T) {
	got, ok := detectArithmeticProbe([]byte(realProbeBody("Q: 100 - 1 = ?")))
	if !ok {
		t.Fatal("未识别")
	}
	if got == "8" || got == "5" {
		t.Fatalf("取到了例题的答案 %s，应取末题结果 99", got)
	}
	if got != "99" {
		t.Fatalf("答案 = %s，应为 99", got)
	}
}

func TestDetectArithmeticProbe_SupportsOperators(t *testing.T) {
	tests := []struct {
		question string
		want     string
	}{
		{question: "Q: 7 + 6 = ?", want: "13"},
		{question: "Q: 9 * 4 = ?", want: "36"},
		{question: "Q: 3 - 10 = ?", want: "-7"},
		{question: "Q: -5 + 12 = ?", want: "7"},
	}
	for _, tt := range tests {
		t.Run(tt.question, func(t *testing.T) {
			got, ok := detectArithmeticProbe([]byte(realProbeBody(tt.question)))
			if !ok {
				t.Fatalf("未识别: %s", tt.question)
			}
			if got != tt.want {
				t.Fatalf("答案 = %s，应为 %s", got, tt.want)
			}
		})
	}
}

// 算不出确定答案时必须放行到上游，而不是猜一个数字回去。
func TestDetectArithmeticProbe_FallsThroughWhenUnparseable(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"` + probeArithmeticPrefix +
		`\n\nQ: 一 加 二 = ?\nA:"}]}`
	if got, ok := detectArithmeticProbe([]byte(body)); ok {
		t.Fatalf("算式无法解析时应放行，却返回了 %s", got)
	}
}

// 判定必须窄：任何一个条件不符都放行。
func TestDetectArithmeticProbe_RejectsNonProbes(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "空 body", body: ""},
		{name: "非法 JSON", body: `{"messages":`},
		{name: "带 tools 的真实会话", body: `{"tools":[{"name":"Bash"}],"messages":[{"role":"user","content":"` +
			probeArithmeticPrefix + `\n\nQ: 1 + 1 = ?\nA:"}]}`},
		{name: "多条消息", body: `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},` +
			`{"role":"user","content":"` + probeArithmeticPrefix + `\n\nQ: 1 + 1 = ?\nA:"}]}`},
		{name: "assistant 角色", body: `{"messages":[{"role":"assistant","content":"` +
			probeArithmeticPrefix + `\n\nQ: 1 + 1 = ?\nA:"}]}`},
		{name: "开场白不符", body: `{"messages":[{"role":"user","content":"Solve this: Q: 1 + 1 = ?\nA:"}]}`},
		{name: "真人问算术", body: `{"messages":[{"role":"user","content":"帮我算一下 48 - 2 等于几?"}]}`},
		{name: "含图片块", body: `{"messages":[{"role":"user","content":[{"type":"image","source":{}}]}]}`},
		{name: "无 messages", body: `{"model":"claude-haiku-4-5-20251001"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, ok := detectArithmeticProbe([]byte(tt.body)); ok {
				t.Fatalf("不该命中，却返回了 %s", got)
			}
		})
	}
}

// 正文过长直接放行，避免拿正则去扫大 body。
func TestDetectArithmeticProbe_RejectsOversizedText(t *testing.T) {
	long := probeArithmeticPrefix + strings.Repeat("x", probeArithmeticMaxTextLen) + "\nQ: 1 + 1 = ?\nA:"
	body := fmt.Sprintf(`{"messages":[{"role":"user","content":%q}]}`, long)
	if got, ok := detectArithmeticProbe([]byte(body)); ok {
		t.Fatalf("超长正文应放行，却返回了 %s", got)
	}
}

// 内容为 text block 数组（而非裸字符串）时同样要识别——两种写法客户端都可能用。
func TestDetectArithmeticProbe_AcceptsTextBlockArray(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"` + probeArithmeticPrefix +
		`\n\nQ: 20 - 6 = ?\nA:"}]}]}`
	got, ok := detectArithmeticProbe([]byte(body))
	if !ok {
		t.Fatal("text block 数组形式未被识别")
	}
	if got != "14" {
		t.Fatalf("答案 = %s，应为 14", got)
	}
}
