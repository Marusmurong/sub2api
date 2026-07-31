package service

import (
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 转发前的工具块结构校验。
//
// Anthropic 的 schema 对工具块有硬性必填字段（tool_use 的 id/name、tool_result 的
// tool_use_id）。缺了这些的请求 100% 会被 400 拒绝，把它们发上去只会：
//
//   - 白白消耗一次订阅账号的调用
//   - 在账号上留下一条格式非法的请求记录——在关联封禁的语境下这才是主要代价
//
// 生产实测（2026-07-31）：这类请求全部来自同一个下游 key（一个自建 Go 中转，
// 背后接着 Go SDK / Python SDK / CSSwitch 等第三方客户端），报文形如
//
//	400 messages.14.content.0.tool_use.id: Field required
//
// 校验必须在**任何改写之前**执行。我们自己的 thinking 剥离会改变块的下标，事后再
// 比对就分不清是客户端发错了还是我们改坏了——今天已经因为这种混淆两次归因失败。
// 放在入口，报出来的下标就是客户端原文的下标。

// describeMalformedToolBlock 返回第一个结构非法的工具块的描述（形如
// "messages.14.content.0.tool_use.id: Field required"），全部合法时返回 ""。
//
// 只检查 Anthropic schema 里的硬性必填项，不做任何猜测性修补：把缺失字段补一个
// 我们编的值，会让上游收到一个与客户端意图不同的请求，也会掩盖调用方的 bug。
//
// 解析失败一律返回 ""——非法 JSON 由既有的解析错误路径处理，在这里再报一次只会
// 让同一个问题出现两种错误文案。
func describeMalformedToolBlock(body []byte) string {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ""
	}
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.Exists() || !msgs.IsArray() {
		return ""
	}

	problem := ""
	msgs.ForEach(func(msgIdx, msg gjson.Result) bool {
		content := msg.Get("content")
		// content 允许是字符串（纯文本消息），此时没有工具块可查。
		if !content.Exists() || !content.IsArray() {
			return true
		}
		for i, b := range content.Array() {
			if !b.IsObject() {
				continue
			}
			path := "messages." + msgIdx.String() + ".content." + strconv.Itoa(i)
			if d := describeMalformedToolBlockAt(b, path); d != "" {
				problem = d
				return false
			}
		}
		return true
	})
	return problem
}

// describeMalformedToolBlockAt 检查单个 content 块。
func describeMalformedToolBlockAt(block gjson.Result, path string) string {
	switch block.Get("type").String() {
	case "tool_use":
		if block.Get("id").String() == "" {
			return path + ".tool_use.id: Field required"
		}
		if block.Get("name").String() == "" {
			return path + ".tool_use.name: Field required"
		}
	case "tool_result":
		if block.Get("tool_use_id").String() == "" {
			return path + ".tool_result.tool_use_id: Field required"
		}
	}
	return ""
}

// MalformedToolBlockError 表示客户端 body 里的工具块结构非法。
//
// 单独一个类型而不是复用 UpstreamFailoverError：后者会让请求依次去试其它账号，
// 而这个错误换任何账号都是同样的 400——轮一遍只会把一个必然失败的请求打到整个
// 账号池上，正是要避免的事。处理方式与 BetaBlockedError 一致：立即 400，不转移。
type MalformedToolBlockError struct {
	Message string
}

func (e *MalformedToolBlockError) Error() string { return e.Message }

// stripServerToolInputSchema 摘掉服务端工具上的 input_schema。
//
// Anthropic 的服务端工具（web_search_20250305 / text_editor_* / computer_* 等）
// schema 由服务端定义，请求里带 input_schema 会被拒：
//
//	400 tools.0.web_search_20250305.input_schema: Extra inputs are not permitted
//
// 生产实测（2026-07-31）：来自与 tool_use.id 同一个下游 key（自建 Go 中转，背后是
// 各类第三方 SDK）。我们自己从不给服务端工具加这个字段。
//
// 这里「摘掉」而不是像 tool_use.id 那样「拒绝」，区别是能不能无损修正：
//   - tool_use.id 缺失 —— 无法凭空编出正确的 id，编了就是伪造数据，只能拒绝
//   - 服务端工具的 input_schema —— 字段本身无意义（schema 由服务端定义）且被上游
//     拒收，摘掉不改变任何语义，摘完请求就能正常完成
//
// 判定「服务端工具」用的是 type 非空且不是 custom/function。自定义工具的两种写法
// （裸 {name, input_schema} 与 {type:"custom", ...}）都不会被误伤。
func stripServerToolInputSchema(body []byte) ([]byte, bool) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return body, false
	}

	out := body
	changed := false
	arr := tools.Array()
	// 倒序处理：sjson 删除的是对象字段而不是数组元素，下标不会移动；
	// 倒序只是与本文件其余删除逻辑保持一致的写法。
	for i := len(arr) - 1; i >= 0; i-- {
		t := arr[i]
		if !t.IsObject() || !isAnthropicServerToolType(t.Get("type").String()) {
			continue
		}
		if !t.Get("input_schema").Exists() {
			continue
		}
		next, err := sjson.DeleteBytes(out, "tools."+strconv.Itoa(i)+".input_schema")
		if err != nil {
			continue
		}
		out = next
		changed = true
	}
	return out, changed
}

// isAnthropicServerToolType 判断 tool.type 是否为服务端工具。
//
// 用「非空且不是 custom/function」而不是枚举已知的服务端工具名：Anthropic 会不断
// 新增带版本号的服务端工具（web_fetch_20250910、code_execution_* …），枚举一份清单
// 就得跟着上游改，漏一个这个错误就会重新出现。
func isAnthropicServerToolType(toolType string) bool {
	switch toolType {
	case "", "custom", "function":
		return false
	default:
		return true
	}
}
