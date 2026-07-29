package handler

import (
	"crypto/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminprobe"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// 本文件实现两类"不该消耗上游配额"的请求的本地拦截，统一在账号选择之前完成：
//
//  1. 人造探针工具类型 —— 通过非法 tool type 触发上游报错、从错误回显里反推
//     上游供应商与能力清单。返回一个与官方 Anthropic 字节形态一致的 400。
//  2. 纯问候语请求 —— 其他中转的健康检查（new-api / sub2api 一类默认发 "hi"）。
//     返回一句自然的问候回复。
//
// 两者都不发上游、不占账号并发槽。

// interceptNonUpstreamRequest 在账号选择之前本地处理探针与健康检查请求。
// 返回 true 表示响应已写出，调用方必须立即返回。
//
// 顺序：先判探针工具（形态明确、优先级高），再判纯问候。
func (h *GatewayHandler) interceptNonUpstreamRequest(
	c *gin.Context,
	body []byte,
	model string,
	maxTokens int,
	stream bool,
	isClaudeCodeClient bool,
	reqLog *zap.Logger,
) bool {
	if h.cfg == nil {
		return false
	}

	if h.cfg.Gateway.InterceptProbeTools {
		if toolType, index, found := detectProbeToolType(body); found {
			reqLog.Info("gateway.intercept_probe_tool",
				zap.String("tool_type", toolType),
				zap.Int("tool_index", index),
				zap.String("model", model))
			sendAnthropicToolTypeError(c, toolType, index)
			return true
		}
	}

	// Claude Code 的预热类请求（Warmup / SUGGESTION MODE / max_tokens=1 haiku 探测）。
	//
	// 这段判定原本在账号选择之后（gateway_handler.go 里两处 IsInterceptWarmupEnabled
	// 分支），命中后还要手动 ReleaseFunc() 把刚占的账号槽还回去。虽然同样不发上游，
	// 但请求已经排过用户并发队列、占过账号并发槽——在槽位打满时甚至会先拿到 503。
	// 提到这里之后，这类请求恒定秒回，与账号池状态完全无关。
	//
	// 各类型的响应形态由 sendMockIntercept* 决定（如 max_tokens=1 探测必须回
	// stop_reason=max_tokens 且正文为 "#"），沿用原实现不变。
	if h.cfg.Gateway.InterceptWarmup {
		if interceptType := detectInterceptType(body, model, maxTokens, isClaudeCodeClient); interceptType != InterceptTypeNone {
			reqLog.Info("gateway.intercept_warmup",
				zap.Int("intercept_type", int(interceptType)),
				zap.String("model", model),
				zap.Bool("stream", stream))
			if stream {
				sendMockInterceptStream(c, model, interceptType)
			} else {
				sendMockInterceptResponse(c, model, interceptType)
			}
			return true
		}
	}

	// max_tokens=1 若未被上面的预热分支接走（例如该开关关闭、或非 haiku 模型），
	// 不能落到下面的问候拦截：客户端期待被 max_tokens 截断的响应，
	// 返回完整问候会让 stop_reason 变成 end_turn，与预期不符。
	if maxTokens == 1 {
		return false
	}

	if !h.cfg.Gateway.InterceptGreeting {
		return false
	}

	// Admin UI 主动探测必须穿透测活拦截，否则管理员只能看到本地假问候、
	// 无法判断账号是否真挂。识别信号（任一即可）：
	//   1) 出站密钥头 X-Sub2API-Admin-Probe（AccountTest / channel_monitor 写入）
	//   2) Admin 账号连通性测试的固定 user 文案（自环到本网关时的兜底）
	// 外部客户端可抄文案 2，但抄了就会真发上游、产生费用，不会白嫖测活假回复。
	// 注意：此旁路只作用于问候/测活；探针工具（zzz_*）仍按上面分支拦截。
	if c != nil && c.Request != nil && adminprobe.IsTrusted(c.Request.Header) {
		reqLog.Info("gateway.skip_liveness_intercept_admin_probe",
			zap.String("reason", "header"),
			zap.String("model", model),
			zap.Bool("stream", stream))
		return false
	}
	if isAdminAccountConnectivityTestBody(body) {
		reqLog.Info("gateway.skip_liveness_intercept_admin_probe",
			zap.String("reason", "account_test_body"),
			zap.String("model", model),
			zap.Bool("stream", stream))
		return false
	}

	// 分层测活拦截，按信号强度从强到弱：
	//   1) UA 自称探针 —— 最强信号，无条件拦
	//   2) 无 session + 测活文案（宽松档）/ 有 session + 纯问候（严格档）
	userAgent := ""
	if c != nil && c.Request != nil {
		userAgent = c.Request.UserAgent()
	}

	tier := ""
	switch {
	case isProbeUserAgent(userAgent):
		tier = "user_agent"
	case isLivenessProbeRequest(body):
		tier = "request_shape"
	default:
		return false
	}

	reqLog.Info("gateway.intercept_liveness_probe",
		zap.String("tier", tier),
		zap.String("model", model),
		zap.Bool("stream", stream),
		zap.Bool("has_session", hasSessionMarker(body)),
		zap.String("user_agent", userAgent))

	text := greetingReplyText(body)
	if stream {
		sendGreetingInterceptStream(c, model, text)
	} else {
		sendGreetingInterceptResponse(c, model, text)
	}
	return true
}

// probeToolTypePrefix 是人造探针最常见的排序前缀：真实 Anthropic server tool
// 不存在以 zzz 开头的类型。
const probeToolTypePrefix = "zzz"

// clientToolTypes 是客户端工具的 type 取值，不参与 server tool 探针判定。
var clientToolTypes = map[string]struct{}{
	"":         {},
	"custom":   {},
	"function": {},
}

// isProbeToolType 判断某个 tool type 是否为人造探针。
//
// 判定刻意收窄，只认两类"真实工具不可能出现"的形态：
//
//	zzz 前缀            —— 探测者惯用的排序前缀
//	非法的 8 位日期后缀 —— 全零、月份 >12、日期 >31、年份越界
//
// 特别注意两条不能误伤的真实形态：
//
//   - computer_20241022 / computer_20250124 等是**真实**工具类型，它们报 400 是
//     因为转发时缺 computer-use beta header，不是探针；
//   - tool_search_tool_bm25 等真实类型**没有**日期后缀，所以"缺日期"不能作为
//     探针信号。
func isProbeToolType(toolType string) bool {
	t := strings.ToLower(strings.TrimSpace(toolType))
	if _, ok := clientToolTypes[t]; ok {
		return false
	}
	if strings.HasPrefix(t, probeToolTypePrefix) {
		return true
	}
	return hasInvalidDateSuffix(t)
}

// hasInvalidDateSuffix 判断类型名末段是否是一个"看起来像日期但不可能是日期"的
// 8 位数字。没有 8 位数字末段时返回 false（真实工具允许无日期后缀）。
func hasInvalidDateSuffix(toolType string) bool {
	idx := strings.LastIndex(toolType, "_")
	if idx < 0 {
		return false
	}
	suffix := toolType[idx+1:]
	const dateLen = 8
	if len(suffix) != dateLen {
		return false
	}
	n, err := strconv.Atoi(suffix)
	if err != nil {
		return false
	}
	year, month, day := n/10000, (n/100)%100, n%100
	if year < 2020 || year > 2099 {
		return true
	}
	if month < 1 || month > 12 {
		return true
	}
	return day < 1 || day > 31
}

// detectProbeToolType 扫描请求体的 tools 数组，返回首个探针类型及其下标。
func detectProbeToolType(body []byte) (toolType string, index int, found bool) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return "", 0, false
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return "", 0, false
	}
	for i, tool := range tools.Array() {
		t := tool.Get("type").String()
		if isProbeToolType(t) {
			return t, i, true
		}
	}
	return "", 0, false
}

