package service

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// ReclaudeAccountLister 列出所有需要维持存在感的 reclaude 账号。
type ReclaudeAccountLister interface {
	ListReclaudeAccounts(ctx context.Context) ([]*Account, error)
}

// ReclaudeScheduler 是 reclaude 的后台节拍器：每分钟一次，
// 为每台设备维持「客户端存在感」，并按各自的作息模拟关机。
//
// 🔴 必须走 leader 锁。sub2api 支持多副本；每个进程各跑一份 ticker 的话，
// 3 副本 ⇒ 每台「设备」每分钟发 6 个请求、来自 3 个 TCP 会话 ——
// 在对端看来是一台机器同时开着 3 个 daemon，比不心跳还糟。
type ReclaudeScheduler struct {
	lister     ReclaudeAccountLister
	store      ReclaudeAccountStore
	heartbeat  *ReclaudeHeartbeatRunner
	telemetry  *ReclaudeTelemetryReporter
	lockCache  LeaderLockCache
	instanceID string

	running atomic.Int32

	mu     sync.Mutex
	cancel context.CancelFunc
}

// NewReclaudeScheduler 构造节拍器。
func NewReclaudeScheduler(
	lister ReclaudeAccountLister,
	store ReclaudeAccountStore,
	heartbeat *ReclaudeHeartbeatRunner,
	telemetry *ReclaudeTelemetryReporter,
	lockCache LeaderLockCache,
	instanceID string,
) *ReclaudeScheduler {
	return &ReclaudeScheduler{
		lister:     lister,
		store:      store,
		heartbeat:  heartbeat,
		telemetry:  telemetry,
		lockCache:  lockCache,
		instanceID: instanceID,
	}
}

// Start 起一个按 ReclaudeHeartbeatInterval 节拍的后台循环，直到 ctx 结束或 Stop。
func (s *ReclaudeScheduler) Start(ctx context.Context) {
	if s == nil {
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	// 重复 Start 不能泄漏上一轮循环：先停旧的再起新的。
	if s.cancel != nil {
		s.cancel()
	}
	s.cancel = cancel
	s.mu.Unlock()

	ticker := time.NewTicker(ReclaudeHeartbeatInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.RunOnce(ctx)
			}
		}
	}()
}

// Stop 停掉后台循环。幂等 —— 进程退出路径可能调用多次。
func (s *ReclaudeScheduler) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

// RunOnce 按当前时刻跑一轮。
func (s *ReclaudeScheduler) RunOnce(ctx context.Context) {
	s.RunOnceAt(ctx, time.Now())
}

// RunOnceAt 按指定时刻跑一轮。
func (s *ReclaudeScheduler) RunOnceAt(ctx context.Context, at time.Time) {
	if s == nil || s.lister == nil {
		return
	}
	// 同进程内也要防重入：上一轮还没跑完时再起一轮，同样会翻倍请求。
	if !s.running.CompareAndSwap(0, 1) {
		return
	}
	defer s.running.Store(0)

	release, ok := tryAcquireSingletonLeaderLock(
		ctx, s.lockCache, nil, ReclaudeHeartbeatLeaderLockKey, s.instanceID, ReclaudeHeartbeatLeaderLockTTL)
	if !ok {
		return
	}
	defer release()

	accounts, err := s.lister.ListReclaudeAccounts(ctx)
	if err != nil {
		logger.LegacyPrintf("service.reclaude", "failed to list reclaude accounts: %v", err)
		return
	}
	// 🔴 成功路径也要留痕：此前这个循环完全静默 —— 生产上查「心跳有没有在跑」
	// 只能看到 0 条日志，而 0 条既可能是「没账号」，也可能是「leader lock 没拿到」
	// 或「注入缺件」。三种原因的处置完全不同。
	logger.LegacyPrintf("service.reclaude", "heartbeat tick: %d account(s)", len(accounts))

	for _, account := range accounts {
		s.tickAccount(ctx, account, at)
	}
}

// tickAccount 处理一台设备：要么让它「合盖」，要么维持存在感。
//
// 一个账号出错不影响其它账号 —— 否则一台设备的网络问题会让整批设备一起停跳，
// 而「一批设备同时静默」正是最该避免的批量特征。
func (s *ReclaudeScheduler) tickAccount(ctx context.Context, account *Account, at time.Time) {
	if account == nil || !account.IsReclaude() {
		return
	}

	if until, offline := ReclaudeOfflineUntil(account, at); offline {
		// 模拟关机必须**同时**停心跳与停调度：只停一样都等于没关机 ——
		// 停调度但还在心跳，设备页面的「最近使用」照常刷新。
		if s.store != nil {
			if err := s.store.SetTempUnschedulable(ctx, account.ID, until, ReclaudeOfflineReason); err != nil {
				logger.LegacyPrintf("service.reclaude",
					"failed to mark account %d offline: %v", account.ID, err)
			}
		}
		return
	}

	if s.heartbeat == nil {
		return
	}
	if err := s.heartbeat.BeatAt(ctx, account, at); err != nil {
		logger.LegacyPrintf("service.reclaude", "heartbeat failed: %v", err)
	}

	// 遥测搭在同一次 tick 上（上报器内部按 300 秒窗口自己决定发不发）。
	// 🔴 刻意不给它单独的 ticker 与 leader 锁：两次独立选主可能落在不同副本，
	// 同一台「设备」的控制面请求就会从两个出口发出去。
	// 心跳失败也继续上报 —— 遥测的价值恰恰在于「有推理就有遥测」这条对账关系，
	// 因一次心跳失败而跳过，等于在对端那里留下一个无法解释的空窗。
	s.telemetry.ReportAt(ctx, account, at)
}
