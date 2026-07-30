//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// StripEmptyTextBlocks 的本意是"删掉空文本块以免上游 400"，但删块本身能把消息删空，
// 换来另一个 400：`messages.N: user messages must have non-empty content`。
// 生产上每天几十次都出自这里。

func TestStripEmptyTextBlocks_NeverEmptiesAMessage(t *testing.T) {
	// 一条 user 消息整条只有空文本块 —— 清洗后不能变成 content: []
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":[{"type":"text","text":""}]}
	]}`)

	out := StripEmptyTextBlocks(body)

	content := gjson.GetBytes(out, "messages.0.content").Array()
	require.Len(t, content, 1, "不得把消息删空")
	require.Equal(t, "text", content[0].Get("type").String())
	require.Equal(t, "(content removed)", content[0].Get("text").String())
}

func TestStripEmptyTextBlocks_AssistantPlaceholderIsDistinct(t *testing.T) {
	// 注意只有严格空串算空块；纯空格块会被保留（strip 只判 txt == ""）
	body := []byte(`{"model":"m","messages":[
		{"role":"assistant","content":[{"type":"text","text":""},{"type":"text","text":""}]}
	]}`)

	out := StripEmptyTextBlocks(body)

	content := gjson.GetBytes(out, "messages.0.content").Array()
	require.Len(t, content, 1)
	// 区分 role 便于排查是哪一侧的内容被清掉
	require.Equal(t, "(assistant content removed)", content[0].Get("text").String())
}

// 只删掉部分空块时不应触发占位符——占位会污染真实内容。
func TestStripEmptyTextBlocks_KeepsRemainingContentUntouched(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":[{"type":"text","text":""},{"type":"text","text":"真实内容"}]}
	]}`)

	out := StripEmptyTextBlocks(body)

	content := gjson.GetBytes(out, "messages.0.content").Array()
	require.Len(t, content, 1)
	require.Equal(t, "真实内容", content[0].Get("text").String())
	require.NotContains(t, string(out), "content removed", "还有内容时不得插占位符")
}

// 没有空块时必须原样返回（快路径），避免无谓改写影响 prompt cache 前缀。
func TestStripEmptyTextBlocks_NoEmptyBlocksUnchanged(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	require.Equal(t, string(body), string(StripEmptyTextBlocks(body)))
}

// 多条消息里只有一条被清空时，其余消息不受影响。
func TestStripEmptyTextBlocks_OnlyEmptiedMessageGetsPlaceholder(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":[{"type":"text","text":"第一条"}]},
		{"role":"user","content":[{"type":"text","text":""}]},
		{"role":"assistant","content":[{"type":"text","text":"第三条"}]}
	]}`)

	out := StripEmptyTextBlocks(body)

	require.Equal(t, "第一条", gjson.GetBytes(out, "messages.0.content.0.text").String())
	require.Equal(t, "(content removed)", gjson.GetBytes(out, "messages.1.content.0.text").String())
	require.Equal(t, "第三条", gjson.GetBytes(out, "messages.2.content.0.text").String())
}

// ===== 出口兜底：ensureNonEmptyMessageContent =====
//
// 上游对空 content 一律 400。空内容有两个来源，逐点加守卫会漏——
// filterThinkingBlocksInternal 就漏了（占位符逻辑只写在 ForRetry 版本里），
// 客户端直接发 content:[] 更是没人管。所以在出口做统一兜底。

func TestEnsureNonEmptyMessageContent_ClientSentEmptyArray(t *testing.T) {
	body := []byte(`{"model":"m","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"hi"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"ok"}]},` +
		`{"role":"user","content":[]}]}`)

	out := ensureNonEmptyMessageContent(body)

	require.Equal(t, "(content removed)",
		gjson.GetBytes(out, "messages.2.content.0.text").String(),
		"客户端原样发来的空 content 必须被兜住")
	require.Equal(t, "hi", gjson.GetBytes(out, "messages.0.content.0.text").String())
	require.Equal(t, "ok", gjson.GetBytes(out, "messages.1.content.0.text").String())
}

func TestEnsureNonEmptyMessageContent_AssistantEmptiedByThinkingStrip(t *testing.T) {
	// thinking=disabled 时 assistant 的 thinking 块被剥离，整条消息变空
	body := []byte(`{"model":"claude-opus-4-8","thinking":{"type":"disabled"},"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"hi"}]},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"s"}]}]}`)

	stripped := FilterThinkingBlocks(body, "claude-opus-4-8")
	require.Empty(t, gjson.GetBytes(stripped, "messages.1.content").Array(),
		"前提：剥离确实会把 assistant 清空（此处不修它，由出口兜底）")

	out := ensureNonEmptyMessageContent(stripped)
	require.Equal(t, "(assistant content removed)",
		gjson.GetBytes(out, "messages.1.content.0.text").String())
}

// 无空内容时必须原样返回：反序列化会打乱字段顺序，影响 prompt cache 前缀。
func TestEnsureNonEmptyMessageContent_NoEmptyContentUntouched(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	require.Equal(t, string(body), string(ensureNonEmptyMessageContent(body)))
}

// content 是字符串（而非数组）时不受影响
func TestEnsureNonEmptyMessageContent_StringContentUntouched(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[]}]}`)
	out := ensureNonEmptyMessageContent(body)
	require.Equal(t, "hi", gjson.GetBytes(out, "messages.0.content").String())
	require.Equal(t, "(assistant content removed)", gjson.GetBytes(out, "messages.1.content.0.text").String())
}
