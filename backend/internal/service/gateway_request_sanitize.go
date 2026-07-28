package service

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 本文件按"上游 schema 约束"净化请求字段，与 stripDeprecatedSamplingParams 同构：
// 下游客户端把上游不接受的字段怼过来时，本地改好再转发，而不是让它变成 400。
//
// 每条规则都来自生产上实际观测到的上游报错，不做臆测：
//
//	stream_options: Extra inputs are not permitted            （跨全部模型）
//	This model does not support the effort parameter.         （haiku-4-5）
//	adaptive thinking is not supported on this model          （haiku-4-5）
//	output_config.effort 'max' is not supported when thinking
//	  is disabled on this model. Use effort 'high' or below.   （opus-5）

// effortDowngradeTarget 是 thinking 关闭时 max/xhigh 的降级目标。
// 取值来自上游报错原话 "Use effort 'high' or below"。
const effortDowngradeTarget = "high"

// effortLevelsRequiringThinking 是必须开启 thinking 才被接受的 effort 档位。
var effortLevelsRequiringThinking = map[string]struct{}{
	"max":   {},
	"xhigh": {},
}

// sanitizeAnthropicRequestFields 修正请求体中会被上游拒收的字段。
//
// 返回 (sanitized, changed)：changed 为 false 时 body 原样返回。
func sanitizeAnthropicRequestFields(body []byte, model string) ([]byte, bool) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, false
	}

	out := body
	changed := false

	if b, ok := stripStreamOptions(out); ok {
		out, changed = b, true
	}
	if b, ok := stripUnsupportedEffort(out, model); ok {
		out, changed = b, true
	}
	if b, ok := normalizeAdaptiveThinking(out, model); ok {
		out, changed = b, true
	}
	if b, ok := downgradeEffortWithoutThinking(out); ok {
		out, changed = b, true
	}
	if b, ok := hoistSystemRoleMessages(out); ok {
		out, changed = b, true
	}

	return out, changed
}

// stripStreamOptions 删除 OpenAI 专有的 stream_options 字段。
// Anthropic Messages API 从不接受它，与模型无关。
func stripStreamOptions(body []byte) ([]byte, bool) {
	if !gjson.GetBytes(body, "stream_options").Exists() {
		return body, false
	}
	b, err := sjson.DeleteBytes(body, "stream_options")
	if err != nil {
		return body, false
	}
	return b, true
}

// modelSupportsEffort 报告模型是否接受 output_config.effort。
//
// haiku 系不支持（生产实测：claude-haiku-4-5 报 "This model does not support
// the effort parameter."）。此处按 haiku 家族判定而非具体版本，因为轻量模型
// 通常不提供 effort 档位；若将来某个 haiku 支持了，代价只是白剥一个可选参数。
func modelSupportsEffort(model string) bool {
	return !isHaikuFamilyModel(model)
}

// modelSupportsAdaptiveThinking 报告模型是否接受 thinking.type = "adaptive"。
// 同样按 haiku 家族判定（实测报 "adaptive thinking is not supported on this model"）。
func modelSupportsAdaptiveThinking(model string) bool {
	return !isHaikuFamilyModel(model)
}

func isHaikuFamilyModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "haiku")
}

// stripUnsupportedEffort 对不支持 effort 的模型删除 output_config.effort；
// 若 output_config 因此变成空对象则整体删除，避免上游对空对象另行报错。
func stripUnsupportedEffort(body []byte, model string) ([]byte, bool) {
	if modelSupportsEffort(model) {
		return body, false
	}
	if !gjson.GetBytes(body, "output_config.effort").Exists() {
		return body, false
	}
	out, err := sjson.DeleteBytes(body, "output_config.effort")
	if err != nil {
		return body, false
	}
	if cfg := gjson.GetBytes(out, "output_config"); cfg.IsObject() && len(cfg.Map()) == 0 {
		if b, err := sjson.DeleteBytes(out, "output_config"); err == nil {
			out = b
		}
	}
	return out, true
}

// normalizeAdaptiveThinking 把不支持 adaptive 的模型上的 thinking.type
// 从 "adaptive" 改为 "enabled"，并在缺失时补上 budget_tokens。
//
// 选择转换而非删除，是为了保留客户端"要思考"的意图：直接删掉 thinking 会静默
// 改变生成行为。
func normalizeAdaptiveThinking(body []byte, model string) ([]byte, bool) {
	if modelSupportsAdaptiveThinking(model) {
		return body, false
	}
	thinking := gjson.GetBytes(body, "thinking")
	if !thinking.IsObject() || thinking.Get("type").String() != "adaptive" {
		return body, false
	}

	out, err := sjson.SetBytes(body, "thinking.type", "enabled")
	if err != nil {
		return body, false
	}
	if !gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
		if b, err := sjson.SetBytes(out, "thinking.budget_tokens", defaultThinkingBudgetTokens); err == nil {
			out = b
		}
	}
	return out, true
}

// downgradeEffortWithoutThinking 在 thinking 未启用时把 max/xhigh 降级为 high。
//
// 上游对此有明确要求（"Use effort 'high' or below, or enable thinking"）。
// 选择降级 effort 而不是替客户端打开 thinking：后者会显著改变输出形态与计费，
// 而 thinking 关闭时高 effort 档位本就没有实际意义。
func downgradeEffortWithoutThinking(body []byte) ([]byte, bool) {
	effort := strings.ToLower(gjson.GetBytes(body, "output_config.effort").String())
	if _, needsThinking := effortLevelsRequiringThinking[effort]; !needsThinking {
		return body, false
	}
	if isThinkingEnabledInBody(body) {
		return body, false
	}
	out, err := sjson.SetBytes(body, "output_config.effort", effortDowngradeTarget)
	if err != nil {
		return body, false
	}
	return out, true
}

// isThinkingEnabledInBody 判断 body 中的 thinking 是否处于开启状态。
// Anthropic 的开启态有两种：enabled（手动预算）与 adaptive（自适应）。
func isThinkingEnabledInBody(body []byte) bool {
	t := gjson.GetBytes(body, "thinking.type").String()
	return t == "enabled" || t == "adaptive"
}
