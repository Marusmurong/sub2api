package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProvideReclaudeRuntime(t *testing.T) {
	t.Run("装配出完整的三件套", func(t *testing.T) {
		runtime := ProvideReclaudeRuntime(nil, nil, nil, nil, nil)

		require.NotNil(t, runtime)
		require.NotNil(t, runtime.Upstream)
		require.NotNil(t, runtime.QuotaGate)
		require.NotNil(t, runtime.Scheduler)
		// 事件旁路必须接上执行器：不接的话换号事件到不了身份纪元，
		// 旧 prev_request_id 会被当成真 parent-link 发给新账号。
		require.NotNil(t, runtime.Upstream.events)
		// 日闸的**写入端**必须接上转发器：只接读取端的话水位恒为 0，
		// 闸门永远不触发 ⇒ 配额包被无限放行。
		require.NotNil(t, runtime.Upstream.quota)
	})

	t.Run("AttachTo 之后 reclaude 请求不再撞未接线错误", func(t *testing.T) {
		gateway := &GatewayService{}
		ProvideReclaudeRuntime(nil, nil, nil, nil, nil).AttachTo(gateway, nil)

		require.NotNil(t, gateway.reclaudeUpstream)
		require.NotNil(t, gateway.reclaudeQuota)

		req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
		require.NoError(t, err)
		_, err = gateway.doUpstream(req, "", &Account{ID: 1, Type: AccountTypeReclaude}, nil)

		// 仍然会失败（没有凭据），但**不能**再是「没接线」。
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrReclaudeUpstreamNotWired)
	})

	t.Run("Start / Stop 幂等且不 panic", func(t *testing.T) {
		runtime := ProvideReclaudeRuntime(nil, nil, nil, nil, nil)

		runtime.Start()
		runtime.Start()
		runtime.Stop()
		runtime.Stop()
	})

	t.Run("nil runtime 的方法全部安全", func(t *testing.T) {
		var runtime *ReclaudeRuntime
		runtime.AttachTo(&GatewayService{}, nil)
		runtime.Start()
		runtime.Stop()
	})
}

func TestReclaudeScheduler_Stop(t *testing.T) {
	scheduler := NewReclaudeScheduler(nil, nil, nil, nil, nil, "inst")

	scheduler.Start(context.Background())
	scheduler.Stop()
	// 重复 Stop 不能 panic：cleanup 可能被调用两次。
	scheduler.Stop()
}
