//go:build unit

package service

import (
	"testing"

	"github.com/tidwall/gjson"
)

// thinking 块必须原样回传——Anthropic 的 signature 校验以此为前提。
// 清洗空文本块时若把整个 messages 数组 json.Unmarshal → json.Marshal 往返一遍，
// 会重排 key 并把 < > & 转义成 < & >，thinking 块随之被改写，
// 换来 400 "Invalid `signature` in `thinking` block"。
//
// 生产实测（2026-07-31）：把空白 text 块纳入清洗范围后，该往返触发频率大增，
// 签名错误率从 2.3% 一路爬到 16.1%。
func TestStripEmptyTextBlocks_PreservesUntouchedBlocksByteForByte(t *testing.T) {
	const thinkingBlock = `{"type":"thinking","thinking":"if a < b && c > d { return \"x\" }","signature":"EqQBCkYIBRgCKkBd0x/sig"}`
	body := `{"model":"claude-opus-4-8","messages":[` +
		`{"role":"assistant","content":[` + thinkingBlock + `,{"type":"text","text":"   "}]},` +
		`{"role":"user","content":"go on"}]}`

	out := StripEmptyTextBlocks([]byte(body))
	got := gjson.GetBytes(out, "messages.0.content.0").Raw

	if got != thinkingBlock {
		t.Errorf("thinking 块必须逐字节保持原样\n want: %s\n got:  %s", thinkingBlock, got)
	}
	if n := gjson.GetBytes(out, "messages.0.content.#").Int(); n != 1 {
		t.Errorf("空白 text 块应被删除,剩余块数 = %d, want 1", n)
	}
}

// 同一条不变量对嵌套在 tool_result 里的内容同样成立。
func TestStripEmptyTextBlocks_PreservesNestedSiblings(t *testing.T) {
	const keep = `{"type":"text","text":"a < b & c"}`
	body := `{"messages":[{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"tu_1","content":[` + keep + `,{"type":"text","text":""}]}]}]}`

	out := StripEmptyTextBlocks([]byte(body))
	got := gjson.GetBytes(out, "messages.0.content.0.content.0").Raw

	if got != keep {
		t.Errorf("嵌套保留块必须逐字节原样\n want: %s\n got:  %s", keep, got)
	}
	if n := gjson.GetBytes(out, "messages.0.content.0.content.#").Int(); n != 1 {
		t.Errorf("嵌套空块应被删除,剩余 = %d, want 1", n)
	}
}

// 删空之后补占位块的行为不能回退：整条消息只有空文本块时，
// 清洗后不得留下 content: []（那是另一个 400）。
func TestStripEmptyTextBlocks_PlaceholderStillApplied(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"  "}]}]}`

	out := StripEmptyTextBlocks([]byte(body))

	if n := gjson.GetBytes(out, "messages.0.content.#").Int(); n != 1 {
		t.Fatalf("清空后应补占位块,块数 = %d", n)
	}
	if txt := gjson.GetBytes(out, "messages.0.content.0.text").String(); txt == "" {
		t.Errorf("占位块文本不得为空")
	}
}

// ensureNonEmptyMessageContent 有同样的往返，同样的不变量。
func TestEnsureNonEmptyMessageContent_PreservesOtherMessages(t *testing.T) {
	const thinkingBlock = `{"type":"thinking","thinking":"x < y & z","signature":"sig-abc"}`
	body := `{"messages":[` +
		`{"role":"assistant","content":[` + thinkingBlock + `]},` +
		`{"role":"user","content":[]}]}`

	out := ensureNonEmptyMessageContent([]byte(body))
	got := gjson.GetBytes(out, "messages.0.content.0").Raw

	if got != thinkingBlock {
		t.Errorf("未被改动的消息必须逐字节原样\n want: %s\n got:  %s", thinkingBlock, got)
	}
	if n := gjson.GetBytes(out, "messages.1.content.#").Int(); n != 1 {
		t.Errorf("空 content 应补占位块,块数 = %d", n)
	}
}

// FilterThinkingBlocks 是级联的源头：它每个带 thinking 的 anthropic 请求都跑，
// 一旦命中一个签名缺失的坏块就整包 Unmarshal/Marshal 往返一次，把**同一请求里
// 所有好块**的 key 重排、< > & 转义掉——好块的签名随之全废。
//
// 生产表现正是"报在没被删的那个块上"：messages.1.content.11、messages.3.content.58。
func TestFilterThinkingBlocks_PreservesValidBlocksByteForByte(t *testing.T) {
	const good = `{"type":"thinking","thinking":"a < b && c > d","signature":"valid-sig-1"}`
	const bad = `{"type":"thinking","thinking":"no signature here","signature":""}`
	const toolUse = `{"type":"tool_use","id":"tu_1","name":"Bash","input":{"cmd":"echo a && echo b"}}`

	body := `{"model":"claude-opus-4-8","thinking":{"type":"enabled"},"messages":[` +
		`{"role":"assistant","content":[` + good + `,` + bad + `,` + toolUse + `]}]}`

	out := FilterThinkingBlocks([]byte(body), "claude-opus-4-8")

	if n := gjson.GetBytes(out, "messages.0.content.#").Int(); n != 2 {
		t.Fatalf("坏块应被删除,剩余块数 = %d, want 2", n)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0").Raw; got != good {
		t.Errorf("有效 thinking 块必须逐字节原样\n want: %s\n got:  %s", good, got)
	}
	if got := gjson.GetBytes(out, "messages.0.content.1").Raw; got != toolUse {
		t.Errorf("tool_use 块必须逐字节原样\n want: %s\n got:  %s", toolUse, got)
	}
}

// thinking 未启用时整批删除，剩余的非 thinking 块同样必须原样。
func TestFilterThinkingBlocks_ThinkingDisabled_PreservesOthers(t *testing.T) {
	const keep = `{"type":"text","text":"x < y & z"}`
	body := `{"model":"claude-opus-4-8","messages":[` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"s"},` + keep + `]}]}`

	out := FilterThinkingBlocks([]byte(body), "claude-opus-4-8")

	if n := gjson.GetBytes(out, "messages.0.content.#").Int(); n != 1 {
		t.Fatalf("thinking 未启用时应删除 thinking 块,剩余 = %d", n)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0").Raw; got != keep {
		t.Errorf("保留块必须逐字节原样\n want: %s\n got:  %s", keep, got)
	}
}

// FilterWebSearchHistoryBlocks 同样在每个 anthropic 请求上跑（含 web_search 的会话），
// 同样的往返，同样会连累同一请求里的 thinking 块。
func TestFilterWebSearchHistoryBlocks_PreservesOtherBlocks(t *testing.T) {
	const thinking = `{"type":"thinking","thinking":"a < b && c","signature":"sig-keep"}`
	const strip = `{"type":"server_tool_use","id":"st_1","name":"web_search","input":{"query":"a & b"}}`

	body := `{"model":"claude-opus-4-8","thinking":{"type":"enabled"},"messages":[` +
		`{"role":"assistant","content":[` + thinking + `,` + strip + `]}]}`

	out := FilterWebSearchHistoryBlocks([]byte(body), "claude-opus-4-8")

	if got := gjson.GetBytes(out, "messages.0.content.0").Raw; got != thinking {
		t.Errorf("thinking 块必须逐字节原样\n want: %s\n got:  %s", thinking, got)
	}
}
