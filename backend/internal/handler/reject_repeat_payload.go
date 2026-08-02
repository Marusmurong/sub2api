package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// 重复 payload 的就地拦截。
//
// 背景（2026-08-01）：账号 claude-e9b4a11a 在 17:04–17:59 收到 22 次连续请求，
// input_tokens 恒为 266715（一个 token 都不差），output_tokens 却在 12702–19442
// 之间各不相同。真实对话每轮在尾部追加内容、input 必然单调增长（同账号前一小时那段
// 就是 262461→266235 一路爬）；输入冻结而输出各异，只能是同一份固定 payload 被反复
// 提交采样。
//
// 代价：该账号未缓存输入占比 87.8%（全站其它账号 0.0%–2.1%），52 次请求吃掉 5 小时
// 窗口内 22.2% 的金额，单次成本 $2.088 是次高账号的 6.2 倍。更要紧的是它在猛灌订阅
// 账号的真实窗口额度，这种形态被上游风控盯上的风险远高于那点钱。
//
// api_keys.rate_limit_5h 一类的额度封顶只能限制花销，拦不住这个动作本身——对方完全
// 可以在额度内继续刷。所以按行为拦。
//
// 拦截在账号选择之前完成：不发上游、不占账号并发槽、不消耗配额，与
// rejectNonClaudeCodeClient 同一个位置意图。

// repeatPayloadRejectMessage 是命中后的固定文案。
//
// 说清三件事：为什么被拒、这不是配额问题、等一会儿会自己恢复——否则对方只会立刻重试，
// 那既解决不了问题，又继续在计数窗口里制造噪音。
const repeatPayloadRejectMessage = "This request was rejected because an identical large payload has been submitted repeatedly in a short window. " +
	"Re-sending the same prompt to sample many completions is not supported on this endpoint. " +
	"This is not a quota issue; the limit clears on its own once the window elapses."

// rejectRepeatPayload 统计并在超过阈值时拦下重复提交的大 payload。
// 返回 true 表示响应已写出，调用方必须立即返回。
//
// 必须在 ParseGatewayRequest 之后调用——指纹取自 parsed.MessagesRaw()。
func (h *GatewayHandler) rejectRepeatPayload(
	c *gin.Context,
	parsed *service.ParsedRequest,
	scope service.RepeatPayloadScope,
	apiKeyID int64,
	reqLog *zap.Logger,
) bool {
	if h == nil || h.cfg == nil || c == nil || c.Request == nil {
		return false
	}
	guard := h.cfg.Gateway.RepeatPayloadGuard
	mode := guard.NormalizedMode()
	if mode == config.RepeatPayloadGuardModeOff {
		return false
	}

	// 体积门槛。低于门槛的请求一律不检测，这是硬性前提而非优化：实测 api_key 84 的
	// haiku 探测请求（input 43 token / output 固定 10）24 小时重复 1353 次，大量前缀
	// 全部命中缓存的正常对话轮次 input_tokens 只有 2、单个 key 重复上千次。这些请求
	// 本来就该重复，也不费钱，不设门槛第一个被打死的就是它们。
	if parsed == nil || parsed.Body == nil || parsed.Body.Len() < guard.MinBodyBytes {
		return false
	}

	fingerprint, ok := service.RepeatPayloadFingerprint(parsed)
	if !ok {
		return false
	}

	threshold := guard.MessagesThreshold
	if scope == service.RepeatPayloadScopeCountTokens {
		threshold = guard.CountTokensThreshold
	}

	// fail-open：cache 未注入或 Redis 报错时放行。这个检测是增强防护，不能成为网关的
	// 新单点故障——全站惯例见 billing_cache_service 的 RPM 检查与 gateway_cache 的
	// 签名污染位。
	if h.repeatPayloadCache == nil {
		return false
	}
	window := time.Duration(guard.WindowMinutes) * time.Minute
	count, err := h.repeatPayloadCache.IncrementRepeatCount(c.Request.Context(), scope, apiKeyID, fingerprint, window)
	if err != nil {
		if reqLog != nil {
			reqLog.Warn("gateway.repeat_payload_count_failed",
				zap.Error(err),
				zap.String("scope", string(scope)),
				zap.String("fingerprint", fingerprint))
		}
		return false
	}
	if count <= int64(threshold) {
		return false
	}

	// Warn 级别会被 ops_system_log_sink 的过滤器自动收进 ops_system_logs，无需额外接线。
	if reqLog != nil {
		reqLog.Warn("gateway.repeat_payload_detected",
			zap.String("mode", mode),
			zap.String("scope", string(scope)),
			zap.String("fingerprint", fingerprint),
			zap.Int64("repeat_count", count),
			zap.Int("threshold", threshold),
			zap.Int("window_minutes", guard.WindowMinutes),
			zap.Int("body_bytes", parsed.Body.Len()),
			zap.String("path", c.Request.URL.Path))
	}

	if mode != config.RepeatPayloadGuardModeBlock {
		return false
	}

	// 标记为「本地策略拒绝」：策略性拒绝不是系统故障，不该计进运维监控的错误率，
	// 否则面板长期挂红、真正的上游异常反而被淹没。与 rejectNonClaudeCodeClient 同源。
	service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalPolicyDenied)

	// 429 而不是 403：这是速率性质的拒绝，窗口过期后自行恢复，语义上属于限流。
	// Retry-After 给出窗口上界，让守规矩的客户端别立刻重试。
	c.Header("Retry-After", strconv.Itoa(guard.WindowMinutes*60))
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    "rate_limit_error",
			"message": repeatPayloadRejectMessage,
		},
	})
	return true
}
