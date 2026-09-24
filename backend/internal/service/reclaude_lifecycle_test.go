//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type recordedLifecycleCall struct {
	method  string
	url     string
	headers http.Header
	body    []byte
}

type recordingLifecycleForwarder struct {
	mu    sync.Mutex
	calls []recordedLifecycleCall
	err   error
}

func (f *recordingLifecycleForwarder) Do(
	inner *http.Request, _ *Account, _ string,
) (*http.Response, error) {
	var body []byte
	if inner.Body != nil {
		body, _ = io.ReadAll(inner.Body)
	}
	f.mu.Lock()
	f.calls = append(f.calls, recordedLifecycleCall{
		method: inner.Method, url: inner.URL.String(),
		headers: inner.Header.Clone(), body: body,
	})
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(`{}`)),
	}, nil
}

func (f *recordingLifecycleForwarder) snapshot() []recordedLifecycleCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedLifecycleCall(nil), f.calls...)
}

// 引导流量的形状必须逐字对齐抓包。
func TestBuildReclaudeBootstrapRequests(t *testing.T) {
	requests := BuildReclaudeBootstrapRequests("2.1.280")

	t.Run("端点与真值一致，且不含 eval/sdk", func(t *testing.T) {
		urls := make([]string, 0, len(requests))
		for _, r := range requests {
			urls = append(urls, r.URL)
		}
		require.Len(t, requests, 8)
		for _, u := range urls {
			// 🔴 eval/sdk-… 路径里带我们编不出来的 SDK 实例 ID，伪造比缺席更危险。
			require.NotContains(t, u, "/api/eval/")
		}
		require.Contains(t, urls, "https://api.anthropic.com/api/oauth/profile")
		require.Contains(t, urls, "https://api.anthropic.com/api/claude_code_penguin_mode")
		require.Contains(t, urls, "https://api.anthropic.com/api/claude_code_grove")
	})

	t.Run("mcp_servers 保留真值的四次重复", func(t *testing.T) {
		// 抹平成一次反而与真值不符 —— Claude Code 启动时确实反复拉它。
		count := 0
		for _, r := range requests {
			if strings.Contains(r.URL, "/v1/mcp_servers") {
				count++
			}
		}
		require.Equal(t, 4, count)
	})

	t.Run("UA 是异构的，不是一种", func(t *testing.T) {
		// 🔴 D13：真值四种 UA，统一成一种按 UA group by 一眼可见。
		agents := map[string]bool{}
		for _, r := range requests {
			agents[r.Headers["user-agent"]] = true
		}
		require.GreaterOrEqual(t, len(agents), 2)
		require.True(t, agents["axios/1.15.2"])
		require.True(t, agents["claude-cli/2.1.280 (external, sdk-cli)"])
	})

	t.Run("mcp-registry 不带 Authorization", func(t *testing.T) {
		// 🔴 真值里这条没有 auth 头；补上是「照着别的请求抄」的直接特征。
		for _, r := range requests {
			if strings.Contains(r.URL, "/mcp-registry/") {
				require.False(t, r.NeedsAuthorization)
				return
			}
		}
		t.Fatal("没有找到 mcp-registry 请求")
	})

	t.Run("时序单调递增且覆盖真值跨度", func(t *testing.T) {
		for i := 1; i < len(requests); i++ {
			require.GreaterOrEqual(t, requests[i].Delay, requests[i-1].Delay)
		}
		require.Equal(t, 3162*time.Millisecond, requests[len(requests)-1].Delay)
	})

	t.Run("每条都带三个固定头", func(t *testing.T) {
		for _, r := range requests {
			require.Equal(t, "application/json, text/plain, */*", r.Headers["accept"])
			require.Equal(t, "gzip, compress, deflate, br", r.Headers["accept-encoding"])
			require.Equal(t, "close", r.Headers["connection"])
			require.Equal(t, "api.anthropic.com", r.Headers["host"])
		}
	})
}

