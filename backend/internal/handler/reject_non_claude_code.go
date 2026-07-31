package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// 非 Claude Code 客户端的就地拦截。
//
// 背景（2026-07-31）：Anthropic 在 8 小时内逐个吊销了 7 个订阅账号
//
//	Token revoked (401): OAuth access token has been revoked
//
// 吊销之前这些账号持续收到 "Third-party apps now draw from your extra usage"。
// 按下游 key 归一化后，该错误 100% 集中在一个自建 Go 中转（背后是 Go SDK /
// Python SDK / CSSwitch 等第三方工具）；共用同一批账号的其它 key 零发生——
// 所以成因在请求形态，不在账号。
//
// 伪装这类客户端做不到位：身份维度（UA / x-app / X-Stainless / billing block /
// beta / metadata / TLS）已经全部统一，抓包实测 78 个上游请求在这些维度上完全一致，
// 但错误率纹丝不动。既然做不像，就不再送上去——账号每小时倒一个的代价远高于这部分流量。
//
// 拦截在账号选择之前完成：不发上游、不占账号并发槽、不消耗配额。

// isNonClaudeCodeUserAgent 判断 UA 是否属于「应当就地拦掉」的第三方客户端。
//
// 只看 UA，刻意**不用** ClaudeCodeValidator 的全套判定。实测全站 6945 个请求里有
// 854 个 UA 正确、X-App/anthropic-beta/anthropic-version 三个头齐全，仅因 system
// prompt 相似度不达标被判非 CC——那是 Claude Code 自己的子请求形态（官方安全监视器
// 就是一例，上游 v0.1.169 才刚补上识别）。用全套判定会把这 854 个自己人一起打掉，
// 只看 UA 不会。
//
// 空 UA 按第三方处理：真实 Claude Code 一定带 claude-cli/ 前缀，不表明身份的请求
// 没有理由享受订阅账号。
func isNonClaudeCodeUserAgent(ua string) bool {
	return !claudeCodeValidator.ValidateUserAgent(ua)
}

// rejectNonClaudeCodeClient 在账号选择之前拦下第三方客户端并固定返回。
// 返回 true 表示响应已写出，调用方必须立即返回。
//
// 响应用 Anthropic 的错误信封 + 403，而不是伪造一个 200：客户端的 SDK 会把
// message 原样呈现给使用者，让对方知道该换 Claude Code，而不是拿到一句看不懂的正文。
func (h *GatewayHandler) rejectNonClaudeCodeClient(c *gin.Context, reqLog *zap.Logger) bool {
	if h.cfg == nil || !h.cfg.Gateway.RejectNonClaudeCodeClients {
		return false
	}
	if c == nil || c.Request == nil {
		return false
	}
	ua := c.Request.UserAgent()
	if !isNonClaudeCodeUserAgent(ua) {
		return false
	}
	if reqLog != nil {
		reqLog.Info("gateway.reject_non_claude_code_client",
			zap.String("user_agent", ua),
			zap.String("path", c.Request.URL.Path))
	}
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    "permission_error",
			"message": nonClaudeCodeRejectMessage,
		},
	})
	return true
}

// nonClaudeCodeRejectMessage 是固定返回文案。
//
// 说清三件事：为什么被拒、该怎么做、这不是配额问题——否则对方只会反复重试，
// 那既解决不了问题，也继续在我们的限速器上制造噪音。
const nonClaudeCodeRejectMessage = "This endpoint only serves the Claude Code client. " +
	"Requests from third-party SDKs and tools are not accepted. " +
	"Please use Claude Code (claude.ai/code). This is not a quota issue; retrying will not help."
