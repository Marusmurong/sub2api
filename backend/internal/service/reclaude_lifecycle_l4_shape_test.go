//go:build unit

package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// E2-L4（RECLAUDE_REVOCATION_VERIFY_PLAN_2026-09-26 §4 L4）：引导集合按 09-26 真值收敛。
//
// ✅ 真值（docs/captures/reclaude-live-2026-09-26/reccap，同机存活 2.1.282）：
//
//	第一次启动（170503.998 起算）:
//	  +0      profile           axios   content-type json + cache-control no-cache
//	  +1ms    mcp_servers       axios   只 1 次（09-23 抓包是 4 次,两份真值取 09-26）
//	  +212ms  org/skills        cli
//	  +213ms  claude_cli/bootstrap  claude-code   ← 冷启动才有
//	  +214ms  penguin_mode      axios   无 content-type      ← 冷启动才有
//	  +215ms  mcp-registry #1   cli     无 auth、无 content-type
//	  +217ms  account/settings  cli
//	  +218ms  grove             cli
//	  +1293ms mcp-registry #2
//	  +1933ms mcp-registry #3
//	  +2436ms mcp-registry #4
//	第二次启动（174038.180 起算,35 分钟后）: 同上但**没有 bootstrap 和 penguin**。
//	两次启动之间的推理**没有** mcp_servers 复查（只有 mcp-proxy 握手）。
func TestBuildReclaudeBootstrapRequestsMatchesLiveCapture(t *testing.T) {
	cold := BuildReclaudeBootstrapRequests(ReclaudeBootstrapParams{
		CLIVersion: "2.1.282",
		OrgUUID:    "47be3ed1-4b36-4bae-837d-40b5106369ec",
		Model:      "claude-opus-5-5",
		Cold:       true,
	})
	warm := BuildReclaudeBootstrapRequests(ReclaudeBootstrapParams{
		CLIVersion: "2.1.282",
		OrgUUID:    "47be3ed1-4b36-4bae-837d-40b5106369ec",
		Model:      "claude-opus-5-5",
	})

	countURL := func(reqs []ReclaudeLifecycleRequest, frag string) int {
		n := 0
		for _, r := range reqs {
			if strings.Contains(r.URL, frag) {
				n++
			}
		}
		return n
	}
	find := func(reqs []ReclaudeLifecycleRequest, frag string) ReclaudeLifecycleRequest {
		for _, r := range reqs {
			if strings.Contains(r.URL, frag) {
				return r
			}
		}
		t.Fatalf("没有找到 %s", frag)
		return ReclaudeLifecycleRequest{}
	}

	t.Run("冷启动 12 条,暖启动 10 条(少 bootstrap 与 penguin)", func(t *testing.T) {
		// +1 model_selector（2026-09-27 完整会话抓包补齐，跟在 skills 之后）。
		require.Len(t, cold, 12)
		require.Len(t, warm, 10)
		require.Equal(t, 1, countURL(cold, "/api/claude_cli/bootstrap"))
		require.Equal(t, 1, countURL(cold, "/api/claude_code_penguin_mode"))
		require.Equal(t, 0, countURL(warm, "/api/claude_cli/bootstrap"))
		require.Equal(t, 0, countURL(warm, "/api/claude_code_penguin_mode"))
	})

	t.Run("model_selector/cc 跟在 skills 之后（org 端点，client-platform 跟 sdk-cli 画像）", func(t *testing.T) {
		require.Equal(t, 1, countURL(cold, "/model_selector/cc"))
		require.Equal(t, 1, countURL(warm, "/model_selector/cc"))
		ms := find(cold, "/model_selector/cc")
		require.Equal(t, "GET", ms.Method)
		require.True(t, ms.NeedsAuthorization)
		// 🔴 sdk-cli 画像（2026-09-28 翻回）：claude_code_sdk。
		require.Equal(t, "claude_code_sdk", ms.Headers["anthropic-client-platform"])
		require.Equal(t, "47be3ed1-4b36-4bae-837d-40b5106369ec", ms.Headers["x-organization-uuid"])
		require.Equal(t, "2023-06-01", ms.Headers["anthropic-version"])
		require.Equal(t, "claude-cli/2.1.282 (external, sdk-cli)", ms.Headers["user-agent"])
		// 时序：紧跟 skills(212ms)。
		require.Equal(t, 219*time.Millisecond, ms.Delay)
	})

	t.Run("skills 缺 org 时 model_selector 也跳过", func(t *testing.T) {
		noOrg := BuildReclaudeBootstrapRequests(ReclaudeBootstrapParams{
			CLIVersion: "2.1.282", Model: "claude-opus-5-5", Cold: true,
		})
		require.Equal(t, 0, countURL(noOrg, "/model_selector/cc"))
		require.Equal(t, 0, countURL(noOrg, "/skills/list-skills"))
	})

	t.Run("mcp_servers 只 1 次,mcp-registry 4 次", func(t *testing.T) {
		require.Equal(t, 1, countURL(cold, "/v1/mcp_servers"))
		require.Equal(t, 4, countURL(cold, "/mcp-registry/"))
		require.Equal(t, 4, countURL(warm, "/mcp-registry/"))
	})

	t.Run("mcp-registry 四次的时刻跨约 2.2 秒", func(t *testing.T) {
		var delays []time.Duration
		for _, r := range cold {
			if strings.Contains(r.URL, "/mcp-registry/") {
				delays = append(delays, r.Delay)
			}
		}
		require.Equal(t, []time.Duration{
			215 * time.Millisecond, 1293 * time.Millisecond,
			1933 * time.Millisecond, 2436 * time.Millisecond,
		}, delays)
	})

	t.Run("profile 带 cache-control: no-cache", func(t *testing.T) {
		p := find(cold, "/api/oauth/profile")
		require.Equal(t, "no-cache", p.Headers["cache-control"])
		require.Equal(t, "application/json", p.Headers["content-type"])
	})

	t.Run("penguin / grove / settings / mcp-registry 无 content-type", func(t *testing.T) {
		for _, frag := range []string{
			"/api/claude_code_penguin_mode", "/api/claude_code_grove",
			"/api/oauth/account/settings", "/mcp-registry/",
		} {
			require.NotContains(t, find(cold, frag).Headers, "content-type", frag)
		}
	})

	t.Run("时序单调且末条是 mcp-registry #4", func(t *testing.T) {
		for i := 1; i < len(cold); i++ {
			require.GreaterOrEqual(t, cold[i].Delay, cold[i-1].Delay)
		}
		require.Contains(t, cold[len(cold)-1].URL, "/mcp-registry/")
		require.Equal(t, 2436*time.Millisecond, cold[len(cold)-1].Delay)
	})
}

