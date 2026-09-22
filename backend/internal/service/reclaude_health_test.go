package service

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type recordingHeartbeatProbe struct {
	mu      sync.Mutex
	targets []string
}

func (p *recordingHeartbeatProbe) ProbeReclaudeEndpoint(
	_ context.Context, _ *Account, endpoint string,
) (*http.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.targets = append(p.targets, endpoint)
	return &http.Response{StatusCode: 200}, nil
}

func (p *recordingHeartbeatProbe) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.targets...)
}

// 心跳周期对齐原版 daemon 的 60s，不是几十分钟。
// 一台每 45 分钟才心跳一次的设备，在他们的时序里是明显稀疏的。
func TestReclaudeHeartbeatInterval(t *testing.T) {
	require.Equal(t, time.Minute, ReclaudeHeartbeatInterval)
}

func TestReclaudeHeartbeat_Endpoints(t *testing.T) {
	account := &Account{ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude}

	t.Run("打原版 daemon 的两个 60s 端点", func(t *testing.T) {
		probe := &recordingHeartbeatProbe{}
		runner := NewReclaudeHeartbeatRunner(probe)

		require.NoError(t, runner.Beat(context.Background(), account))

		require.Equal(t, []string{
			ReclaudeInterceptDomainsPath,
			ReclaudeClientAccountPath,
		}, probe.snapshot())
	})

	t.Run("不发遥测", func(t *testing.T) {
		probe := &recordingHeartbeatProbe{}
		runner := NewReclaudeHeartbeatRunner(probe)

		require.NoError(t, runner.Beat(context.Background(), account))

		require.NotContains(t, probe.snapshot(), ReclaudeTelemetryPath,
			"遥测会暴露 TTFT 与流量规模，且不是 /proxy 的必要条件")
	})

	// 模拟关机期间必须**整段**停掉心跳：只停调度不停心跳，
	// 设备页面的「最近使用」照常刷新，等于关机没生效。
	t.Run("离线窗口内不心跳", func(t *testing.T) {
		offline := offlineAccount(t, "UTC", 0, 23)
		probe := &recordingHeartbeatProbe{}
		runner := NewReclaudeHeartbeatRunner(probe)

		require.NoError(t, runner.BeatAt(context.Background(), offline,
			time.Date(2026, 9, 21, 3, 0, 0, 0, time.UTC)))

		require.Empty(t, probe.snapshot(), "模拟关机期间还在心跳")
	})

	t.Run("非 reclaude 账号不心跳", func(t *testing.T) {
		probe := &recordingHeartbeatProbe{}
		runner := NewReclaudeHeartbeatRunner(probe)

		require.NoError(t, runner.Beat(context.Background(),
			&Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}))

		require.Empty(t, probe.snapshot())
	})

	t.Run("探测器缺席时不 panic", func(t *testing.T) {
		require.NotPanics(t, func() {
			_ = NewReclaudeHeartbeatRunner(nil).Beat(context.Background(), account)
		})
	})
}

// 🔴 多副本下每个进程各跑一份 ticker ⇒ 每台"设备"每分钟发 6 个请求、
// 来自 3 个 TCP 会话 —— 在对端看来是一台机器同时开着 3 个 daemon。
func TestReclaudeHeartbeatRequiresLeaderLock(t *testing.T) {
	require.NotEmpty(t, ReclaudeHeartbeatLeaderLockKey)
	require.Greater(t, ReclaudeHeartbeatLeaderLockTTL, ReclaudeHeartbeatInterval,
		"锁的 TTL 必须长于心跳周期，否则每个周期都会重新选主、多副本同时发")
}
