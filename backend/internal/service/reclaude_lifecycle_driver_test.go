//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func driverFixture(t *testing.T) (*ReclaudeLifecycleDriver, *recordingLifecycleForwarder, *Account) {
	t.Helper()
	account, cipher := probeAccount(t)
	forwarder := &recordingLifecycleForwarder{}
	sender := NewReclaudeLifecycleSender(forwarder, cipher)
	sender.sleep = func(time.Duration) {}
	return NewReclaudeLifecycleDriver(sender), forwarder, account
}

// 驱动器发出的是异步 goroutine，等它跑完再断言。
func waitForCalls(t *testing.T, forwarder *recordingLifecycleForwarder, want int) []recordedLifecycleCall {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls := forwarder.snapshot(); len(calls) >= want {
			return calls
		}
		time.Sleep(5 * time.Millisecond)
	}
	return forwarder.snapshot()
}

func TestReclaudeLifecycleDriver(t *testing.T) {
	ctx := context.Background()

	t.Run("首次推理触发完整引导", func(t *testing.T) {
		driver, forwarder, account := driverFixture(t)

		driver.OnInference(ctx, account, "http://proxy:8080", "claude-opus-5-5")

		calls := waitForCalls(t, forwarder, 8)
		require.Len(t, calls, 8)
	})

	t.Run("同一会话内不重复引导", func(t *testing.T) {
		// 每条消息都重发一轮引导 = 一台每分钟启动一次的机器，比不发更假。
		driver, forwarder, account := driverFixture(t)

		driver.OnInference(ctx, account, "http://proxy:8080", "claude-opus-5-5")
		waitForCalls(t, forwarder, 8)
		driver.OnInference(ctx, account, "http://proxy:8080", "claude-opus-5-5")
		calls := waitForCalls(t, forwarder, 9)

		bootstraps := 0
		for _, c := range calls {
			if strings.Contains(c.url, "/api/oauth/profile") {
				bootstraps++
			}
		}
		require.Equal(t, 1, bootstraps)
	})

	t.Run("静默超过间隔后重新引导", func(t *testing.T) {
		driver, forwarder, account := driverFixture(t)
		base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
		driver.now = func() time.Time { return base }

		driver.OnInference(ctx, account, "http://proxy:8080", "claude-opus-5-5")
		waitForCalls(t, forwarder, 8)
		driver.now = func() time.Time { return base.Add(ReclaudeSessionIdleGap) }
		driver.OnInference(ctx, account, "http://proxy:8080", "claude-opus-5-5")

		calls := waitForCalls(t, forwarder, 16)
		bootstraps := 0
		for _, c := range calls {
			if strings.Contains(c.url, "/api/oauth/profile") {
				bootstraps++
			}
		}
		require.Equal(t, 2, bootstraps)
	})

	// 🔴 这是整个 D3 最危险的失败模式：合成请求再触发合成 = 指数爆炸。
	t.Run("合成请求不再触发合成", func(t *testing.T) {
		driver, forwarder, account := driverFixture(t)

		driver.OnInference(WithReclaudeSynthetic(ctx), account, "http://proxy:8080", "claude-opus-5-5")
		time.Sleep(50 * time.Millisecond)

		require.Empty(t, forwarder.snapshot())
	})

	t.Run("端到端不会递归爆炸", func(t *testing.T) {
		// 把驱动器接回一个「会回调驱动器」的转发器 —— 复刻生产接线，
		// 若标记失效，这个用例会一直涨到超时。
		account, cipher := probeAccount(t)
		var calls atomic.Int64
		var driver *ReclaudeLifecycleDriver
		forwarder := &callbackForwarder{onDo: func(req *http.Request) {
			calls.Add(1)
			driver.OnInference(req.Context(), account, "http://proxy:8080", "claude-opus-5-5")
		}}
		sender := NewReclaudeLifecycleSender(forwarder, cipher)
		sender.sleep = func(time.Duration) {}
		driver = NewReclaudeLifecycleDriver(sender)

		driver.OnInference(context.Background(), account, "http://proxy:8080", "claude-opus-5-5")
		time.Sleep(300 * time.Millisecond)

		require.EqualValues(t, 8, calls.Load(), "只应发一轮引导，不该有二次放大")
	})

	t.Run("无代理时不发 —— 不从机房 IP 出去", func(t *testing.T) {
		driver, forwarder, account := driverFixture(t)

		driver.OnInference(ctx, account, "", "claude-opus-5-5")
		time.Sleep(50 * time.Millisecond)

		require.Empty(t, forwarder.snapshot())
	})

	t.Run("非 reclaude 账号不发", func(t *testing.T) {
		driver, forwarder, _ := driverFixture(t)

		driver.OnInference(ctx, &Account{ID: 9, Type: AccountTypeOAuth}, "http://proxy:8080", "claude-opus-5-5")
		time.Sleep(50 * time.Millisecond)

		require.Empty(t, forwarder.snapshot())
	})

	t.Run("nil 依赖不 panic", func(t *testing.T) {
		require.NotPanics(t, func() {
			var nilDriver *ReclaudeLifecycleDriver
			nilDriver.OnInference(ctx, nil, "", "claude-opus-5-5")
			NewReclaudeLifecycleDriver(nil).OnInference(ctx, &Account{ID: 1}, "p", "claude-opus-5-5")
		})
	})
}

