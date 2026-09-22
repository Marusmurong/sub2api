package admin

import (
	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
)

// ReclaudeSettingsResponse 是 reclaude 通道运行时配置的出参。
//
// 只回配置本身，不回任何凭据相关的东西 —— 这个端点是给「出事时按开关」用的，
// 不是账号详情。
type ReclaudeSettingsResponse struct {
	Enabled           bool    `json:"enabled"`
	UnknownEventAlert bool    `json:"unknown_event_alert"`
	OversellRatio     float64 `json:"oversell_ratio"`
}

// GetReclaudeSettings 读取 reclaude 通道配置
// GET /api/v1/admin/settings/reclaude
func (h *SettingHandler) GetReclaudeSettings(c *gin.Context) {
	settings, err := h.settingService.GetReclaudeSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, ReclaudeSettingsResponse{
		Enabled:           settings.Enabled,
		UnknownEventAlert: settings.UnknownEventAlert,
		OversellRatio:     settings.OversellRatio,
	})
}

// UpdateReclaudeSettingsRequest 更新 reclaude 通道配置请求。
//
// 三个字段都用指针：PUT 允许只改一项（典型场景是只翻 enabled 这一个开关），
// 非指针会把没传的字段静默重置成零值 —— 对「关闭告警」「超卖率归零」这两项
// 来说，静默重置的后果分别是失去预警和全量拒绝调度。
type UpdateReclaudeSettingsRequest struct {
	Enabled           *bool    `json:"enabled"`
	UnknownEventAlert *bool    `json:"unknown_event_alert"`
	OversellRatio     *float64 `json:"oversell_ratio"`
}

// UpdateReclaudeSettings 更新 reclaude 通道配置
// PUT /api/v1/admin/settings/reclaude
//
// 🔴 这个端点是紧急止血通道：`enabled=false` 后，全部 rec 账号**立即**不可调度。
func (h *SettingHandler) UpdateReclaudeSettings(c *gin.Context) {
	var req UpdateReclaudeSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	current, err := h.settingService.GetReclaudeSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	settings := *current
	if req.Enabled != nil {
		settings.Enabled = *req.Enabled
	}
	if req.UnknownEventAlert != nil {
		settings.UnknownEventAlert = *req.UnknownEventAlert
	}
	if req.OversellRatio != nil {
		settings.OversellRatio = *req.OversellRatio
	}

	if err := h.settingService.SetReclaudeSettings(c.Request.Context(), &settings); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	// 回读而不是回显入参：Normalized() 可能修正过越界的超卖率，
	// 前端应该看到**真正生效**的值。
	saved, err := h.settingService.GetReclaudeSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, ReclaudeSettingsResponse{
		Enabled:           saved.Enabled,
		UnknownEventAlert: saved.UnknownEventAlert,
		OversellRatio:     saved.OversellRatio,
	})
}
