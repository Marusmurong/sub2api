package claude

import "strings"

// SamplingParamFields 是 Anthropic Messages API 的采样参数字段名。
// 新世代模型（Opus 4.6+ / Sonnet 4.6+ / Sonnet 5 / Fable 5 ...）已废弃这些字段：
// 只要字段**存在**就返回 400，与取值无关，也不接受"传默认值"。
//
//	{"type":"error","error":{"type":"invalid_request_error",
//	 "message":"`temperature` is deprecated for this model."}}
var SamplingParamFields = []string{"temperature", "top_p", "top_k"}

// samplingCapableModelBases 是**仍然接受**采样参数的模型基名白名单。
//
// 策略是白名单保留而非黑名单剥离：Anthropic 的废弃是按代次单向推进的，
// 新模型只会更严格。不认识的模型 ID 一律按"已废弃"处理并剥离采样参数，
// 这样上线新模型不需要改代码；万一判断保守剥错了，网关侧还有
// IsSamplingParamsDeprecatedError 驱动的 400 兜底重试兜住。
//
// 基名不含日期后缀。匹配规则见 SupportsSamplingParams。
var samplingCapableModelBases = []string{
	"claude-opus-4",
	"claude-opus-4-1",
	"claude-opus-4-5",
	"claude-sonnet-4",
	"claude-sonnet-4-5",
	"claude-haiku-4-5",
	"claude-3-opus",
	"claude-3-haiku",
	"claude-3-5-sonnet",
	"claude-3-5-haiku",
	"claude-3-7-sonnet",
}

// SupportsSamplingParams 报告 model 是否仍接受 temperature / top_p / top_k。
//
// 匹配要求基名之后只能跟**日期或别名后缀**（20250514 / latest / v1:0 等），
// 不能跟版本号段。这条约束是本函数的关键：否则 "claude-opus-4-7" 会被基名
// "claude-opus-4" 前缀命中而被误判为支持采样参数，正是要避免的 400。
//
// 入参兼容 Bedrock（anthropic.claude-...-v1:0、us.anthropic.claude-...）
// 与 Vertex（claude-...@20251101）的模型 ID 写法。
func SupportsSamplingParams(model string) bool {
	id := normalizeModelIDForCapability(model)
	if id == "" {
		return false
	}
	for _, base := range samplingCapableModelBases {
		if id == base {
			return true
		}
		rest, ok := strings.CutPrefix(id, base+"-")
		if !ok {
			continue
		}
		if isModelVariantSuffix(firstSegment(rest)) {
			return true
		}
	}
	return false
}

// IsSamplingParamsDeprecatedError 判断上游 400 的错误文案是否为"采样参数已废弃"。
// 用于驱动"剥离采样参数后重试一次"的兜底路径。
//
// 必须同时命中参数名与废弃语义，避免把 temperature 取值越界这类
// **参数仍受支持**的 400 误判成废弃（剥离重试对后者无效，只会浪费一次上游往返）。
func IsSamplingParamsDeprecatedError(message string) bool {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return false
	}

	mentionsParam := false
	for _, field := range SamplingParamFields {
		if strings.Contains(msg, field) {
			mentionsParam = true
			break
		}
	}
	if !mentionsParam {
		return false
	}

	return strings.Contains(msg, "deprecated") ||
		strings.Contains(msg, "unsupported parameter") ||
		strings.Contains(msg, "not supported")
}

// normalizeModelIDForCapability 把各家写法的模型 ID 归一到 "claude-..." 形式：
// 小写、去空白、Vertex 的 '@' 日期分隔符归一为 '-'、剥掉 Bedrock 的厂商/区域前缀。
func normalizeModelIDForCapability(model string) string {
	id := strings.ToLower(strings.TrimSpace(model))
	if id == "" {
		return ""
	}
	id = strings.ReplaceAll(id, "@", "-")
	// "anthropic.claude-x" / "us.anthropic.claude-x" → "claude-x"
	if idx := strings.Index(id, "claude-"); idx > 0 {
		id = id[idx:]
	}
	return id
}

// firstSegment 返回 '-' 分隔的第一段。
func firstSegment(s string) string {
	if idx := strings.Index(s, "-"); idx >= 0 {
		return s[:idx]
	}
	return s
}

// isModelVariantSuffix 判断某一段是否是"发布日期 / 别名 / 厂商版本"后缀，
// 而不是模型的版本号段。这是 SupportsSamplingParams 前缀匹配的安全闸：
// 只有这三类后缀才允许基名匹配成立，否则 "claude-opus-4-7" 会被基名
// "claude-opus-4" 命中而误判为支持采样参数。
//
//	日期     20250514（8 位数字，Anthropic / Vertex）
//	别名     latest
//	厂商版本 v1、v1:0、v2:0（Bedrock）
func isModelVariantSuffix(segment string) bool {
	if segment == "latest" {
		return true
	}
	if isDateSegment(segment) {
		return true
	}
	return isVendorVersionSegment(segment)
}

// isDateSegment 判断是否为 YYYYMMDD 形式的发布日期。
func isDateSegment(segment string) bool {
	const dateLen = 8
	if len(segment) != dateLen {
		return false
	}
	return isAllDigits(segment)
}

// isVendorVersionSegment 判断是否为 Bedrock 的 v<major>[:<minor>] 版本后缀。
// 注意与模型版本号段（"7"、"5"）的区别：厂商后缀必须以 'v' 开头，
// 这正是两者不会互相误判的原因。
func isVendorVersionSegment(segment string) bool {
	rest, ok := strings.CutPrefix(segment, "v")
	if !ok || rest == "" {
		return false
	}
	major, minor, hasMinor := strings.Cut(rest, ":")
	if major == "" || !isAllDigits(major) {
		return false
	}
	if hasMinor && (minor == "" || !isAllDigits(minor)) {
		return false
	}
	return true
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
