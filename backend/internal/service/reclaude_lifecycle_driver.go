package service

import (
	"context"
	"sync"
	"time"
)

// ReclaudeLifecycleDriver 决定每条推理该带哪些生命周期流量。
//
// 它是 D3 的入口：把「会话边界判定」「该发什么」「怎么发」三者接起来，
// 而转发器只需要调一次 OnInference。
type ReclaudeLifecycleDriver struct {
	tracker *ReclaudeSessionTracker
	sender  *ReclaudeLifecycleSender
	now     func() time.Time

	mu sync.Mutex
	// turns 记每个账号在**当前会话内**已经发过几次推理，
	// 用来决定要不要带 mcp 复查（真值里只有会话早期带）。
	turns map[int64]int
}

// NewReclaudeLifecycleDriver 构造驱动器。
func NewReclaudeLifecycleDriver(sender *ReclaudeLifecycleSender) *ReclaudeLifecycleDriver {
	return &ReclaudeLifecycleDriver{
		tracker: NewReclaudeSessionTracker(),
		sender:  sender,
		now:     time.Now,
		turns:   map[int64]int{},
	}
}

// OnInference 在每条推理请求出网前调用。
//
// 🔴 三道闸，缺一不可：
//  1. 合成请求自己不触发合成 —— 否则每条合成请求再生成一批，指数爆炸。
//  2. 只对 reclaude 账号生效。
//  3. 无代理时不发 —— 与心跳同一条红线：宁可不发，也不从机房 IP 出去。
func (d *ReclaudeLifecycleDriver) OnInference(ctx context.Context, account *Account, proxyURL string) {
	if d == nil || d.sender == nil || account == nil || !account.IsReclaude() {
		return
	}
	// 🔴 递归防线。
	if IsReclaudeSynthetic(ctx) {
		return
	}
	if proxyURL == "" {
		return
	}

	clientVersion := account.GetCredential(CredKeyReclaudeClientVersion)

	if d.tracker.ObserveAt(account.ID, d.now()) {
		d.resetTurns(account.ID)
		d.sender.SendAsync(account, proxyURL, BuildReclaudeBootstrapRequests(clientVersion))
		return
	}

	// 非新会话：只有会话早期的几次推理带 MCP 复查。
	if followups := BuildReclaudeInferenceFollowups(d.nextTurn(account.ID)); len(followups) > 0 {
		d.sender.SendAsync(account, proxyURL, followups)
	}
}

// resetTurns 把账号的会话内轮次归零（新会话开始）。
//
// 归零时设成 1：引导流量本身就对应会话的第一次推理，
// 不归零会让一个长期活跃的账号永远停在「轮次 > 2」，再也不发 MCP 复查。
func (d *ReclaudeLifecycleDriver) resetTurns(accountID int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.turns[accountID] = 1
}

func (d *ReclaudeLifecycleDriver) nextTurn(accountID int64) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.turns[accountID]++
	return d.turns[accountID]
}
