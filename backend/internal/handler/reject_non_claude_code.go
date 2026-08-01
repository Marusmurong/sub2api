package handler

import (
	"net/http"
	"regexp"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

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
	return !claudeCodeValidator.ValidateUserAgent(ua) && !officialClaudeCodeAltUAPattern.MatchString(ua)
}

// officialClaudeCodeAltUAPattern 补上 claude-code/ 这个前缀。
//
// 生产实测（拦截启用后）：UA 为 claude-code/2.1.132 的请求被拦了 124 次——那是官方
// Claude Code，只是部分版本/构建自称 claude-code/ 而不是 claude-cli/。
//
// 只在本拦截器内放宽，刻意不改 ClaudeCodeValidator 的 claudeCodeUAPattern：
// 那个正则还被 group.claude_code_only 与客户端版本闸使用，放宽它会连带改变那两处
// 的判定语义，而这里要解决的只是"别把官方客户端拦掉"。
//
// 要求带版本号（\d+\.\d+\.\d+）并锚定行首，避免 claude-coder/ 或
// my-claude-code-proxy/ 这类形似名字蒙混过关。
var officialClaudeCodeAltUAPattern = regexp.MustCompile(`(?i)^claude-code/\d+\.\d+\.\d+`)

// rejectNonClaudeCodeClient 在账号选择之前拦下第三方客户端并固定返回。
// 返回 true 表示响应已写出，调用方必须立即返回。
//
// 响应用 Anthropic 的错误信封 + 403，而不是伪造一个 200：客户端的 SDK 会把
// message 原样呈现给使用者，让对方知道该换 Claude Code，而不是拿到一句看不懂的正文。
func (h *GatewayHandler) rejectNonClaudeCodeClient(c *gin.Context, apiKeyGroupPlatform string, reqLog *zap.Logger) bool {
	if h.cfg == nil || !h.cfg.Gateway.RejectNonClaudeCodeClients {
		return false
	}
	if c == nil || c.Request == nil {
		return false
	}
	// 只对 Anthropic 生效。平台在本函数之后才被主流程解析，所以这里自行解析一次——
	// 拦截位置不能后移：后移就会先占用户并发槽与账号槽，失去"不消耗资源"的意义。
	if !rejectAppliesToPlatform(resolveRequestPlatform(c, apiKeyGroupPlatform)) {
		return false
	}
	ua := c.Request.UserAgent()
	if !isNonClaudeCodeUserAgent(ua) {
		return false
	}
	// 标记为「本地策略拒绝」：这是策略性拒绝而非系统故障，不该计进运维监控的错误率。
	// 不标记的话面板上会永远挂着一片红，真正的上游异常反而被淹没。
	// 既有的 BetaBlockedError 与 responses 子路径守卫走的是同一个标记。
	service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalPolicyDenied)

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

// rejectAppliesToPlatform 限定拦截只作用于 Anthropic。
//
// 生产事故（2026-07-31）：启用后 Grok 客户端（grok-shell）被一起拦掉、吞吐归零。
// Claude Code 伪装与 Anthropic 账号吊销的因果链只存在于 Anthropic OAuth 上，
// 其余平台的客户端本来就不该带 claude-cli 的 UA。
//
// 平台为空（解析不出）时不拦：宁可放过，也不要在判据不足时拒绝付费流量。
func rejectAppliesToPlatform(platform string) bool {
	return platform == service.PlatformAnthropic
}

// resolveRequestPlatform 复刻主流程的平台判定顺序，供拦截层提前使用。
// 顺序与 gateway_handler.go 中的解析保持一致：强制平台 → composite 解析结果 → 分组平台。
func resolveRequestPlatform(c *gin.Context, apiKeyGroupPlatform string) string {
	if forced, ok := middleware2.GetForcePlatformFromContext(c); ok {
		return forced
	}
	if resolved, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context()); ok {
		return resolved
	}
	return apiKeyGroupPlatform
}
