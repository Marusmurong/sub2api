//go:build unit

package service

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReclaudeSessionTracker(t *testing.T) {
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)

	t.Run("首次见到即新会话", func(t *testing.T) {
		require.True(t, NewReclaudeSessionTracker().ObserveAt(1, base))
	})

	t.Run("连续对话不算新会话", func(t *testing.T) {
		tracker := NewReclaudeSessionTracker()
		tracker.ObserveAt(1, base)

		require.False(t, tracker.ObserveAt(1, base.Add(time.Minute)))
		require.False(t, tracker.ObserveAt(1, base.Add(5*time.Minute)))
	})

	t.Run("静默超过间隔算新会话", func(t *testing.T) {
		tracker := NewReclaudeSessionTracker()
		tracker.ObserveAt(1, base)

		require.True(t, tracker.ObserveAt(1, base.Add(ReclaudeSessionIdleGap)))
	})

	t.Run("每台设备各自算边界", func(t *testing.T) {
		// 全局共用时钟会让所有设备在同一秒集体「启动」。
		tracker := NewReclaudeSessionTracker()
		tracker.ObserveAt(1, base)

		require.True(t, tracker.ObserveAt(2, base.Add(time.Minute)),
			"账号 2 是第一次出现，不该被账号 1 的活动掩盖")
		require.False(t, tracker.ObserveAt(1, base.Add(2*time.Minute)))
	})

	t.Run("时钟回拨不触发新会话", func(t *testing.T) {
		tracker := NewReclaudeSessionTracker()
		tracker.ObserveAt(1, base)

		require.False(t, tracker.ObserveAt(1, base.Add(-time.Hour)))
	})

	t.Run("Forget 之后重新算新会话", func(t *testing.T) {
		tracker := NewReclaudeSessionTracker()
		tracker.ObserveAt(1, base)
		tracker.Forget(1)

		require.True(t, tracker.ObserveAt(1, base.Add(time.Minute)))
	})

	t.Run("并发观测只产出一次新会话", func(t *testing.T) {
		// 🔴 十个并发请求同时到达绝不能触发十轮启动引导。
		tracker := NewReclaudeSessionTracker()
		var mu sync.Mutex
		starts := 0
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if tracker.ObserveAt(1, base) {
					mu.Lock()
					starts++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()

		require.Equal(t, 1, starts)
	})

	t.Run("nil 跟踪器不 panic", func(t *testing.T) {
		var tracker *ReclaudeSessionTracker
		require.NotPanics(t, func() {
			require.False(t, tracker.ObserveAt(1, base))
			tracker.Forget(1)
		})
	})
}
