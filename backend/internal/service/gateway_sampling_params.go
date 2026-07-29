package service

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// stripDeprecatedSamplingParams 在转发前按目标模型净化采样参数
// （temperature / top_p / top_k），与 sanitizeAnthropicBodyForBetaTokens 同属
// "body 与上游 schema 对称" 的前置净化。
//
// 问题场景：
//  1. 废弃（新世代模型）：
//     - 下游客户端（多数框架默认就带 temperature）把采样参数透传进来
//     - Anthropic 新模型只要字段存在就 400 "`temperature` is deprecated..."
//     - 老模型仍然接受，不能无条件删——那会静默改变用户的生成行为
//  2. 互斥（仍支持采样的老模型）：
//     - 上游不允许同一次请求同时指定 temperature 与 top_p
//     - 生产实测 400；固定保留 temperature、删除 top_p（见
//       stripMutuallyExclusiveSamplingParams）
//
// 因此按模型判定：白名单内的老模型只做互斥处理，其余（含未来新模型）剥离全部。
// 判定见 claude.SupportsSamplingParams。
//
// model 为空时回退读 body.model，方便透传路径直接调用。
//
// 返回 (sanitized, changed)：changed 表示是否发生实际删除，供调用方决定
// 是否重用原 body 引用。
func stripDeprecatedSamplingParams(body []byte, model string) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}
	if model == "" {
		model = gjson.GetBytes(body, "model").String()
	}
	if claude.SupportsSamplingParams(model) {
		// 老模型仍接受采样参数：不能全剥，但必须消掉 temperature/top_p 互斥冲突。
		return stripMutuallyExclusiveSamplingParams(body)
	}
	return forceStripSamplingParams(body)
}

// stripMutuallyExclusiveSamplingParams 在仍支持采样参数的模型上，若 temperature
// 与 top_p 同时存在则删除 top_p，保留 temperature。
//
// Anthropic 老模型允许二者各自单独使用，但同一次请求不能同时指定（生产 400）。
// 固定保留 temperature：多数 SDK/框架默认只发 temperature；显式 top_p 相对少见，
// 且与 temperature 同时出现时 top_p 往往是框架附带的默认值。
//
// 只带其中一个、或两者都不带时原样返回。
func stripMutuallyExclusiveSamplingParams(body []byte) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}
	if !gjson.GetBytes(body, "temperature").Exists() || !gjson.GetBytes(body, "top_p").Exists() {
		return body, false
	}
	out, err := sjson.DeleteBytes(body, "top_p")
	if err != nil {
		logger.LegacyPrintf("service.gateway",
			"[SamplingParamStrip] sjson.DeleteBytes(top_p) failed unexpectedly: %v (body len=%d). "+
				"upstream may still reject the request as temperature/top_p mutual exclusion.", err, len(body))
		return body, false
	}
	return out, true
}

// forceStripSamplingParams 无条件剥离采样参数，不做模型判定。
//
// 供上游已经明确回了 "`temperature` is deprecated for this model." 的 400
// 兜底重试使用：此时上游已经给出权威答案，模型白名单判断错了，直接删。
func forceStripSamplingParams(body []byte) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}

	out := body
	changed := false
	for _, field := range claude.SamplingParamFields {
		if !gjson.GetBytes(out, field).Exists() {
			continue
		}
		stripped, err := sjson.DeleteBytes(out, field)
		if err != nil {
			// 不应发生：gjson 刚验证过字段存在 + body 是合法 JSON。
			// 保守起见保留已删除的部分，并记录以便运维定位残留的 400。
			logger.LegacyPrintf("service.gateway",
				"[SamplingParamStrip] sjson.DeleteBytes(%s) failed unexpectedly: %v (body len=%d). "+
					"upstream may still reject the request as deprecated-param.", field, err, len(out))
			continue
		}
		out = stripped
		changed = true
	}
	return out, changed
}
