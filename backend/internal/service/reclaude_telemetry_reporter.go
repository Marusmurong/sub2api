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

	// passthroughLoader 从同机 passthrough.json 读真值 host;nil 或读不到 → 空。
	// 注入而非直接读文件:便于测试,且路径按账号(extra)或全局回落决定。
	passthroughLoader func(account *Account) []ReclaudeTelemetryPassthroughHost

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

// SetPassthroughLoader 注入 passthrough_hosts 来源(同机 passthrough.json)。
//
// 未设置(nil)时上报 passthrough 恒空 —— 旧行为,非同机部署安全。
func (r *ReclaudeTelemetryReporter) SetPassthroughLoader(
	loader func(account *Account) []ReclaudeTelemetryPassthroughHost,
) {
	if r == nil {
		return
	}
	r.passthroughLoader = loader
}

// ReportAt 在窗口边界到达、且本窗口有内容时上报一次。
//
// 🔴 三空跳过(2026-09-29 反汇编实证):真客户端 flushTelemetry 在 rollups +
// passthrough_hosts + overflow 三者全空时**跳过发送**。sub 对齐此门槛,不发空体
// ——「周期性发一个空 telemetry」本身是可判别特征。
//
// 🔴 门槛必须在 shouldReport **之前**:先取数据判空,无内容则**不消费窗口标记**,
// 等本窗口后续 tick(passthrough 可能刚被 reclaude 观测到)再试;有内容才 shouldReport
// 消费窗口并发送。反过来(先 shouldReport)会在窗口首个 tick 恰好空时消费掉标记,
// 导致本窗口后来有内容也不再发 —— 那正是"有推理零遥测"要消除的破绽。
func (r *ReclaudeTelemetryReporter) ReportAt(ctx context.Context, account *Account, at time.Time) {
	if r == nil || r.poster == nil || account == nil || !account.IsReclaude() {
		return
	}
	// 模拟关机期间整段静默：只停调度不停遥测，等于关机没生效。
	if _, offline := ReclaudeOfflineUntil(account, at); offline {
		return
	}

	// passthrough 可反复读不消费(读同机文件);rollup 的 drain 是消费性的,放后面。
	var passthrough []ReclaudeTelemetryPassthroughHost
	if r.passthroughLoader != nil {
		passthrough = r.passthroughLoader(account)
	}
	// 是否有待发 rollup:非消费性预探(PeekHasClosedWindows),避免这里 drain 后
	// 若 shouldReport 挡下就把 rollup 丢了。
	hasRollup := r.collector.PeekHasClosedWindows(account.ID, at)

	// 三空跳过(shouldReport 之前,不消费窗口标记):无 rollup 且无 passthrough → 不发,
	// 等本窗口后续 tick(passthrough 可能刚被观测到)再试。
	if !hasRollup && len(passthrough) == 0 {
		return
	}
	// 有内容:消费窗口标记(同窗口不重复发)。
	if !r.shouldReport(account.ID, at) {
		return
	}
	// 确定要发,此刻才消费 rollup(drain)。
	rollups := r.collector.DrainClosedWindows(account.ID, at)

	payload, err := BuildReclaudeTelemetryPayload(rollups, passthrough)
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
