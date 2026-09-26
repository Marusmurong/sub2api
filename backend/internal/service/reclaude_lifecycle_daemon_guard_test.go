//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// E1（RECLAUDE_REVOCATION_VERIFY_PLAN_2026-09-26 §3.1）：daemon 模式下不合成生命周期流量。
//
// Do 在进入 daemon 分支之前就调用了 OnInference。daemon 模式下本机真客户端自己会发
// 引导 / 遥测，我们再补一份就是双份；而且我们的合成 GET 会带着自己的缺陷
// （content-length: 0、mcp-registry 带 auth）经 daemon 转出去，把对照实验搞脏。
func TestReclaudeLifecycleDriverSkipsDaemonMode(t *testing.T) {
	ctx := context.Background()

	t.Run("transparent 口配置时不发", func(t *testing.T) {
		driver, forwarder, account := driverFixture(t)
		account.Extra = map[string]any{ExtraKeyReclaudeDaemonEndpoint: "https://127.0.0.1:40997"}

		driver.OnInference(ctx, account, "http://proxy:8080", "claude-opus-5-5")
		time.Sleep(50 * time.Millisecond)

		require.Empty(t, forwarder.snapshot())
	})

	t.Run("正向代理口配置时不发", func(t *testing.T) {
		driver, forwarder, account := driverFixture(t)
		account.Extra = map[string]any{ExtraKeyReclaudeDaemonProxy: "http://127.0.0.1:40996"}

		driver.OnInference(ctx, account, "http://proxy:8080", "claude-opus-5-5")
		time.Sleep(50 * time.Millisecond)

		require.Empty(t, forwarder.snapshot())
	})

	t.Run("非 daemon 账号照常发", func(t *testing.T) {
		driver, forwarder, account := driverFixture(t)

		driver.OnInference(ctx, account, "http://proxy:8080", "claude-opus-5-5")

		require.NotEmpty(t, waitForCalls(t, forwarder, 1))
	})
}
