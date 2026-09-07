package handler

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// clientPlatformSessionTTL 是「会话首次判定平台」记录的保存时长。会话粘性本身最长
// 也就几十分钟到几小时，24 小时足够覆盖一段对话的整个生命周期。
const clientPlatformSessionTTL = 24 * time.Hour

// observeClientPlatform 是按平台分池（审计 C-1）的观察期：只判定、只计数，不影响路由。
//
// 产出三类数字（Redis，见 repository/client_platform_stats_cache.go）：
//   - 各平台请求占比，按判定来源（env 块 / 头 / 无）与客户端（CC / 其它）拆分
//   - 同一会话内判定是否稳定（flip 计数）——决定分池后能否把判定跟粘性绑死
//   - 非 CC 客户端占比——它们只能归默认池
//
// 计数口径：挂在会话 hash 生成之后，也就是在非 CC 拒绝、本地拦截（探针/问候/重复
// payload）、安全审计、并发与计费门槛之后。所以统计的是「真正走到选号的请求」，
// 不是网关入口的全部流量——这正是分池要服务的那部分。
//
// 全程 fail-open：Redis 不可用或未注入时只记一条 debug。
func (h *GatewayHandler) observeClientPlatform(c *gin.Context, body []byte, sessionHash string, isClaudeCode bool, reqLog *zap.Logger) {
	if c == nil || c.Request == nil {
		return
	}
	obs := service.ClassifyClientPlatform(c.Request.Header, body)
	field := service.ClientPlatformStatField(obs, isClaudeCode)

	if reqLog != nil {
		reqLog.Debug("gateway.client_platform_observed",
			zap.String("platform", string(obs.Platform)),
			zap.String("source", string(obs.Source)),
			zap.Bool("is_claude_code", isClaudeCode),
			zap.String("session_hash", shortSessionHashForLog(sessionHash)),
		)
	}

	// 断言失败即未注入（或实现不支持）；typed-nil 的情况由实现自身的 nil 守卫兜住。
	stats, ok := h.repeatPayloadCache.(service.ClientPlatformStatsCache)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	day := time.Now().UTC().Format("2006-01-02")
	if err := stats.IncrClientPlatformStat(ctx, day, field); err != nil && reqLog != nil {
		reqLog.Debug("gateway.client_platform_stat_failed", zap.Error(err))
		return
	}
	if obs.Platform == service.ClientPlatformUnknown {
		return
	}
	// 真 CC 的请求头里就有各平台的 stainless 身份组合，顺手记下来：分池第二阶段给
	// Windows / Linux 账号配头时直接用，不必在对应系统上跑客户端。只记头，不记正文。
	if isClaudeCode {
		if field := service.ClientIdentityStatField(c.Request.Header, obs.Platform); field != "" {
			_ = stats.IncrClientIdentityStat(ctx, day, field)
		}
	}
	if sessionHash == "" {
		return
	}
	prev, err := stats.RecordSessionClientPlatform(ctx, sessionHash, string(obs.Platform), clientPlatformSessionTTL)
	if err != nil {
		if reqLog != nil {
			reqLog.Debug("gateway.client_platform_session_failed", zap.Error(err))
		}
		return
	}
	if prev != "" && prev != string(obs.Platform) {
		// 同一会话内判定变了：分池后这会是一次「因平台换号」的风险点，观察期先数清楚。
		_ = stats.IncrClientPlatformStat(ctx, day, "flip|"+prev+"->"+string(obs.Platform))
	}
}

func shortSessionHashForLog(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}