func TestReclaudeClientVersionInUA(t *testing.T) {
	t.Run("按账号版本拼，不写死", func(t *testing.T) {
		// 写死会让整个设备群停在同一个版本上（D9）。
		requests := BuildReclaudeBootstrapRequests("2.1.300")
		found := false
		for _, r := range requests {
			if strings.Contains(r.Headers["user-agent"], "claude-cli/") {
				require.Contains(t, r.Headers["user-agent"], "2.1.300")
				found = true
			}
		}
		require.True(t, found)
	})

	t.Run("去掉表单可能带的 v 前缀", func(t *testing.T) {
		require.Equal(t, "claude-cli/1.4.0 (external, sdk-cli)", reclaudeInnerUACLI("v1.4.0"))
	})

	t.Run("缺版本时用兜底值而不是空串", func(t *testing.T) {
		require.Equal(t, "claude-code/2.1.280", reclaudeInnerUAClaudeCode(""))
	})
}

func TestBuildReclaudeInferenceFollowups(t *testing.T) {
	t.Run("会话早期的推理带 mcp 复查", func(t *testing.T) {
		require.Len(t, BuildReclaudeInferenceFollowups(1), 1)
		require.Len(t, BuildReclaudeInferenceFollowups(2), 1)
	})

	t.Run("后续推理不再带", func(t *testing.T) {
		// 真值里复查集中在会话早期；每条推理都带会凭空放大请求量。
		require.Empty(t, BuildReclaudeInferenceFollowups(3))
		require.Empty(t, BuildReclaudeInferenceFollowups(50))
	})

	t.Run("非法轮次不 panic", func(t *testing.T) {
		require.Empty(t, BuildReclaudeInferenceFollowups(0))
		require.Empty(t, BuildReclaudeInferenceFollowups(-1))
	})
}

func lifecycleSenderFixture(t *testing.T) (*ReclaudeLifecycleSender, *recordingLifecycleForwarder, *Account) {
	t.Helper()
	account, cipher := probeAccount(t)
	forwarder := &recordingLifecycleForwarder{}
	sender := NewReclaudeLifecycleSender(forwarder, cipher)
	// 测试里不真的等三秒。
	sender.sleep = func(time.Duration) {}
	return sender, forwarder, account
}

func TestReclaudeLifecycleSender(t *testing.T) {
	t.Run("按顺序发完整批", func(t *testing.T) {
		sender, forwarder, account := lifecycleSenderFixture(t)
		requests := BuildReclaudeBootstrapRequests("2.1.280")

		sender.send(account, "http://proxy:8080", requests)

		calls := forwarder.snapshot()
		require.Len(t, calls, len(requests))
		for i := range requests {
			require.Equal(t, requests[i].URL, calls[i].url)
		}
	})

	t.Run("Authorization 按 spec 注入", func(t *testing.T) {
		sender, forwarder, account := lifecycleSenderFixture(t)

		sender.send(account, "", BuildReclaudeBootstrapRequests("2.1.280"))

		for _, call := range forwarder.snapshot() {
			// 🔴 用 map 直读而不是 Header.Get：setHeaderRaw 刻意存**小写**键
			// （装箱时 headers 统一转小写），Get 走 canonical 查找会取不到。
			auth := ""
			if values := call.headers["authorization"]; len(values) > 0 {
				auth = values[0]
			}
			if strings.Contains(call.url, "/mcp-registry/") {
				require.Empty(t, auth, "mcp-registry 真值不带 authorization")
				continue
			}
			require.True(t, strings.HasPrefix(auth, "Bearer sk-rec-"), "url=%s", call.url)
		}
	})

	t.Run("单条失败不影响后续", func(t *testing.T) {
		// 合成流量是旁路，一条失败不能让整批停摆。
		sender, forwarder, account := lifecycleSenderFixture(t)
		forwarder.err = io.ErrUnexpectedEOF

		require.NotPanics(t, func() {
			sender.send(account, "", BuildReclaudeBootstrapRequests("2.1.280"))
		})
		require.Len(t, forwarder.snapshot(), 8)
	})

	t.Run("依赖缺席时静默跳过", func(t *testing.T) {
		require.NotPanics(t, func() {
			var nilSender *ReclaudeLifecycleSender
			nilSender.SendAsync(nil, "", nil)
			NewReclaudeLifecycleSender(nil, nil).SendAsync(&Account{ID: 1}, "", nil)
		})
	})

	t.Run("空批次不起 goroutine", func(t *testing.T) {
		sender, forwarder, account := lifecycleSenderFixture(t)

		sender.SendAsync(account, "", nil)
		time.Sleep(20 * time.Millisecond)

		require.Empty(t, forwarder.snapshot())
	})
}
