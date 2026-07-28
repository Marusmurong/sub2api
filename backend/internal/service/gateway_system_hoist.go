package service

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// hoistSystemRoleMessages 把 messages 里 role 为 system / developer 的消息提升到
// 顶层 system 参数。
//
// 动机（生产实测 400）：
//
//	messages.0: use the top-level 'system' parameter for the initial system
//	prompt; the directive-only form (content: [] with output_config) is
//	accepted at any position
//
// Anthropic Messages API 不接受 messages 里的 system 角色（这是 OpenAI 的写法），
// 系统提示必须放在顶层 system 字段。下游客户端按 OpenAI 习惯构造请求时就会撞上。
// 这类请求不该拦截——搬个位置它就是合法请求。
//
// 保守边界（任一命中则整体不改动，宁可让上游报错也不破坏语义）：
//
//   - 顶层 system 已经是 block 数组：其中可能带 cache_control，合并成字符串会
//     破坏 prompt 缓存命中，得不偿失；
//   - messages 里全是 system 消息：Anthropic 要求 messages 非空，提升后会留下
//     空数组，反而换成另一个 400。
//
// 返回 (rewritten, changed)。
func hoistSystemRoleMessages(body []byte) ([]byte, bool) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, false
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body, false
	}
	arr := messages.Array()

	var systemParts []string
	kept := make([]any, 0, len(arr))
	found := false
	for _, msg := range arr {
		if !isSystemRoleMessage(msg) {
			kept = append(kept, msg.Value())
			continue
		}
		found = true
		if text := strings.TrimSpace(systemTextOfMessage(msg)); text != "" {
			systemParts = append(systemParts, text)
		}
	}
	if !found {
		return body, false
	}
	// 提升后 messages 会变空 —— Anthropic 要求非空，放弃改写。
	if len(kept) == 0 {
		return body, false
	}

	existing := gjson.GetBytes(body, "system")
	// 顶层 system 是 block 数组时可能带 cache_control，合并会破坏缓存，放弃改写。
	if existing.IsArray() {
		return body, false
	}

	out, err := sjson.SetBytes(body, "messages", kept)
	if err != nil {
		return body, false
	}

	merged := mergeSystemText(strings.TrimSpace(existing.String()), systemParts)
	if merged == "" {
		return out, true
	}
	if b, err := sjson.SetBytes(out, "system", merged); err == nil {
		out = b
	}
	return out, true
}

// isSystemRoleMessage 判断消息角色是否为 system / developer。
// developer 是 OpenAI 较新的系统提示角色，语义等价。
func isSystemRoleMessage(msg gjson.Result) bool {
	role := strings.ToLower(strings.TrimSpace(msg.Get("role").String()))
	return role == "system" || role == "developer"
}

// systemTextOfMessage 抽取消息内容的文本，兼容字符串与 text block 数组两种写法。
func systemTextOfMessage(msg gjson.Result) string {
	content := msg.Get("content")
	if !content.IsArray() {
		return content.String()
	}
	var parts []string
	for _, block := range content.Array() {
		if block.Get("type").String() != "text" {
			continue
		}
		if text := block.Get("text").String(); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// mergeSystemText 把已有的顶层 system 与提升上来的片段拼接，已有内容在前。
func mergeSystemText(existing string, parts []string) string {
	all := make([]string, 0, len(parts)+1)
	if existing != "" {
		all = append(all, existing)
	}
	all = append(all, parts...)
	return strings.Join(all, "\n\n")
}
