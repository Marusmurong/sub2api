package service

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)

// anthropicLongContextCreditsMarker 是「长上下文需要额外用量」拒绝的响应体特征。
//
// 2026-09-24 生产实测（account 175, req_011CfN3vdPBLd7U31ye1LzX2）：
//
//	HTTP 429
//	{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for long context requests."}}
//	X-Should-Retry: false
//	Anthropic-Ratelimit-Unified-Overage-Disabled-Reason: org_level_disabled
//	Anthropic-Ratelimit-Unified-Reset: 1790812800   ← 计费周期重置点，不是限流解除点
//	（无任何 5h/7d per-window 头）
//
// 这是对「这一个请求」的拒绝：组织没开 overage，超长上下文不给用；账号本身 5h 用量 4%、
// 7d 用量 26%，普通请求全部 200。此前 handle429 按聚合 reset 头把整号锁到 10-01，
// 一个请求让一个健康账号停摆 6 天多。
const anthropicLongContextCreditsMarker = "usage credits are required for long context"

// isAnthropicRequestScoped429 报告这个 429 是否只针对本次请求、与账号额度无关。
//
// 判据取响应体消息而非 Overage-Disabled-Reason 头：后者在真实配额耗尽的 429 上也可能出现。
// 共享 5h/7d 窗口明确耗尽时仍按真限流处理——那时换号、冷却都是对的。
func isAnthropicRequestScoped429(account *Account, statusCode int, headers http.Header, body []byte) bool {
	if account == nil || account.Platform != PlatformAnthropic || statusCode != http.StatusTooManyRequests {
		return false
	}
	if selectAnthropicExhaustedWindow(headers, time.Now()) != nil {
		return false
	}
	msg := strings.ToLower(extractUpstreamErrorMessage(body))
	return strings.Contains(msg, anthropicLongContextCreditsMarker)
}

// writeAnthropicRequestScoped429 把请求级 429 原样交还客户端：不冷却账号、不换号。
//
// 换号必然是同一个拒绝（组织级开关），还会把下一个号也拖进来。带上 X-Should-Retry: false
// 与上游一致，Claude Code 据此不会拿同一个超长请求反复重试。
func (s *GatewayService) writeAnthropicRequestScoped429(c *gin.Context, account *Account, resp *http.Response, body []byte) error {
	upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))

	logger.LegacyPrintf("service.gateway", "[Forward] Upstream request-scoped 429 (no cooldown, no failover): Account=%d(%s) RequestID=%s Message=%s",
		account.ID, account.Name, upstreamRequestID(resp.Header), upstreamMsg)

	upstreamDetail := ""
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		upstreamDetail = truncateString(string(body), s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes)
	}
	setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:            opsUpstreamProxyID(account),
		ProxyName:          opsUpstreamProxyName(account),
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  upstreamRequestID(resp.Header),
		Kind:               "request_scoped",
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})

	MarkResponseCommitted(c)
	c.Header("X-Should-Retry", "false")
	c.JSON(http.StatusTooManyRequests, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    "rate_limit_error",
			"message": upstreamMsg,
		},
	})
	return fmt.Errorf("upstream error: %d request-scoped message=%s", resp.StatusCode, upstreamMsg)
}