type callbackForwarder struct {
	onDo func(*http.Request)
}

func (f *callbackForwarder) Do(inner *http.Request, _ *Account, _ string) (*http.Response, error) {
	f.onDo(inner)
	return &http.Response{StatusCode: 200}, nil
}

// event_logging 必须真的跟着推理发出去（D3 剩余部分）。
func TestReclaudeLifecycleDriverEmitsEvents(t *testing.T) {
	ctx := context.Background()

	eventDriverFixture := func(t *testing.T) (*ReclaudeLifecycleDriver, *recordingLifecycleForwarder, *Account) {
		t.Helper()
		account := eventAccount(t)
		_, cipher := probeAccount(t)
		forwarder := &recordingLifecycleForwarder{}
		sender := NewReclaudeLifecycleSender(forwarder, cipher)
		sender.sleep = func(time.Duration) {}
		return NewReclaudeLifecycleDriver(sender), forwarder, account
	}

	t.Run("推理后发出 event_logging", func(t *testing.T) {
		driver, forwarder, account := eventDriverFixture(t)

		driver.OnInference(ctx, account, "http://proxy:8080", "claude-opus-5-5")

		calls := waitForCalls(t, forwarder, 9)
		events := 0
		for _, c := range calls {
			if strings.Contains(c.url, "/api/event_logging/v2/batch") {
				events++
				require.Equal(t, http.MethodPost, c.method)
				// ✅ 真值：这条路径的 UA 是 claude-code，不是 claude-cli。
				require.Contains(t, c.headers["user-agent"][0], "claude-code/")
				require.Equal(t, "claude-code", c.headers["x-service-name"][0])
			}
		}
		require.Equal(t, 1, events)
	})

	t.Run("缺真值身份时整批不发事件，其余流量照常", func(t *testing.T) {
		// 🔴 编一个 UUID 比沉默更危险 —— 归属关系对端一查就知道。
		driver, forwarder, account := eventDriverFixture(t)
		delete(account.Credentials, CredKeyReclaudeOrganizationUUID)

		driver.OnInference(ctx, account, "http://proxy:8080", "claude-opus-5-5")

		calls := waitForCalls(t, forwarder, 8)
		require.Len(t, calls, 8, "引导流量不受影响")
		for _, c := range calls {
			require.NotContains(t, c.url, "/api/event_logging/")
		}
	})

	t.Run("同一会话的事件共享 session_id，跨会话变化", func(t *testing.T) {
		driver, forwarder, account := eventDriverFixture(t)
		base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
		driver.now = func() time.Time { return base }

		driver.OnInference(ctx, account, "http://proxy:8080", "claude-opus-5-5")
		waitForCalls(t, forwarder, 9)
		driver.OnInference(ctx, account, "http://proxy:8080", "claude-opus-5-5")
		waitForCalls(t, forwarder, 11)
		driver.now = func() time.Time { return base.Add(ReclaudeSessionIdleGap) }
		driver.OnInference(ctx, account, "http://proxy:8080", "claude-opus-5-5")

		sessions := collectEventSessionIDs(t, waitForCalls(t, forwarder, 20))
		require.GreaterOrEqual(t, len(sessions), 2)
		require.Len(t, uniqueStrings(sessions), 2,
			"同会话内 session_id 相同，跨会话必须变化")
	})
}

func collectEventSessionIDs(t *testing.T, calls []recordedLifecycleCall) []string {
	t.Helper()
	var ids []string
	for _, c := range calls {
		if !strings.Contains(c.url, "/api/event_logging/") {
			continue
		}
		var batch ReclaudeEventBatch
		require.NoError(t, json.Unmarshal(c.body, &batch))
		require.NotEmpty(t, batch.Events)
		ids = append(ids, batch.Events[0].EventData.SessionID)
	}
	return ids
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
