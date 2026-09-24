package service

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// ReclaudeTelemetryPoster 发一次遥测上报。
type ReclaudeTelemetryPoster interface {
	PostReclaudeTelemetry(ctx context.Context, account *Account, payload []byte) (*http.Response, error)
}

// ReclaudeTelemetryReporter 把采集器里已关闭的窗口上报出去。
//
// 🔴 **不挂自己的 ticker**，由心跳节拍器每分钟调一次 ReportAt，内部按
// 300 秒窗口自己决定发不发。理由是「同一个 leader」：
// 心跳与遥测各自选主的话，两者可能落在不同副本上 ——
// 同一台「设备」的控制面请求从两个出口发出，正是我们要消除的特征。
type ReclaudeTelemetryReporter struct {
	poster    ReclaudeTelemetryPoster
	collector *ReclaudeTelemetryCollector

	mu sync.Mutex
	// lastReported 记每个账号上次上报覆盖到的窗口起点，避免同一窗口重复发。
	lastReported map[int64]int64
	// started 标记本进程是否已经为该账号发过首报。
	started map[int64]bool
}

// NewReclaudeTelemetryReporter 构造上报器。
func NewReclaudeTelemetryReporter(
	poster ReclaudeTelemetryPoster, collector *ReclaudeTelemetryCollector,
) *ReclaudeTelemetryReporter {
	return &ReclaudeTelemetryReporter{
		poster:       poster,
		collector:    collector,
		lastReported: map[int64]int64{},
		started:      map[int64]bool{},
	}
}

// ReportAt 在窗口边界到达时上报一次。
//
// 🔴 **启动后第一次必发**（即使没有任何数据）。真客户端的
// telemetryUploadLoop 是「先 flush 一次，再进 ticker」——
// 一台刚上线就沉默五分钟的设备，与真值的启动时序对不上。
func (r *ReclaudeTelemetryReporter) ReportAt(ctx context.Context, account *Account, at time.Time) {
	if r == nil || r.poster == nil || account == nil || !account.IsReclaude() {
		return
	}
	// 模拟关机期间整段静默：只停调度不停遥测，等于关机没生效。
	if _, offline := ReclaudeOfflineUntil(account, at); offline {
		return
	}
	if !r.shouldReport(account.ID, at) {
		return
	}

	rollups := r.collector.DrainClosedWindows(account.ID, at)
	payload, err := BuildReclaudeTelemetryPayload(rollups)
	if err != nil {
		logger.LegacyPrintf("service.reclaude",
			"failed to build telemetry payload for account %d: %v", account.ID, err)
		return
	}

	resp, err := r.poster.PostReclaudeTelemetry(ctx, account, payload)
	if err != nil {
		// 上报失败不回滚 lastReported：重试会把同一个 window_start_ms 再发一遍，
		// 而对端是按这个键记账的。宁可丢一个窗口的统计。
		logger.LegacyPrintf("service.reclaude",
			"telemetry upload failed for account %d (%d rollup(s)): %v",
			account.ID, len(rollups), err)
		return
	}
	if resp != nil && resp.Body != nil {
		// 必须读完并关闭，否则连接不复用，反而制造额外 TCP 会话。
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

// shouldReport 判断当前时刻是否该发，并记下已覆盖的窗口。
func (r *ReclaudeTelemetryReporter) shouldReport(accountID int64, at time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	currentWindow := ReclaudeTelemetryWindowStartMs(at)
	if !r.started[accountID] {
		r.started[accountID] = true
		r.lastReported[accountID] = currentWindow
		return true
	}
	if r.lastReported[accountID] >= currentWindow {
		return false
	}
	r.lastReported[accountID] = currentWindow
	return true
}
