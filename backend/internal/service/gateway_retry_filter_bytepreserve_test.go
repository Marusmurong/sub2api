//go:build unit

package service

import (
	"testing"

	"github.com/tidwall/gjson"
)

// FilterThinkingBlocksForRetry 是今天唯一还在做整包 Unmarshal/Marshal 往返的过滤器。
//
// 它的调用频率被污染标记机制放大了：一个被标记的会话每一轮都会走这里。生产实测
// 账号 57（15 小时、279 个请求）累计剥掉 24997 个 thinking 块，单次中位 180 个、
// 最多 238 个，而 body 上下文常达 40~60 万 token——每一轮都把这么大的 body 完整
// 反序列化再序列化一遍。
//
// 往返还有正确性代价：它会重排所有 key、把字符串里的 < > & 转义成 < &
// >，连没打算动的 tool_use 一起改写。同一类问题今天已经在
// StripEmptyTextBlocks / FilterThinkingBlocks / FilterWebSearchHistoryBlocks
// 上修过三次。
func TestFilterThinkingBlocksForRetry_PreservesUntouchedBlocks(t *testing.T) {
	const toolUse = `{"type":"tool_use","id":"toolu_01ABC","name":"Bash","input":{"cmd":"echo a && echo b"}}`
	const keepText = `{"type":"text","text":"x < y & z"}`

	body := `{"model":"claude-opus-4-8","thinking":{"type":"enabled","budget_tokens":1024},"messages":[` +
		`{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"reasoning a < b","signature":"sig-1"},` +
		toolUse + `,` + keepText + `]},` +
		`{"role":"user","content":[{"type":"text","text":"go on"}]}]}`

	out := FilterThinkingBlocksForRetry([]byte(body), "claude-opus-4-8")

	if gjson.GetBytes(out, "thinking").Exists() {
		t.Errorf("顶层 thinking 应被删除: %s", gjson.GetBytes(out, "thinking").Raw)
	}

	blocks := gjson.GetBytes(out, "messages.0.content").Array()
	if len(blocks) != 3 {
		t.Fatalf("块数 = %d, want 3（thinking 转 text，其余保留）", len(blocks))
	}
	if got := blocks[0].Raw; gjson.Get(got, "type").String() != "text" ||
		gjson.Get(got, "text").String() != "reasoning a < b" {
		t.Errorf("thinking 应转成 text 且内容不变: %s", got)
	}
	if got := blocks[1].Raw; got != toolUse {
		t.Errorf("tool_use 必须逐字节原样\n want: %s\n got:  %s", toolUse, got)
	}
	if got := blocks[2].Raw; got != keepText {
		t.Errorf("普通 text 必须逐字节原样\n want: %s\n got:  %s", keepText, got)
	}
}

// redacted_thinking 删除、空白 text 删除，其余块仍需原样。
func TestFilterThinkingBlocksForRetry_DropsRedactedAndBlank(t *testing.T) {
	const keep = `{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"a & b"}]}`
	body := `{"model":"claude-opus-4-8","messages":[{"role":"user","content":[` +
		`{"type":"redacted_thinking","data":"zzz"},` +
		`{"type":"text","text":"   "},` +
		keep + `]}]}`

	out := FilterThinkingBlocksForRetry([]byte(body), "claude-opus-4-8")

	blocks := gjson.GetBytes(out, "messages.0.content").Array()
	if len(blocks) != 1 {
		t.Fatalf("块数 = %d, want 1", len(blocks))
	}
	if blocks[0].Raw != keep {
		t.Errorf("保留块必须逐字节原样\n want: %s\n got:  %s", keep, blocks[0].Raw)
	}
}

// 整条消息被清空时必须补占位块，否则换来另一个 400。
func TestFilterThinkingBlocksForRetry_PlaceholderOnEmptied(t *testing.T) {
	body := `{"model":"claude-opus-4-8","messages":[` +
		`{"role":"assistant","content":[{"type":"redacted_thinking","data":"z"}]},` +
		`{"role":"user","content":[{"type":"text","text":"hi"}]}]}`

	out := FilterThinkingBlocksForRetry([]byte(body), "claude-opus-4-8")

	blocks := gjson.GetBytes(out, "messages.0.content").Array()
	if len(blocks) != 1 {
		t.Fatalf("清空后应补占位块，块数 = %d", len(blocks))
	}
	if blocks[0].Get("text").String() == "" {
		t.Errorf("占位块文本不得为空: %s", blocks[0].Raw)
	}
}

// passback-required 上游（DeepSeek/Kimi/GLM 等）要求历史 thinking 原样回传，不得变形。
func TestFilterThinkingBlocksForRetry_PassbackUpstreamUnchanged(t *testing.T) {
	body := `{"model":"deepseek-chat","thinking":{"type":"enabled"},"messages":[` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"s"}]}]}`

	out := FilterThinkingBlocksForRetry([]byte(body), "deepseek-chat")
	if string(out) != body {
		t.Errorf("passback-required 上游的 body 不得改动\n got: %s", out)
	}
}
