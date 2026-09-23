package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// redacted_thinking 块带的是 data 而不是 signature —— 按签名判据会被当成
// 「无有效签名」一律删掉。
//
// 🔴 删掉它是错的：Anthropic 的 signature 校验以「历史原样回传」为前提，
// 少一个块就破坏了这个前提，换来
//
//	400 messages.N.content.M: Invalid `signature` in `thinking` block
//
// 这条由上游 v0.2.8 的 Opus 5.5 用例发现（adaptive thinking 下 redacted_thinking
// 必须保留）。合并时择优采纳上游语义。
func TestFilterThinkingBlocksKeepsRedactedThinking(t *testing.T) {
	t.Run("adaptive 下 assistant 的 redacted_thinking 保留", func(t *testing.T) {
		body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"signed"},{"type":"redacted_thinking","data":"encrypted"}]}],"thinking":{"type":"adaptive"}}`)

		out := FilterThinkingBlocks(body, "claude-opus-5-5")

		require.Contains(t, string(out), "redacted_thinking", "带 data 的块被删会破坏签名校验前提")
		require.Equal(t, string(body), string(out), "整体应逐字不变")
	})

	t.Run("enabled 下同样保留", func(t *testing.T) {
		body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":"encrypted"}]}],"thinking":{"type":"enabled"}}`)

		out := FilterThinkingBlocks(body, "claude-sonnet-4-5")

		require.Contains(t, string(out), "redacted_thinking")
	})

	t.Run("data 为空的 redacted_thinking 仍然删除", func(t *testing.T) {
		// 空 data 没有回传价值，且可能是我们自己改写链的产物。
		body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":""},{"type":"text","text":"hi"}]}],"thinking":{"type":"adaptive"}}`)

		out := FilterThinkingBlocks(body, "claude-opus-5-5")

		require.NotContains(t, string(out), "redacted_thinking")
	})

	t.Run("thinking 未启用时仍然删除", func(t *testing.T) {
		// 上游不接受「thinking 关闭却带 thinking 块」。
		body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":"encrypted"},{"type":"text","text":"hi"}]}]}`)

		out := FilterThinkingBlocks(body, "claude-sonnet-4-5")

		require.NotContains(t, string(out), "redacted_thinking")
	})

	t.Run("user 消息里的 redacted_thinking 仍然删除", func(t *testing.T) {
		// 保留条件限定 assistant：user 侧不存在合法的 thinking 历史。
		body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":[{"type":"redacted_thinking","data":"encrypted"},{"type":"text","text":"hi"}]}],"thinking":{"type":"adaptive"}}`)

		out := FilterThinkingBlocks(body, "claude-opus-5-5")

		require.NotContains(t, string(out), "redacted_thinking")
	})

	t.Run("无效签名的 thinking 块照常删除（不受本改动影响）", func(t *testing.T) {
		body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":""},{"type":"text","text":"hi"}]}],"thinking":{"type":"adaptive"}}`)

		out := FilterThinkingBlocks(body, "claude-opus-5-5")

		require.False(t, gjson.GetBytes(out, `messages.0.content.#(type=="thinking")`).Exists())
	})
}

// Opus 5.5 的 thinking 默认就是 adaptive —— 请求**不带**顶层 thinking 字段时，
// 历史 thinking/redacted_thinking 块同样要保留。
//
// 🔴 filterThinkingBlocksInternal 收了 alwaysThinking 参数却没用上（合并 v0.2.8
// 时发现）：只看 thinking.type 会把「省略 thinking 的 Opus 5.5 请求」判成未启用，
// 于是把带签名的历史块整批删掉，破坏 signature 校验前提。
func TestFilterThinkingBlocksOpus55DefaultsToAdaptive(t *testing.T) {
	t.Run("不带 thinking 字段时历史块仍保留", func(t *testing.T) {
		body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"signed"},{"type":"redacted_thinking","data":"encrypted"}]}]}`)

		out := FilterThinkingBlocks(body, "claude-opus-5-5")

		require.Equal(t, string(body), string(out), "Opus 5.5 省略 thinking 即 adaptive，历史块不该被删")
	})

	t.Run("非 Opus 5.5 不带 thinking 字段时照常删除", func(t *testing.T) {
		// 其它模型省略 thinking 就是未启用，带 thinking 块会被上游拒绝。
		body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"signed"},{"type":"text","text":"hi"}]}]}`)

		out := FilterThinkingBlocks(body, "claude-sonnet-4-5")

		require.NotContains(t, string(out), `"type":"thinking"`)
	})
}