// E2-L3（§4 L3）：新会话的引导批在首条推理之前**同步**发完。
//
// 09-26 sub 实发信封里推理比 profile 早 4 毫秒签名出门；真客户端是引导发完再推理。
func TestReclaudeLifecycleDriverBootstrapsBeforeInference(t *testing.T) {
	ctx := context.Background()

	t.Run("OnInference 返回时引导批已经发出", func(t *testing.T) {
		driver, forwarder, account := driverFixture(t)

		driver.OnInference(ctx, account, "http://proxy:8080", "claude-opus-5-5")

		// 不等：同步语义就是「返回即发完」。
		calls := forwarder.snapshot()
		require.GreaterOrEqual(t, len(calls), 10, "引导批必须在返回前发完")
		// 🔴 并发发送后顺序不保证(2026-09-27 从串行改并发,绕开串行累计超时的撤销根因),
		// 断言改为「引导批集合含 profile」而非「首条是 profile」。
		urls := make([]string, 0, len(calls))
		for _, c := range calls {
			urls = append(urls, c.url)
		}
		require.Condition(t, func() bool {
			for _, u := range urls {
				if strings.Contains(u, "/api/oauth/profile") {
					return true
				}
			}
			return false
		}, "引导批必须包含 profile")
	})

	t.Run("遥测仍异步,不阻塞推理", func(t *testing.T) {
		account := eventAccount(t)
		_, cipher := probeAccount(t)
		forwarder := &recordingLifecycleForwarder{}
		sender := NewReclaudeLifecycleSender(forwarder, cipher)
		sender.sleep = func(time.Duration) {}
		driver := NewReclaudeLifecycleDriver(sender)

		driver.OnInference(ctx, account, "http://proxy:8080", "claude-opus-5-5")

		for _, c := range forwarder.snapshot() {
			require.NotContains(t, c.url, "/api/event_logging/", "event_logging 不在同步批里")
		}
		// 引导 12 条 + event_logging 1 + Datadog 1 = 14。
		calls := waitForCalls(t, forwarder, 14)
		events := 0
		for _, c := range calls {
			if strings.Contains(c.url, "/api/event_logging/") {
				events++
			}
		}
		require.Equal(t, 1, events)
	})

	t.Run("冷/暖启动按上次活动时间判定", func(t *testing.T) {
		driver, forwarder, account := driverFixture(t)
		base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
		driver.now = func() time.Time { return base }

		driver.OnInference(ctx, account, "http://proxy:8080", "claude-opus-5-5")
		first := forwarder.snapshot()
		require.Equal(t, 1, countBootstrap(first), "首次必冷启动")

		driver.now = func() time.Time { return base.Add(ReclaudeSessionIdleGap) }
		driver.OnInference(ctx, account, "http://proxy:8080", "claude-opus-5-5")
		second := forwarder.snapshot()[len(first):]
		require.Equal(t, 0, countBootstrap(second), "30 分钟后再启动是暖启动")

		driver.now = func() time.Time { return base.Add(ReclaudeSessionIdleGap + ReclaudeColdStartGap) }
		driver.OnInference(ctx, account, "http://proxy:8080", "claude-opus-5-5")
		third := forwarder.snapshot()[len(first)+len(second):]
		require.Equal(t, 1, countBootstrap(third), "静默超过冷启动间隔后再冷启动")
	})

	t.Run("会话内的后续推理不再带 mcp_servers 复查", func(t *testing.T) {
		driver, forwarder, account := driverFixture(t)

		driver.OnInference(ctx, account, "http://proxy:8080", "claude-opus-5-5")
		n := len(forwarder.snapshot())
		driver.OnInference(ctx, account, "http://proxy:8080", "claude-opus-5-5")
		time.Sleep(50 * time.Millisecond)

		require.Len(t, forwarder.snapshot(), n, "09-26 真值里两次推理之间没有 mcp_servers")
	})
}

func countBootstrap(calls []recordedLifecycleCall) int {
	n := 0
	for _, c := range calls {
		if strings.Contains(c.url, "/api/claude_cli/bootstrap") {
			n++
		}
	}
	return n
}

func TestReclaudeSessionTrackerLastSeen(t *testing.T) {
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	tracker := NewReclaudeSessionTracker()

	_, seen := tracker.LastSeen(1)
	require.False(t, seen)

	tracker.ObserveAt(1, base)
	got, seen := tracker.LastSeen(1)
	require.True(t, seen)
	require.Equal(t, base, got)
}
