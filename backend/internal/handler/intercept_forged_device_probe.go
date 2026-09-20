package handler

import (
	"regexp"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// 伪造设备身份的探针拦截。
//
// small_probe 按「同一指纹重复次数」抓探活，抓不到换模型、换 session 的巡检；
// 这里按**身份**判：真实 Claude Code 的 metadata.user_id 里 device_id 是 randomBytes(32)
// 的 64 位 hex（见 ExtractDeviceLimitID），一个带 CC 外衣却把 device_id 写成 "1" 的
// 请求只可能是伪装。判定是确定性的，不需要计数器，也不碰 Redis。
//
// 形态门槛与 small_probe 一致（小体积、无 tools、单条 user），保证真会话不会误伤：
// 真实 CC 对话必带 tools，且首轮 system+tools 通常 20KB 以上。
//
// 命中动作与 small_probe 相同：本地回一句问候（200，官方 message 形态），不发上游、
// 不占并发槽、不进账号请求计数。回错误会让对方把我们标为不可用并切走流量。

// forgedDeviceFallbackMaxBodyBytes 在 small_probe.max_body_bytes 未配置时使用。
const forgedDeviceFallbackMaxBodyBytes = 4096

var validDeviceIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// isForgedDeviceIdentity 判断 metadata.user_id 是否"非空但拿不出合法 64 位 hex 设备 ID"。
// 空值不算伪造（不带 metadata 的客户端由 claude_code_only 校验器管）。
func isForgedDeviceIdentity(metadataUserID string) (deviceID string, forged bool) {
	if metadataUserID == "" {
		return "", false
	}
	parsed := service.ParseMetadataUserID(metadataUserID)
	if parsed == nil {
		return "", true
	}
	if validDeviceIDPattern.MatchString(parsed.DeviceID) {
		return parsed.DeviceID, false
	}
	return parsed.DeviceID, true
}

// interceptForgedDeviceProbe 拦截伪造设备身份的小请求。
// 返回 true 表示响应已写出，调用方必须立即返回。
//
// 必须在 ParseGatewayRequest 之后、账号选择之前调用。
func (h *GatewayHandler) interceptForgedDeviceProbe(
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
	mode := guard.ForgedDevice.NormalizedMode()
	if mode == config.RepeatPayloadGuardModeOff {
		return false
	}

	maxBodyBytes := guard.SmallProbe.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = forgedDeviceFallbackMaxBodyBytes
	}
	if parsed.Body.Len() > maxBodyBytes {
		return false
	}
	if !isSingleUserTurnWithoutTools(body) {
		return false
	}

	deviceID, forged := isForgedDeviceIdentity(parsed.MetadataUserID)
	if !forged {
		return false
	}

	// Warn 级别会被 ops_system_log_sink 收进 ops_system_logs，排查用
	// `gateway.forged_device_probe_detected`。
	if reqLog != nil {
		reqLog.Warn("gateway.forged_device_probe_detected",
			zap.String("mode", mode),
			zap.String("device_id", truncateForLog(deviceID, 32)),
			zap.Int64("api_key_id", apiKeyID),
			zap.Int("body_bytes", parsed.Body.Len()),
			zap.String("model", model),
			zap.Bool("stream", stream),
			zap.String("user_agent", c.Request.UserAgent()))
	}

	if mode != config.RepeatPayloadGuardModeBlock {
		return false
	}

	text := greetingReplyText(body)
	if stream {
		sendGreetingInterceptStream(c, model, text)
	} else {
		sendGreetingInterceptResponse(c, model, text)
	}
	return true
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
