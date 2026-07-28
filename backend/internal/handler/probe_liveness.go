package handler

import (
	"strings"

	"github.com/tidwall/gjson"
)

// 分层测活拦截。
//
// 生产画像（2026-07-28，输出 ≤5 token 的请求）给出的决定性信号是 session：
//
//	Go-http-client/2.0                    606 次, 平均输入 523,  会话数 0
//	Go-http-client/1.1                    365 次, 平均输入 2120, 会话数 0
//	GatewayClient/1.0                     134 次, 平均输入 601,  会话数 0
//	openox-...-capability-probe            16 次, 平均输入 460,  会话数 0
//	claude-cli/2.1.178 (external, cli)     42 次, 平均输入 143,  会话数 42  ← 真实
//	claude-cli/2.1.161 (external, cli)     30 次, 平均输入 83,   会话数 29  ← 真实
//
// 真实 Claude Code CLI 几乎每个请求都带 metadata.user_id 里的 session 段，
// 而脚本/网关的测活一个都没有。这比按内容判定可靠得多，因此分两档：
//
//	无 session → 宽松档：允许带 system、扩展测活文案（脚本测活多带完整系统提示）
//	有 session → 严格档：沿用 isTrivialGreetingRequest（必须无 system 的纯问候）
//
// 另有一档更强的信号：UA 直接自称探针，无条件拦。

// probeUserAgentMarkers 是 UA 中表明"这是探测/监控请求"的关键词。
// 真实客户端（claude-cli / Go-http-client / 浏览器）都不含这些词。
var probeUserAgentMarkers = []string{
	"probe",
	"healthcheck",
	"health-check",
	"uptime",
	"monitor",
}

// isProbeUserAgent 判断 UA 是否自称探测/监控。
func isProbeUserAgent(userAgent string) bool {
	ua := strings.ToLower(strings.TrimSpace(userAgent))
	if ua == "" {
		return false
	}
	for _, marker := range probeUserAgentMarkers {
		if strings.Contains(ua, marker) {
			return true
		}
	}
	return false
}

// sessionMarker 是 Claude Code 写入 metadata.user_id 的会话段前缀。
const sessionMarker = "_session_"

// hasSessionMarker 判断请求是否携带会话标识。
//
// 取的是客户端**原始**的 metadata.user_id：网关自身的重写发生在
// buildUpstreamRequest 阶段，而拦截点远在其之前，所以这里看到的确实是
// 客户端有没有主动带会话。
func hasSessionMarker(body []byte) bool {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	uid := gjson.GetBytes(body, "metadata.user_id").String()
	return strings.Contains(uid, sessionMarker)
}

// livenessProbeTexts 是无 session 时额外认定为测活的文案（宽松档专用）。
// 比 greetingTexts 宽，因为"裸 HTTP 脚本发这些词"基本不可能是真实用途。
var livenessProbeTexts = map[string]struct{}{
	"test": {}, "ping": {}, "ok": {}, "hello world": {},
	"say hi": {}, "reply with ok": {}, "are you there": {},
	"1": {}, "2": {}, "1+1": {}, "2+2": {}, "1 + 1": {}, "2 + 2": {},
	"测试": {}, "你是谁": {}, "在吗": {}, "收到": {},
}

// isLivenessProbeRequest 判断请求是否为测活探测。
//
// 严格档（有 session）与宽松档（无 session）的差别只在"是否允许带 system 提示"
// 与"文案集大小"；两档都要求：无 tools、恰好一条 user 消息、内容为纯文本。
// 带工具、带对话历史、含图片、或问候后还有实质诉求的请求一律放行。
func isLivenessProbeRequest(body []byte) bool {
	// 有 session：真实客户端的可能性高，维持严格口径。
	if hasSessionMarker(body) {
		return isTrivialGreetingRequest(body)
	}

	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	if tools := gjson.GetBytes(body, "tools"); tools.IsArray() && len(tools.Array()) > 0 {
		return false
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return false
	}
	arr := messages.Array()
	if len(arr) != 1 || arr[0].Get("role").String() != "user" {
		return false
	}

	text, ok := plainTextOfMessageContent(arr[0].Get("content"))
	if !ok {
		return false
	}
	normalized := normalizeGreeting(text)
	if _, isGreeting := greetingTexts[normalized]; isGreeting {
		return true
	}
	_, isProbeText := livenessProbeTexts[normalized]
	return isProbeText
}