// greetingTexts 是被视为"纯问候"的归一化文本集合。
// 刻意保持极窄：只收不含任何实质诉求的招呼语。
var greetingTexts = map[string]struct{}{
	"hi": {}, "hello": {}, "hey": {}, "yo": {},
	"hi there": {}, "hello there": {},
	"你好": {}, "您好": {}, "哈喽": {}, "哈啰": {}, "嗨": {},
}

// isAdminAccountConnectivityTestBody 识别 Admin「测试连接」出站 body：
// 恰好一条 user 消息，且纯文本等于 AccountTest 固定探测句。
// 该句刻意不是问候语；用于自环到本网关时即使密钥头丢失也能穿透测活拦截。
func isAdminAccountConnectivityTestBody(body []byte) bool {
	if len(body) == 0 || !gjson.ValidBytes(body) {
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
	return strings.TrimSpace(text) == adminprobe.ConnectivityTestUserText
}

// greetingTrimCutset 是归一化时剥掉的首尾标点与空白。
const greetingTrimCutset = " \t\r\n!?.,;:~！？。，；：、"

// isTrivialGreetingRequest 判断请求是否是"纯问候"的连通性测试。
//
// 判定条件全部满足才算（严格口径，宁可漏拦不可误伤真实会话）：
//
//   - 无 tools（Claude Code 一类的真实会话必带工具）
//   - 无实质 system prompt
//   - messages 恰好一条，且 role 为 user
//   - 内容为纯文本（含图片直接排除），归一化后落在 greetingTexts 内
//
// 真实用户开场说 "hi" 会被拦，但拿到的是一句自然的问候回复，与真实模型输出
// 无法分辨，因此误伤代价接近于零。
func isTrivialGreetingRequest(body []byte) bool {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	if tools := gjson.GetBytes(body, "tools"); tools.IsArray() && len(tools.Array()) > 0 {
		return false
	}
	if hasSubstantiveSystemPrompt(gjson.GetBytes(body, "system")) {
		return false
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return false
	}
	arr := messages.Array()
	if len(arr) != 1 {
		return false
	}
	if arr[0].Get("role").String() != "user" {
		return false
	}

	text, ok := plainTextOfMessageContent(arr[0].Get("content"))
	if !ok {
		return false
	}
	_, isGreeting := greetingTexts[normalizeGreeting(text)]
	return isGreeting
}

// hasSubstantiveSystemPrompt 判断 system 字段是否含非空白内容。
// 兼容 Anthropic 的两种写法：纯字符串，或 text block 数组。
func hasSubstantiveSystemPrompt(system gjson.Result) bool {
	if !system.Exists() {
		return false
	}
	if system.IsArray() {
		for _, block := range system.Array() {
			if strings.TrimSpace(block.Get("text").String()) != "" {
				return true
			}
		}
		return false
	}
	return strings.TrimSpace(system.String()) != ""
}

// plainTextOfMessageContent 提取消息内容的纯文本。
// ok 为 false 表示内容不是纯文本（例如含图片 / tool_result 块），此时不应按
// 问候语处理。
func plainTextOfMessageContent(content gjson.Result) (string, bool) {
	if !content.IsArray() {
		return content.String(), true
	}
	var parts []string
	for _, block := range content.Array() {
		if block.Get("type").String() != "text" {
			return "", false
		}
		parts = append(parts, block.Get("text").String())
	}
	return strings.Join(parts, " "), true
}

// normalizeGreeting 归一化问候语：去首尾标点空白、转小写。
func normalizeGreeting(text string) string {
	return strings.ToLower(strings.Trim(text, greetingTrimCutset))
}

const (
	greetingReplyEN = "Hello! How can I help you today?"
	greetingReplyZH = "你好！有什么可以帮你的吗？"
)

// greetingReplyText 按来访问候语的语种选择回复，让伪造响应更自然。
func greetingReplyText(body []byte) string {
	text, _ := plainTextOfMessageContent(gjson.GetBytes(body, "messages.0.content"))
	for _, r := range text {
		if r >= 0x4E00 && r <= 0x9FFF {
			return greetingReplyZH
		}
	}
	return greetingReplyEN
}

// anthropicAcceptedToolTags 是伪造 400 时回显的"可用工具清单"。
//
// 这份清单由我们自己掌控，与账号池真实能力解耦：探测者拿到的是一份统一、稳定
// 的假指纹，既看不出真实上游是谁，也看不出池子里混用了不同来源（此前同一参数
// 在不同账号上会分别报 `is deprecated` 与 `range: 0..1`，本身就是暴露）。
// 它不需要跟随 Anthropic 更新——正因为它是假的。
const anthropicAcceptedToolTags = `'bash_20250124', 'code_execution_20250522', 'code_execution_20250825', ` +
	`'custom', 'memory_20250818', 'text_editor_20250124', 'text_editor_20250429', ` +
	`'text_editor_20250728', 'web_fetch_20250910', 'web_search_20250305'`

// sendAnthropicToolTypeError 返回一个与官方 Anthropic 字节形态一致的 400。
//
// 与后台"错误透传规则"改写出来的响应不同，这里的 type 是官方的
// invalid_request_error（规则路径会被强制成 upstream_error，一比对就能认出是
// 中转），并且回显了对方实际发送的 tag 名与下标。
func sendAnthropicToolTypeError(c *gin.Context, toolType string, index int) {
	message := "tools." + strconv.Itoa(index) + ": Input tag '" + toolType +
		"' found using 'type' does not match any of the expected tags: " + anthropicAcceptedToolTags

	c.JSON(http.StatusBadRequest, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    "invalid_request_error",
			"message": message,
		},
		"request_id": generateRealisticRequestID(),
	})
}

