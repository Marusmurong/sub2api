package service

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// stripDeprecatedSamplingParams 在转发前按目标模型剥离已废弃的采样参数
// （temperature / top_p / top_k），与 sanitizeAnthropicBodyForBetaTokens 同属
// "body 与上游 schema 对称" 的前置净化。
//
// 问题场景：
//   - 下游客户端（多数框架默认就带 temperature，且没有关闭开关）把采样参数
//     透传进来
//   - Anthropic 新世代模型只要字段存在就返回 400
//     "`temperature` is deprecated for this model."，与取值无关
//   - 老模型仍然接受，不能无条件删——那会静默改变用户的生成行为
//
// 因此按模型判定：白名单内的老模型原样透传，其余（含未来新模型）剥离。
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
		return body, false
	}
	return forceStripSamplingParams(body)
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
