package service

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// 本文件处理「下游用 OpenAI 的写法请求 Anthropic 原生端点」产生的两类硬 400。
//
// 2026-08-03 生产统计（3 小时 / 25252 请求）里，上游 400 共 349 条，其中：
//
//	57  messages.0: use the top-level 'system' parameter for the initial system prompt
//	15  tool_choice: Input should be an object
//
// 两者合计占 400 的 21%，且全部来自同一个下游（api_key 92 / group 30）。它们不是偶发，
// 而是该客户端稳定的请求形态——按分钟看频率恒定，与总流量无关。
//
// 网关本来就是协议翻译层（已经在做 system 注入、metadata 补全、模型名归一化），把这两个
// 形态翻成 Anthropic 的写法与既有职责一致；而且真实 Claude Code 从不产生这两种形态，
// 归一化同时也让伪装更贴。
//
// 两个函数都遵循同一条原则：**能确定语义时才改，不能确定就原样返回**。宁可让上游继续
// 报 400（下游至少能看到真实原因），也不要猜错语义把请求改成另一个意思。

// hoistLeadingSystemMessage 把 messages[0] 里的 system 消息提升为顶层 system 字段。
//
// Anthropic 不接受 messages 里的 system 角色，初始 system 必须走顶层参数。OpenAI 则相反，
// system 就是 messages[0]。直接转发的结果是 400。
//
// 合并规则（顶层已有 system 时不能丢弃任何一边）：
//   - 顶层无 system      → 直接设为提升上来的文本
//   - 顶层 system 是字符串 → 提升的文本在前，原值在后，空行分隔
//   - 顶层 system 是数组   → 作为 text 块插到最前
//
// 只处理纯文本内容。messages[0].content 若含图片等非文本块，无法无损提升，原样返回——
// 让上游报出真实原因，好过我们悄悄丢掉一张图。
func hoistLeadingSystemMessage(body []byte) ([]byte, bool) {
	first := gjson.GetBytes(body, "messages.0")
	if !first.Exists() || first.Get("role").String() != "system" {
		return body, false
	}

	text, ok := extractPlainTextContent(first.Get("content"))
	if !ok || strings.TrimSpace(text) == "" {
		return body, false
	}

	out := body
	existing := gjson.GetBytes(out, "system")

	switch {
	case !existing.Exists():
		next, ok := setJSONValueBytes(out, "system", text)
		if !ok {
			return body, false
		}
		out = next

	case existing.Type == gjson.String:
		next, ok := setJSONValueBytes(out, "system", text+"\n\n"+existing.String())
		if !ok {
			return body, false
		}
		out = next

	case existing.IsArray():
		// sjson 的 -1 追加语义只能加到末尾，而 system 的顺序有语义（越靠前越先生效），
		// 因此整体重建数组，把提升上来的块放在最前。
		blocks := []json.RawMessage{}
		block, err := json.Marshal(map[string]string{"type": "text", "text": text})
		if err != nil {
			return body, false
		}
		blocks = append(blocks, block)
		existing.ForEach(func(_, item gjson.Result) bool {
			blocks = append(blocks, json.RawMessage(item.Raw))
			return true
		})
		encoded, err := json.Marshal(blocks)
		if err != nil {
			return body, false
		}
		next, ok := setJSONRawBytes(out, "system", encoded)
		if !ok {
			return body, false
		}
		out = next

	default:
		return body, false
	}

	next, ok := deleteJSONPathBytes(out, "messages.0")
	if !ok {
		return body, false
	}
	return next, true
}

// normalizeToolChoiceShape 把 OpenAI 风格的 tool_choice 翻成 Anthropic 的对象形式。
//
// OpenAI 用字符串（"auto" / "none" / "required"），Anthropic 要对象（{"type":"auto"}）。
// 语义映射里唯一需要留意的是 required → any：Anthropic 没有 "required"，表达「必须调用
// 某个工具」的是 any。
//
// 无法映射的值（null、数字、未知字符串）一律删除而不是猜：tool_choice 缺省就是 auto，
// 删掉最多是回到默认行为；猜错则可能把「不许用工具」改成「必须用工具」。
func normalizeToolChoiceShape(body []byte) ([]byte, bool) {
	tc := gjson.GetBytes(body, "tool_choice")
	if !tc.Exists() || tc.IsObject() {
		return body, false
	}

	if tc.Type == gjson.String {
		if mapped, ok := anthropicToolChoiceType(tc.String()); ok {
			next, ok := setJSONRawBytes(body, "tool_choice", []byte(`{"type":"`+mapped+`"}`))
			if !ok {
				return body, false
			}
			return next, true
		}
	}

	next, ok := deleteJSONPathBytes(body, "tool_choice")
	if !ok {
		return body, false
	}
	return next, true
}

// anthropicToolChoiceType 把 OpenAI 的 tool_choice 字符串映射到 Anthropic 的 type。
func anthropicToolChoiceType(v string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "auto":
		return "auto", true
	case "none":
		return "none", true
	case "any", "required":
		return "any", true
	default:
		return "", false
	}
}

// extractPlainTextContent 从消息 content 中取纯文本。
//
// 返回 false 表示存在非文本内容（图片、工具结果等），调用方据此放弃改写——这类内容
// 无法无损地折进顶层 system。
func extractPlainTextContent(content gjson.Result) (string, bool) {
	if !content.Exists() {
		return "", false
	}
	if content.Type == gjson.String {
		return content.String(), true
	}
	if !content.IsArray() {
		return "", false
	}

	parts := make([]string, 0, 4)
	allText := true
	content.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() != "text" {
			allText = false
			return false
		}
		parts = append(parts, item.Get("text").String())
		return true
	})
	if !allText || len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "\n\n"), true
}
