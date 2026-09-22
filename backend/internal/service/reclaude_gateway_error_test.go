package service

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClassifyReclaudeGatewayStatus(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   ReclaudeAccountActionKind
	}{
		{"401 = 凭据失效", http.StatusUnauthorized, ReclaudeActionCredentialRevoked},
		{"403 = 凭据失效", http.StatusForbidden, ReclaudeActionCredentialRevoked},
		{"429 = 他们侧限流，走冷却", http.StatusTooManyRequests, ReclaudeActionCooldown},
		{"500 = 节点不可用", http.StatusInternalServerError, ReclaudeActionGatewayUnavailable},
		{"502 = 节点不可用", http.StatusBadGateway, ReclaudeActionGatewayUnavailable},
		{"503 = 节点不可用", http.StatusServiceUnavailable, ReclaudeActionGatewayUnavailable},
		// 网关不该回 404/400：出现了就说明端点或协议变了，必须告警而不是静默。
		{"404 = 协议变了", http.StatusNotFound, ReclaudeActionUnknownEvent},
		{"418 = 协议变了", http.StatusTeapot, ReclaudeActionUnknownEvent},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, ClassifyReclaudeGatewayStatus(tc.status))
		})
	}
}

func TestReclaudeAccountService_GatewayFailures(t *testing.T) {
	t.Run("凭据失效置 error 并停止调度，且必须告警", func(t *testing.T) {
		service, store, alerter := reclaudeActuatorFixture(t)

		service.ApplyReclaudeAction(ReclaudeAccountAction{
			Kind: ReclaudeActionCredentialRevoked, AccountID: 9, Reason: "gateway status 401",
		})

		// 🔴 凭据失效没有自动降级路径（R-2 禁止混池、§6.8-B 禁止跨设备兜底），
		// 恢复要走「买新订阅 + 新人设」的多小时 runbook ⇒ 必须立刻有人看见。
		require.Equal(t, 1, alerter.count())
		require.Contains(t, store.errorMessages[9], "401")
		// 不写冷却：冷却会到期自动恢复调度，而凭据失效不会自己好。
		require.True(t, store.cooldown[9].until.IsZero())
	})

	t.Run("节点不可用写短冷却，理由与关机/限流可区分", func(t *testing.T) {
		service, store, alerter := reclaudeActuatorFixture(t)

		service.ApplyReclaudeAction(ReclaudeAccountAction{
			Kind: ReclaudeActionGatewayUnavailable, AccountID: 9,
		})

		require.Empty(t, store.errorMessages[9])
		require.Equal(t, ReclaudeGatewayUnavailableReason, store.cooldown[9].reason)
		require.NotEqual(t, ReclaudeOfflineReason, store.cooldown[9].reason)
		require.NotEqual(t, ReclaudeRateLimitedReason, store.cooldown[9].reason)
		require.WithinDuration(t,
			time.Now().Add(ReclaudeGatewayUnavailableCooldown), store.cooldown[9].until, 5*time.Second)
		require.Equal(t, 1, alerter.count())
	})

	t.Run("冷却时长封顶", func(t *testing.T) {
		// 🔴 冷却时长来自底层那个 Claude 账号的窗口。换号之后它指向一个
		// 已经不相关的时间点，不封顶就是一台设备白白躺几个小时。
		service, store, _ := reclaudeActuatorFixture(t)

		service.ApplyReclaudeAction(ReclaudeAccountAction{
			Kind: ReclaudeActionCooldown, AccountID: 9, RetryAfterSec: 6 * 60 * 60,
		})

		require.WithinDuration(t,
			time.Now().Add(ReclaudeMaxCooldown), store.cooldown[9].until, 5*time.Second)
	})
}

func TestReclaudeEventDispatcher_GatewayError(t *testing.T) {
	t.Run("映射成动作并交给执行器", func(t *testing.T) {
		actuator := &recordingActuator{}
		dispatcher := NewReclaudeEventDispatcher(actuator, time.Minute)

		dispatcher.HandleReclaudeGatewayError(
			&Account{ID: 9, Type: AccountTypeReclaude}, http.StatusUnauthorized)

		require.Len(t, actuator.actions, 1)
		require.Equal(t, ReclaudeActionCredentialRevoked, actuator.actions[0].kind)
		require.Equal(t, int64(9), actuator.actions[0].accountID)
	})

	t.Run("窗口内去重", func(t *testing.T) {
		// 一个 401 会在窗口内被每一次重试、每一个并发请求各触发一遍。
		actuator := &recordingActuator{}
		dispatcher := NewReclaudeEventDispatcher(actuator, time.Minute)
		account := &Account{ID: 9, Type: AccountTypeReclaude}

		for i := 0; i < 5; i++ {
			dispatcher.HandleReclaudeGatewayError(account, http.StatusUnauthorized)
		}

		require.Len(t, actuator.actions, 1)
	})

	t.Run("不同账号互不吃掉", func(t *testing.T) {
		actuator := &recordingActuator{}
		dispatcher := NewReclaudeEventDispatcher(actuator, time.Minute)

		dispatcher.HandleReclaudeGatewayError(&Account{ID: 9, Type: AccountTypeReclaude}, 401)
		dispatcher.HandleReclaudeGatewayError(&Account{ID: 10, Type: AccountTypeReclaude}, 401)

		require.Len(t, actuator.actions, 2)
	})
}

func TestReclaudeUpstream_ReportsGatewayError(t *testing.T) {
	account, cipher := reclaudeTestAccount(t)
	actuator := &recordingActuator{}
	dispatcher := NewReclaudeEventDispatcher(actuator, time.Minute)

	upstream := NewReclaudeUpstream(&capturingUpstream{response: &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"error":"revoked device"}`)),
	}}, cipher, dispatcher)

	_, err := upstream.Do(innerRequest(t, "{}"), account, "")

	var gatewayErr *ReclaudeGatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, http.StatusUnauthorized, gatewayErr.StatusCode)
	// 🔴 绝不透传他们的 body：那是 reclaude 自己的错误结构，不是 Anthropic 格式。
	require.NotContains(t, err.Error(), "revoked device")
	require.Len(t, actuator.actions, 1)
	require.Equal(t, ReclaudeActionCredentialRevoked, actuator.actions[0].kind)
}
