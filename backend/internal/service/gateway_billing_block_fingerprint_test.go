package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"unicode/utf16"
)

// referenceFingerprint 是按真实 CLI 语义（JS 字符串下标 = UTF-16 code unit）
// 独立实现的一份参考算法，用于交叉验证 fingerprintSampleChars 的取样。
// 它刻意不复用被测代码的辅助函数。
func referenceFingerprint(text, version string) string {
	units := utf16.Encode([]rune(text))
	chars := ""
	for _, i := range []int{4, 7, 20} {
		switch {
		case i >= len(units):
			chars += "0"
		case units[i] >= 0xD800 && units[i] <= 0xDFFF:
			chars += "�"
		default:
			chars += string(rune(units[i]))
		}
	}
	sum := sha256.Sum256([]byte(fingerprintSalt + chars + version))
	return hex.EncodeToString(sum[:])[:3]
}

func TestFingerprintSampleCharsIndexesUTF16CodeUnits(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "ASCII 逐字符取样",
			text: "Please translate this sentence into English",
			want: "sts",
		},
		{
			// 回归用例：字节索引会取到 UTF-8 续字节（\xa5\xbc\x8a）而非字符。
			// 索引 4/7 落在 "帮"/"这"，索引 20 越界补 '0'。
			name: "中文按字符而非字节取样",
			text: "你好，请帮我把这段话翻译成英文",
			want: "帮这0",
		},
		{
			name: "短文本全部越界补零",
			text: "abc",
			want: "000",
		},
		{
			name: "空文本",
			text: "",
			want: "000",
		},
		{
			// emoji 各占 2 个 UTF-16 code unit，units 为：
			//   a b D83C DF89 D83C DF89 c d e f g h i j   （共 14 个）
			// 索引 4 落在第二个 emoji 的高代理项上 → 与 JS 的 text[4] 一样得到
			// 孤立代理项 → U+FFFD；索引 7 是 'd'；索引 20 越界补 '0'。
			name: "星外字符取到代理对返回替换字符",
			text: "ab🎉🎉cdefghij",
			want: "�d0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fingerprintSampleChars(tt.text); got != tt.want {
				t.Errorf("fingerprintSampleChars(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestComputeClaudeCodeFingerprintMatchesReference(t *testing.T) {
	texts := []string{
		"Please translate this sentence into English",
		"你好，请帮我把这段话翻译成英文",
		"帮我看看这段代码为什么会 panic，顺便解释一下原因",
		"abc",
		"",
		"ab🎉🎉cdefghij",
		"Fix the bug in src/main.go:42",
	}

	const version = "2.1.220"
	for _, text := range texts {
		body := buildFingerprintTestBody(t, text)
		got := computeClaudeCodeFingerprint(body, version)
		want := referenceFingerprint(text, version)
		if got != want {
			t.Errorf("computeClaudeCodeFingerprint(first_user_text=%q) = %q, want %q", text, got, want)
		}
		if len(got) != 3 {
			t.Errorf("fingerprint 长度应为 3，得到 %q", got)
		}
	}
}

// TestComputeClaudeCodeFingerprintRejectsByteIndexing 固化本次修复：
// 中文首条消息下，字节索引与字符索引必须产生不同的结果。
// 若将来有人把实现改回字节索引，这条会失败。
func TestComputeClaudeCodeFingerprintRejectsByteIndexing(t *testing.T) {
	const text = "你好，请帮我把这段话翻译成英文"
	const version = "2.1.220"

	byteIndexed := make([]byte, 0, 3)
	for _, i := range []int{4, 7, 20} {
		if i < len(text) {
			byteIndexed = append(byteIndexed, text[i])
		} else {
			byteIndexed = append(byteIndexed, '0')
		}
	}
	sum := sha256.Sum256([]byte(fingerprintSalt + string(byteIndexed) + version))
	legacy := hex.EncodeToString(sum[:])[:3]

	got := computeClaudeCodeFingerprint(buildFingerprintTestBody(t, text), version)
	if got == legacy {
		t.Fatalf("中文消息仍在按字节取样：fingerprint=%q 与旧实现一致", got)
	}
}

func buildFingerprintTestBody(t *testing.T, firstUserText string) []byte {
	t.Helper()
	body, err := marshalFingerprintTestBody(firstUserText)
	if err != nil {
		t.Fatalf("构造测试 body 失败: %v", err)
	}
	if got := extractFirstUserText(body); got != firstUserText {
		t.Fatalf("extractFirstUserText = %q, want %q", got, firstUserText)
	}
	return body
}

func marshalFingerprintTestBody(firstUserText string) ([]byte, error) {
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type payload struct {
		Messages []message `json:"messages"`
	}
	return json.Marshal(payload{Messages: []message{{Role: "user", Content: firstUserText}}})
}

// 真实 CLI 取样的是「第一条 非 meta 的 user 消息」的首个 text 块
// （2.1.263 二进制原文：e.find(o => o.type==="user" && !o.isMeta)）。
// meta 消息在线上形态里是 <system-reminder>/<command-*>/<local-command-stdout>
// 开头的 text 块，与真正的用户输入合并在同一条 user 消息里、排在前面。
// 期望值 ed3 / e10 来自 2026-09-07 本机真实 2.1.263 抓包（-p 模式，
// messages[0].content = [system-reminder 块, 用户输入块]）。
func TestExtractFirstUserTextSkipsMetaBlocksLikeRealCLI(t *testing.T) {
	sysReminder := "<system-reminder>\nAs you answer the user's questions, you can use the following context:\n# claudeMd\n...\n</system-reminder>\n\n"
	mk := func(blocks ...string) []byte {
		content := make([]map[string]string, 0, len(blocks))
		for _, b := range blocks {
			content = append(content, map[string]string{"type": "text", "text": b})
		}
		body, _ := json.Marshal(map[string]any{"messages": []map[string]any{{"role": "user", "content": content}}})
		return body
	}

	cases := []struct {
		name     string
		body     []byte
		wantText string
		wantFP   string
	}{
		{"system-reminder 在前，取用户输入 hi", mk(sysReminder, "hi"), "hi", "ed3"},
		{"system-reminder 在前，取较长提示词", mk(sysReminder, "please summarize the theory of relativity in three sentences for a curious teenager"), "please summarize the theory of relativity in three sentences for a curious teenager", "e10"},
		{"没有 meta 块时行为不变", mk("hi"), "hi", "ed3"},
		{"多个 meta 块都跳过", mk(sysReminder, "<local-command-stdout>ok</local-command-stdout>", sysReminder, "hi"), "hi", "ed3"},
		{"斜杠命令展开块是 meta", mk("<command-message>review is running…</command-message>\n<command-name>/review</command-name>", "hi"), "hi", "ed3"},
		{"字符串 content 直接取", []byte(`{"messages":[{"role":"user","content":"hi"}]}`), "hi", "ed3"},
		{"标签前有空白也算 meta", mk("\n  \t<system-reminder>x</system-reminder>", "hi"), "hi", "ed3"},
		{"非 meta 消息里 text 前有 image 块，取 text", []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>x</system-reminder>"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}},{"type":"text","text":"hi"}]}]}`), "hi", "ed3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractFirstUserText(tc.body); got != tc.wantText {
				t.Fatalf("extractFirstUserText = %q, want %q", got, tc.wantText)
			}
			if got := computeClaudeCodeFingerprint(tc.body, "2.1.263"); got != tc.wantFP {
				t.Fatalf("fingerprint = %q, want %q（真实 2.1.263 抓包值）", got, tc.wantFP)
			}
		})
	}
}

