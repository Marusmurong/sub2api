package admin

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// SetReclaudeRuntime 注入 reclaude 运行时。
//
// 用 setter 而不是加构造参数：AccountHandler 的构造函数已经有十七个参数且被
// 大量测试引用，而这个依赖允许缺席 —— 缺席时两个端点如实报「未启用」，
// 不影响其它账号类型。
func (h *AccountHandler) SetReclaudeRuntime(runtime *service.ReclaudeRuntime) {
	if h == nil {
		return
	}
	h.reclaudeRuntime = runtime
}

// ReclaudeSelfCheck 跑一次建号后的连通性自检
// POST /api/v1/admin/accounts/:id/reclaude-self-check
//
// 三步全绿才把账号置为可调度。失败**不改动账号状态** —— 自检是个可以随便点的
// 按钮，一次网络抖动不该把正在跑的账号打下线（真失效由 §6.7 的网关错误映射停号）。
func (h *AccountHandler) ReclaudeSelfCheck(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	if h.reclaudeRuntime == nil || h.reclaudeRuntime.SelfChecker == nil {
		response.BadRequest(c, "reclaude runtime is not configured")
		return
	}

	result, err := h.reclaudeRuntime.SelfChecker.Run(c.Request.Context(), accountID)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, result)
}

// ReclaudeDailyUsage 读今日配额包水位
// GET /api/v1/admin/accounts/:id/reclaude-usage
//
// 🔴 水位只有我们自己的计数这一个信源（对方不提供任何用量接口）。读不到时
// **报错而不是回零**：回 0 会被读成「今天还没用」，而真相是「我们不知道」。
func (h *AccountHandler) ReclaudeDailyUsage(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	if h.reclaudeRuntime == nil || h.reclaudeRuntime.QuotaGate == nil {
		response.BadRequest(c, "reclaude runtime is not configured")
		return
	}

	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	snapshot, err := h.reclaudeRuntime.QuotaGate.TodayUsage(c.Request.Context(), account)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, snapshot)
}
