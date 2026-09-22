//go:build unit

package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func doReclaudeSettingsUpdate(t *testing.T, h *SettingHandler, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/reclaude", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")

	h.UpdateReclaudeSettings(c)
	return rec
}

func reclaudeSettingsPayload(t *testing.T, rec *httptest.ResponseRecorder) ReclaudeSettingsResponse {
	t.Helper()
	var envelope struct {
		Data ReclaudeSettingsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	return envelope.Data
}

func TestGetReclaudeSettings_DefaultsToDisabled(t *testing.T) {
	// 🔴 这条通道把全量明文 prompt 发给第三方网关，默认必须是关的。
	h, _ := newStepUpSwitchTestHandler(t, map[string]string{})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/reclaude", nil)
	h.GetReclaudeSettings(c)

	require.Equal(t, http.StatusOK, rec.Code)
	payload := reclaudeSettingsPayload(t, rec)
	require.False(t, payload.Enabled)
	require.True(t, payload.UnknownEventAlert)
	require.Equal(t, service.DefaultReclaudeOversellRatio, payload.OversellRatio)
}

func TestUpdateReclaudeSettings_TogglesKillSwitch(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})

	rec := doReclaudeSettingsUpdate(t, h, map[string]any{"enabled": true})

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, reclaudeSettingsPayload(t, rec).Enabled)
	require.Contains(t, repo.values[service.SettingKeyReclaudeSettings], `"enabled":true`)
}

func TestUpdateReclaudeSettings_PartialPayloadKeepsOtherFields(t *testing.T) {
	// 出事时按的是 enabled 这一个开关。非指针字段会把没传的项静默重置成零值：
	// 告警被关掉、超卖率归零（=全量拒绝调度），两种都不是调用方的本意。
	h, _ := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyReclaudeSettings: `{"enabled":true,"unknown_event_alert":false,"oversell_ratio":0.5}`,
	})

	payload := reclaudeSettingsPayload(t, doReclaudeSettingsUpdate(t, h, map[string]any{"enabled": false}))

	require.False(t, payload.Enabled)
	require.False(t, payload.UnknownEventAlert)
	require.Equal(t, 0.5, payload.OversellRatio)
}

func TestUpdateReclaudeSettings_ReturnsNormalizedValue(t *testing.T) {
	// 回读而不是回显入参：越界的超卖率被修正过，前端该看到真正生效的值。
	h, _ := newStepUpSwitchTestHandler(t, map[string]string{})

	payload := reclaudeSettingsPayload(t, doReclaudeSettingsUpdate(t, h, map[string]any{"oversell_ratio": 9}))

	require.Equal(t, service.DefaultReclaudeOversellRatio, payload.OversellRatio)
}