// 第一条 user 消息只有 meta 块时，继续找下一条 user 消息（镜像 CLI 的 find 语义）。
func TestExtractFirstUserTextFallsThroughAllMetaMessage(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":[{"type":"text","text":"<system-reminder>x</system-reminder>"}]},
		{"role":"assistant","content":[{"type":"text","text":"ok"}]},
		{"role":"user","content":[{"type":"text","text":"second prompt"}]}]}`)
	if got := extractFirstUserText(body); got != "second prompt" {
		t.Fatalf("got %q, want %q", got, "second prompt")
	}
}

// 第一条非 meta 的 user 消息没有 text 块时，CLI 的 find 已经命中它并返回空串，
// 不会再往后找——线上等价于：跳过 meta 块后遇到 tool_result/空数组就停。
func TestExtractFirstUserTextStopsAtNonMetaMessageWithoutText(t *testing.T) {
	cases := map[string]string{
		"tool_result 消息":      `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"ok"}]},{"role":"assistant","content":[{"type":"text","text":"..."}]},{"role":"user","content":[{"type":"text","text":"later"}]}]}`,
		"meta 后跟 tool_result": `{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>x</system-reminder>"},{"type":"tool_result","tool_use_id":"x","content":"ok"}]},{"role":"user","content":[{"type":"text","text":"later"}]}]}`,
		"空数组":                 `{"messages":[{"role":"user","content":[]},{"role":"user","content":[{"type":"text","text":"later"}]}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if got := extractFirstUserText([]byte(body)); got != "" {
				t.Fatalf("got %q, want empty (CLI find 命中无 text 的消息即停止)", got)
			}
		})
	}
}