// generateRealisticRequestID 生成仿真的请求 ID（req_XXXXXXXX 格式），
// 与 Claude API 真实响应的 request_id 形态一致。
func generateRealisticRequestID() string {
	const charset = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	const idLen = 24
	randomBytes := make([]byte, idLen)
	if _, err := rand.Read(randomBytes); err != nil {
		return "req_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	b := make([]byte, idLen)
	for i := range b {
		b[i] = charset[int(randomBytes[i])%len(charset)]
	}
	return "req_" + string(b)
}

// sendGreetingInterceptResponse 返回非流式的问候回复。
func sendGreetingInterceptResponse(c *gin.Context, model, text string) {
	c.JSON(http.StatusOK, gin.H{
		"model":         model,
		"id":            generateRealisticMsgID(),
		"type":          "message",
		"role":          "assistant",
		"content":       []gin.H{{"type": "text", "text": text}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage": gin.H{
			"input_tokens":                10,
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens":     0,
			"cache_creation": gin.H{
				"ephemeral_5m_input_tokens": 0,
				"ephemeral_1h_input_tokens": 0,
			},
			"output_tokens": greetingOutputTokens,
			"total_tokens":  10 + greetingOutputTokens,
		},
	})
}

// greetingOutputTokens 是伪造响应里上报的输出 token 数。问候回复长度固定，
// 取一个与之相称的小值即可（拦截请求不计费，仅用于响应体形态自洽）。
const greetingOutputTokens = 9

// sendGreetingInterceptStream 返回流式的问候回复。
//
// SSE 事件形态与 sendMockInterceptStream 保持一致；此处独立实现是为了不改动
// 那条已在线上服务预热/建议模式拦截的路径。
func sendGreetingInterceptStream(c *gin.Context, model, text string) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	msgID := generateRealisticMsgID()
	messageStart := `{"type":"message_start","message":{"id":` + strconv.Quote(msgID) +
		`,"type":"message","role":"assistant","model":` + strconv.Quote(model) +
		`,"content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}`

	events := []string{
		"event: message_start\ndata: " + messageStart,
		`event: content_block_start` + "\n" + `data: {"content_block":{"text":"","type":"text"},"index":0,"type":"content_block_start"}`,
		"event: content_block_delta\ndata: " +
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + strconv.Quote(text) + `}}`,
		`event: content_block_stop` + "\n" + `data: {"index":0,"type":"content_block_stop"}`,
		"event: message_delta\ndata: " +
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":10,"output_tokens":` +
			strconv.Itoa(greetingOutputTokens) + `}}`,
		`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
	}

	for _, event := range events {
		_, _ = c.Writer.WriteString(event + "\n\n")
		c.Writer.Flush()
	}
}
