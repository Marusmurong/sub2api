package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// accountStateProbe 对 /client/account 返回预设 JSON，其它端点返回空 200。
type accountStateProbe struct {
	body string
}

func (p *accountStateProbe) ProbeReclaudeEndpoint(
	_ context.Context, _ *Account, endpoint string,
) (*http.Response, error) {
	if endpoint != ReclaudeClientAccountPath {
		return &http.Response{StatusCode: 200}, nil
	}
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(p.body)),
	}, nil
}

func reclaudeAccountWithBoundEmail(email string) *Account {
	return &Account{
		ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude,
		Extra: map[string]any{ExtraKeyReclaudeBoundEmail: email},
	}
}

func TestReclaudeHeartbeat_DetectsAccountSwitch(t *testing.T) {
	ctx := context.Background()

	t.Run("绑定邮箱变了就当作换号", func(t *testing.T) {
		// 🔴 换号的带外事件不保证每次都到；「当前绑定邮箱」是另一条独立的可见信号。
		// 漏掉换号 ⇒ 旧 prev_request_id 被当成假 parent-link、旧 thinking 签名必拒。
		actuator := &recordingActuator{}
		runner := NewReclaudeHeartbeatRunner(&accountStateProbe{
			body: `{"user_email":"n***@example.com","account_uuid":"u-1"}`,
		})
		runner.SetAccountObserver(actuator)

		require.NoError(t, runner.Beat(ctx, reclaudeAccountWithBoundEmail("o***@example.com")))

		require.Len(t, actuator.actions, 1)
		require.Equal(t, ReclaudeActionAccountSwitched, actuator.actions[0].kind)
		require.Equal(t, "n***@example.com", actuator.actions[0].maskedEmail)
	})

	t.Run("邮箱没变就什么都不做", func(t *testing.T) {
		actuator := &recordingActuator{}
		runner := NewReclaudeHeartbeatRunner(&accountStateProbe{
			body: `{"user_email":"o***@example.com"}`,
		})
		runner.SetAccountObserver(actuator)

		require.NoError(t, runner.Beat(ctx, reclaudeAccountWithBoundEmail("o***@example.com")))

		require.Empty(t, actuator.actions)
	})

	t.Run("首次拿到邮箱只回填，不当作换号", func(t *testing.T) {
		// 建号时没有这个字段，第一次心跳拿到它不是「换号」——
		// 当成换号会白白推进身份纪元，把刚建好的会话态整体作废。
		actuator := &recordingActuator{}
		runner := NewReclaudeHeartbeatRunner(&accountStateProbe{
			body: `{"user_email":"o***@example.com"}`,
		})
		runner.SetAccountObserver(actuator)

		account := &Account{ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude}
		require.NoError(t, runner.Beat(ctx, account))

		require.Len(t, actuator.actions, 1)
		require.Equal(t, ReclaudeActionBoundEmailObserved, actuator.actions[0].kind)
	})

	t.Run("响应不是 JSON 时不影响心跳", func(t *testing.T) {
		// 心跳的首要职责是「让设备看起来还活着」。解析失败不能让它中断。
		actuator := &recordingActuator{}
		runner := NewReclaudeHeartbeatRunner(&accountStateProbe{body: "<html>nope</html>"})
		runner.SetAccountObserver(actuator)

		require.NoError(t, runner.Beat(ctx, reclaudeAccountWithBoundEmail("o***@example.com")))
		require.Empty(t, actuator.actions)
	})

	t.Run("没有观察者时行为不变", func(t *testing.T) {
		runner := NewReclaudeHeartbeatRunner(&accountStateProbe{
			body: `{"user_email":"n***@example.com"}`,
		})

		require.NoError(t, runner.Beat(ctx, reclaudeAccountWithBoundEmail("o***@example.com")))
	})
}

func TestReclaudeAccountService_BoundEmailObserved(t *testing.T) {
	t.Run("只回填邮箱，不推进身份纪元", func(t *testing.T) {
		// 🔴 推进纪元会把 prev_request_id 与签名态整体翻页。建号后的首次观测
		// 不是换号，翻页等于把刚建好的会话态白白作废。
		service, store, alerter := reclaudeActuatorFixture(t)

		service.ApplyReclaudeAction(ReclaudeAccountAction{
			Kind: ReclaudeActionBoundEmailObserved, AccountID: 9,
			NewAccountMaskedEmail: "o***@example.com",
		})

		updates := store.extra[9]
		require.Equal(t, "o***@example.com", updates[ExtraKeyReclaudeBoundEmail])
		require.NotContains(t, updates, ExtraKeyReclaudeIdentityEpoch)
		require.NotContains(t, updates, ExtraKeyReclaudeLastSwitchedAt)
		// 首次回填不是事件，不该告警。
		require.Zero(t, alerter.count())
	})

	t.Run("邮箱为空时不写库", func(t *testing.T) {
		service, store, _ := reclaudeActuatorFixture(t)

		service.ApplyReclaudeAction(ReclaudeAccountAction{
			Kind: ReclaudeActionBoundEmailObserved, AccountID: 9,
		})

		require.NotContains(t, store.extra, int64(9))
	})
}

func TestProvideReclaudeRuntime_WiresSwitchDetection(t *testing.T) {
	// 心跳的换号检测必须接上执行器，否则「绑定邮箱变了」这条独立信号白采集。
	runtime := ProvideReclaudeRuntime(nil, nil, nil, nil, nil)

	require.NotNil(t, runtime.Scheduler.heartbeat)
	require.NotNil(t, runtime.Scheduler.heartbeat.observer)
}
