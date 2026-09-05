package handler

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// 小请求重复探活的本地拦截。
//
// 三层拦截各管一段（见 config.RepeatSmallProbeConfig 的背景）：探针工具看 tools、
// 问候/测活看固定文案、大请求刷号看 200KB 以上的重复。中转商发的「不在名单里、
// 带 session、500 字节、每 3 分钟一次」的固定 ping 正好落在缝里。这里补的是
// 「小请求 + 高频重复」这一档，判据是行为不是内容，所以不用追着文案改名单。
//
// 拦截动作是**回一句问候（200）**而不是 4xx：对方在测活，回错误它会把我们标为
// 不可用并切走流量；回正常问候它认为通道健康、继续用我们，但我们不再为每次探活
// 付上游成本和账号请求次数。与 probe_intercept.go 的问候拦截同一哲学。

// interceptRepeatSmallProbe 统计并在超过阈值时本地回复重复的小请求。
// 返回 true 表示响应已写出，调用方必须立即返回。
//
// 必须在 ParseGatewayRequest 之后、账号选择之前调用。
func (h *GatewayHandler) interceptRepeatSmallProbe(
	c *gin.Context,
	parsed *service.ParsedRequest,
	body []byte,
	model string,
	stream bool,
	apiKeyID int64,
	reqLog *zap.Logger,
) bool {
	if h == nil || h.cfg == nil || c == nil || c.Request == nil || parsed == nil || parsed.Body == nil {
		return false
	}
	guard := h.cfg.Gateway.RepeatPayloadGuard
	sp := guard.SmallProbe
	mode := sp.NormalizedMode()
	if mode == config.RepeatPayloadGuardModeOff {
		return false
	}

	// 形态门槛，三条全过才碰计数器（Redis 往返不是免费的）：
	//   体积 ≤ 上限     —— 更大的归 rejectRepeatPayload 管
	//   无 tools        —— 真实 Claude Code 会话必带工具，这是最强的「非真人」信号
	//   单条 user 消息  —— 多轮对话不是探活形态
	if parsed.Body.Len() > sp.MaxBodyBytes {
		return false
	}
	if !isSingleUserTurnWithoutTools(body) {
		return false
	}

	// 指纹沿用大请求那套：只取 messages，不含 system / metadata。
	// system 里的 billing header 与 metadata 里的 session_id 每次都变，含进去永远不命中。
	fingerprint, ok := service.RepeatPayloadFingerprint(parsed)
	if !ok {
		return false
	}

	// fail-open：cache 未注入或 Redis 报错时放行，不能成为网关的新单点。
	if h.repeatPayloadCache == nil {
		return false
	}
	window := time.Duration(guard.WindowMinutes) * time.Minute
	count, err := h.repeatPayloadCache.IncrementRepeatCount(c.Request.Context(),
		service.RepeatPayloadScopeSmallProbe, apiKeyID, fingerprint, window)
	if err != nil {
		if reqLog != nil {
			reqLog.Warn("gateway.repeat_small_probe_count_failed",
				zap.Error(err), zap.String("fingerprint", fingerprint))
		}
		return false
	}
	if count <= int64(sp.Threshold) {
		return false
	}

	// Warn 级别会被 ops_system_log_sink 自动收进 ops_system_logs，排查用
	// `gateway.repeat_small_probe_detected`。
	if reqLog != nil {
		reqLog.Warn("gateway.repeat_small_probe_detected",
			zap.String("mode", mode),
			zap.String("fingerprint", fingerprint),
			zap.Int64("repeat_count", count),
			zap.Int("threshold", sp.Threshold),
			zap.Int("window_minutes", guard.WindowMinutes),
			zap.Int("body_bytes", parsed.Body.Len()),
			zap.String("model", model),
			zap.Bool("stream", stream),
			zap.Bool("has_session", hasSessionMarker(body)),
			zap.String("user_agent", c.Request.UserAgent()))
	}

	if mode != config.RepeatPayloadGuardModeBlock {
		return false
	}

	// 回 200 问候，所以不需要 MarkOpsClientBusinessLimited（ops 错误采集只看 ≥400）。
	text := greetingReplyText(body)
	if stream {
		sendGreetingInterceptStream(c, model, text)
	} else {
		sendGreetingInterceptResponse(c, model, text)
	}
	return true
}

// isSingleUserTurnWithoutTools 判断请求是否是「无 tools、恰好一条 user 消息」的形态。
// 与 isLivenessProbeRequest 的宽松档共用同一组结构判据，但不看内容。
func isSingleUserTurnWithoutTools(body []byte) bool {
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
	return len(arr) == 1 && arr[0].Get("role").String() == "user"
}
