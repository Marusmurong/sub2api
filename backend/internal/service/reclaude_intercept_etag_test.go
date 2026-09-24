package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// 🔴 /client/intercept-domains 必须带 If-None-Match。
//
// ✅ 真值抓包（reclaude-lab/sink/dump/*intercept-domains.txt）：
//
//	GET /client/intercept-domains HTTP/1.1
//	If-None-Match: "0:0"
//
// 真客户端把清单版本（intercept.Snapshot.Version，形如 "0:0"）持久化，
// 之后每次条件请求 —— 第一次拿 200，此后永远是 304。
//
// 我们此前从不设这个头，也不持久化版本 ⇒ 每 60 秒强制一次全量 200。
// 这是一条**每分钟重复、跨设备完全一致**的信号：一台"清单永远在变"的设备，
// 而真设备的清单一整天都是 304。
func TestReclaudeInterceptETag(t *testing.T) {
	t.Run("首次无 ETag 时不带该头", func(t *testing.T) {
		// 没有版本就不能凭空编一个 —— 编出来的值与服务端记录对不上，
		// 反而比不带更可疑。
		store := NewReclaudeInterceptETagStore()

		value, ok := store.Get(7)

		require.False(t, ok)
		require.Empty(t, value)
	})

	t.Run("记住响应里的 ETag，下次带上", func(t *testing.T) {
		store := NewReclaudeInterceptETagStore()
		store.Remember(7, `"0:0"`)

		value, ok := store.Get(7)

		require.True(t, ok)
		require.Equal(t, `"0:0"`, value)
	})

	t.Run("按账号隔离", func(t *testing.T) {
		// 串了会让 A 设备带着 B 设备的版本号请求。
		store := NewReclaudeInterceptETagStore()
		store.Remember(1, `"0:0"`)
		store.Remember(2, `"3:9"`)

		v1, _ := store.Get(1)
		v2, _ := store.Get(2)
		require.Equal(t, `"0:0"`, v1)
		require.Equal(t, `"3:9"`, v2)
	})

	t.Run("服务端下发新版本时更新", func(t *testing.T) {
		store := NewReclaudeInterceptETagStore()
		store.Remember(7, `"0:0"`)
		store.Remember(7, `"1:4"`)

		value, _ := store.Get(7)
		require.Equal(t, `"1:4"`, value)
	})

	t.Run("空 ETag 不覆盖已有值", func(t *testing.T) {
		// 304 响应通常不重复带 ETag；此时不能把已记住的版本清掉，
		// 否则下一次又会退回全量 200。
		store := NewReclaudeInterceptETagStore()
		store.Remember(7, `"0:0"`)
		store.Remember(7, "")

		value, ok := store.Get(7)
		require.True(t, ok)
		require.Equal(t, `"0:0"`, value)
	})

	t.Run("并发安全", func(t *testing.T) {
		store := NewReclaudeInterceptETagStore()
		done := make(chan struct{})
		for i := range 8 {
			go func(n int) {
				defer func() { done <- struct{}{} }()
				for range 50 {
					store.Remember(int64(n%3), `"0:0"`)
					store.Get(int64(n % 3))
				}
			}(i)
		}
		for range 8 {
			<-done
		}
	})
}

// 接线：probe 发 intercept-domains 时带上已知 ETag，并从响应里记下新值。
func TestProbeSendsInterceptETag(t *testing.T) {
	account, cipher := probeAccount(t)

	t.Run("已知版本时带 If-None-Match", func(t *testing.T) {
		upstream := &capturingUpstream{response: &http.Response{StatusCode: 304, Header: http.Header{}}}
		probe := NewReclaudeGatewayProbe(upstream, cipher)
		probe.RememberInterceptETag(account.ID, `"0:0"`)

		_, err := probe.ProbeReclaudeEndpoint(context.Background(), account, ReclaudeInterceptDomainsPath)

		require.NoError(t, err)
		require.Equal(t, `"0:0"`, upstream.gotRequest.Header.Get("If-None-Match"))
	})

	t.Run("首次没有版本时不带该头", func(t *testing.T) {
		upstream := &capturingUpstream{response: &http.Response{StatusCode: 200, Header: http.Header{}}}

		_, err := NewReclaudeGatewayProbe(upstream, cipher).
			ProbeReclaudeEndpoint(context.Background(), account, ReclaudeInterceptDomainsPath)

		require.NoError(t, err)
		require.Empty(t, upstream.gotRequest.Header.Get("If-None-Match"))
	})

	t.Run("只对 intercept-domains 带，account 不带", func(t *testing.T) {
		// ETag 是清单端点专有的；给 /client/account 带上是另一种"抄错了"。
		upstream := &capturingUpstream{response: &http.Response{StatusCode: 200, Header: http.Header{}}}
		probe := NewReclaudeGatewayProbe(upstream, cipher)
		probe.RememberInterceptETag(account.ID, `"0:0"`)

		_, err := probe.ProbeReclaudeEndpoint(context.Background(), account, ReclaudeClientAccountPath)

		require.NoError(t, err)
		require.Empty(t, upstream.gotRequest.Header.Get("If-None-Match"))
	})
}
