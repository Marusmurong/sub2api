package service

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unsafe"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	blockTypeServerToolUse       = "server_tool_use"
	blockTypeWebSearchToolResult = "web_search_tool_result"
)

// Fast-path byte patterns: both block types only ever appear as quoted JSON
// string values, so a raw substring check is a safe pre-filter regardless of
// key/value spacing.
var (
	patternServerToolUse       = []byte(`"server_tool_use"`)
	patternWebSearchToolResult = []byte(`"web_search_tool_result"`)
)

// FilterWebSearchHistoryBlocks removes web-search content blocks from
// historical messages when the upstream cannot accept them:
//
//  1. Emulation-synthesized blocks — server_tool_use / web_search_tool_result
//     whose tool-use ID carries webSearchToolUseIDPrefix — are fabricated
//     locally by the web-search emulation (gateway_websearch_emulation.go).
//     No upstream ever issued them, so clients replaying the conversation
//     (e.g. Claude Code) poison every follow-up request. They are stripped
//     for all upstreams.
//  2. For passback-required upstreams (DeepSeek/Kimi/GLM …, see
//     ResolveThinkingProtocol) all server_tool_use / web_search_tool_result
//     blocks are stripped: these upstreams only accept
//     text/thinking/image/tool_use/tool_result and reject anything else with
//     400 "invalid value: `server_tool_use`". anthropic-strict and unknown
//     upstreams keep genuine blocks untouched.
//
// The emulated assistant turn always carries a trailing text summary, so the
// search context survives the strip. A message whose content would become
// empty gets a placeholder text block (mirroring FilterThinkingBlocksForRetry).
// Returns the original body unchanged when nothing needs stripping.
func FilterWebSearchHistoryBlocks(body []byte, mappedModel string) []byte {
	if !bytes.Contains(body, patternServerToolUse) && !bytes.Contains(body, patternWebSearchToolResult) {
		return body
	}

	stripAll := ResolveThinkingProtocol(mappedModel) == ThinkingProtocolPassbackRequired

	jsonStr := *(*string)(unsafe.Pointer(&body))
	msgsRes := gjson.Get(jsonStr, "messages")
	if !msgsRes.Exists() || !msgsRes.IsArray() {
		return body
	}

	// 只删该删的块，其余字节一律不碰。
	//
	// 早先这里把 messages 整个 json.Unmarshal 到 []any 再 Marshal 回去，会重排 key 并
	// 把字符串里的 < > & 转义成 \u003c \u0026 \u003e。语义没变，但同一请求里的
	// thinking 块因此不再是原样字节，其 signature 校验随即失败——报错还会指向我们
	// 根本没打算删的那个块。详见 filterThinkingBlocksInternal 的说明。
	out := body
	msgsRes.ForEach(func(msgIdx, msg gjson.Result) bool {
		content := msg.Get("content")
		if !content.Exists() || !content.IsArray() {
			return true
		}
		blocks := content.Array()
		base := "messages." + msgIdx.String() + ".content."

		// 倒序删除：正序删会让后面的下标整体前移。
		removed := 0
		for i := len(blocks) - 1; i >= 0; i-- {
			if !blocks[i].IsObject() || !shouldStripWebSearchBlockJSON(blocks[i], stripAll) {
				continue
			}
			if next, err := sjson.DeleteBytes(out, base+strconv.Itoa(i)); err == nil {
				out = next
				removed++
			}
		}
		if removed == 0 || removed < len(blocks) {
			return true
		}

		// 整条被删空：留下 content: [] 会换来另一个 400，补占位块。
		placeholder, err := json.Marshal(emptyContentPlaceholder(msg.Get("role").String()))
		if err != nil {
			return true
		}
		if next, err := sjson.SetRawBytes(out, "messages."+msgIdx.String()+".content", placeholder); err == nil {
			out = next
		}
		return true
	})
	return out
}

// shouldStripWebSearchBlockJSON 是 shouldStripWebSearchBlock 的 gjson 版本，
// 判定规则完全一致——保留两份是为了让既有的 map 版单测继续有效。
func shouldStripWebSearchBlockJSON(block gjson.Result, stripAll bool) bool {
	m, ok := block.Value().(map[string]any)
	if !ok {
		return false
	}
	return shouldStripWebSearchBlock(m, stripAll)
}

func shouldStripWebSearchBlock(block map[string]any, stripAll bool) bool {
	blockType, _ := block["type"].(string)
	switch blockType {
	case blockTypeServerToolUse:
		if stripAll {
			return true
		}
		id, _ := block["id"].(string)
		return strings.HasPrefix(id, webSearchToolUseIDPrefix)
	case blockTypeWebSearchToolResult:
		if stripAll {
			return true
		}
		id, _ := block["tool_use_id"].(string)
		return strings.HasPrefix(id, webSearchToolUseIDPrefix)
	default:
		return false
	}
}
