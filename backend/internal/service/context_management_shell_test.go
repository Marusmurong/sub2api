//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 剥掉 thinking 之后不能留下空的 context_management 壳。
//
// 真实 Claude Code 要么带着 edits 发这个字段，要么根本不发,不存在"发一个空对象"。
// 2026-09-10 出口抓包实测到过:客户端启用 thinking → 我们补 clear_thinking edit →
// 签名污染把 thinking 剥掉 → edits 被清空 → "context_management":{} 留在出站请求里。
func TestRemoveThinkingStrategiesDropsEmptyShell(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"messages":[]}`)
	out := removeThinkingDependentContextStrategies(body)
	require.False(t, gjson.GetBytes(out, "context_management").Exists(),
		"edits 清空后整个 context_management 应当删掉,而不是留个空壳")
	require.True(t, gjson.GetBytes(out, "model").Exists(), "其余字段不得受影响")
}

// 还有别的 edit 时只删 clear_thinking 那一条,字段保留。
func TestRemoveThinkingStrategiesKeepsOtherEdits(t *testing.T) {
	body := []byte(`{"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"},{"type":"compact_20250101"}]}}`)
	out := removeThinkingDependentContextStrategies(body)
	edits := gjson.GetBytes(out, "context_management.edits")
	require.True(t, edits.IsArray())
	require.Len(t, edits.Array(), 1)
	require.Equal(t, "compact_20250101", edits.Array()[0].Get("type").String())
}

// context_management 上还有别的键时,只删 edits,对象保留。
func TestRemoveThinkingStrategiesKeepsSiblingKeys(t *testing.T) {
	body := []byte(`{"context_management":{"edits":[{"type":"clear_thinking_20251015"}],"trigger":{"type":"input_tokens","value":1000}}}`)
	out := removeThinkingDependentContextStrategies(body)
	require.True(t, gjson.GetBytes(out, "context_management").Exists())
	require.False(t, gjson.GetBytes(out, "context_management.edits").Exists())
	require.True(t, gjson.GetBytes(out, "context_management.trigger").Exists())
}

// 没有 clear_thinking 时一个字节都不该动。
func TestRemoveThinkingStrategiesNoOp(t *testing.T) {
	body := []byte(`{"context_management":{"edits":[{"type":"compact_20250101"}]}}`)
	require.Equal(t, string(body), string(removeThinkingDependentContextStrategies(body)))
}
