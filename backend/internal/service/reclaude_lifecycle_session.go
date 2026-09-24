package service

import (
	"sync"
	"time"
)

// ReclaudeSessionIdleGap 是「上一次推理之后隔多久，再来的请求算一次新启动」。
//
// 真客户端的启动引导（oauth/profile、mcp_servers、penguin_mode…）发生在
// **一次 Claude Code 进程启动**时，不是每条消息。我们看不到下游进程的生死，
// 只能用「静默多久」当代理指标。
//
// 取 30 分钟：短于它，一个人吃顿饭回来接着聊会被判成两次启动（一台机器一天
// 启动几十次，不合理）；长于它，一天只引导一次，又回到「从不启动」的形态。
// ⚠️ 这个值是工程取舍，**不是真值** —— 抓包只有单次会话，推不出分布。
const ReclaudeSessionIdleGap = 30 * time.Minute

// ReclaudeSessionTracker 判断某次推理是否属于一次「新启动的会话」。
//
// 🔴 按账号（= 设备）分别跟踪。一台设备就是一个人的一台机器，会话边界是
// 每台各自的事；全局共用一个时钟会让所有设备在同一秒集体「启动」。
type ReclaudeSessionTracker struct {
	mu       sync.Mutex
	lastSeen map[int64]time.Time
}

// NewReclaudeSessionTracker 构造跟踪器。
func NewReclaudeSessionTracker() *ReclaudeSessionTracker {
	return &ReclaudeSessionTracker{lastSeen: map[int64]time.Time{}}
}

// ObserveAt 记一次推理，返回它是否开启了一次新会话。
//
// 首次见到某账号一定算新会话：进程此前没在跑过，这与真客户端一致。
func (t *ReclaudeSessionTracker) ObserveAt(accountID int64, at time.Time) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	previous, seen := t.lastSeen[accountID]
	t.lastSeen[accountID] = at

	if !seen {
		return true
	}
	// 时钟回拨或乱序到达：不当成新会话。宁可漏一次引导，
	// 也不要因为两个并发请求的纳秒差就多发一整轮启动流量。
	if at.Before(previous) {
		return false
	}
	return at.Sub(previous) >= ReclaudeSessionIdleGap
}

// Forget 丢弃某账号的会话状态（账号被删或停用时调用，避免 map 无限增长）。
func (t *ReclaudeSessionTracker) Forget(accountID int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.lastSeen, accountID)
}
