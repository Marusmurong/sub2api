package service

import (
	"encoding/json"
	"sync"
	"time"
)

// ReclaudeTelemetryStats 是一个上报周期内的用量汇总。
type ReclaudeTelemetryStats struct {
	SuccessCount int
	FailureCount int
	Latencies    []time.Duration
	BytesIn      int64
	BytesOut     int64
	// Edge 是这批请求实际发往的网关主机名，与信封 meta 的 edge 同源。
	Edge string
}

// reclaudeLatencyBucketCount 是时延直方图的档数。
//
// 18 档来自逆向报告 §12（TTFT/时延分桶各 18）。档数不对是结构性差异 ——
// 对端按固定长度数组解析，多一档少一档都说明发的不是同一个客户端。
const reclaudeLatencyBucketCount = 18

// reclaudeLatencyBucketUpperMs 是各档的上界（毫秒），最后一档是无上界溢出桶。
//
// 取指数级递增：真实推理时延跨度从百毫秒到分钟级，线性分桶会让 90% 的样本
// 挤在头两档里。
var reclaudeLatencyBucketUpperMs = [reclaudeLatencyBucketCount]int64{
	50, 100, 200, 300, 500, 750, 1000, 1500, 2000,
	3000, 5000, 7500, 10000, 15000, 20000, 30000, 60000, 0, // 0 = 无上界
}

// reclaudeTelemetryRollup 对应 cli.telemetryRollupPayload。
type reclaudeTelemetryRollup struct {
	Success        int    `json:"success"`
	Failure        int    `json:"failure"`
	BytesIn        int64  `json:"bytes_in"`
	BytesOut       int64  `json:"bytes_out"`
	Edge           string `json:"edge"`
	LatencyBuckets []int  `json:"latency_buckets"`
	TTFTBuckets    []int  `json:"ttft_buckets"`
}

// reclaudeTelemetryRequest 对应 cli.telemetryUploadRequestWithLeak（逆向报告 §12）。
//
// 三个字段一个不能少：对端按这个结构解析，缺字段与字段为空是两回事。
type reclaudeTelemetryRequest struct {
	Rollups []reclaudeTelemetryRollup `json:"rollups"`
	// 🔴 我们不做 MITM，没有 passthrough 主机，但必须是 [] 而不是 null：
	// null 在对端看来是"字段缺失"，空数组才是"没有数据"。
	PassthroughHosts    []string `json:"passthrough_hosts"`
	PassthroughOverflow int64    `json:"passthrough_overflow"`
}

// BuildReclaudeTelemetryPayload 把一个周期的用量汇总编成上报体。
//
// 🔴 为什么必须发（推翻早先"不发"的决定）：逆向报告 §12.3 认为遥测是 opt-out、
// 服务端不会强制。那个推理对**真客户端**成立 —— 它关了遥测就同时不发推理。
// 对我们不成立：我们的形态是**发了大量推理、遥测恒为零**，
//
//	网关侧：设备今天有 N 次 /proxy 记录
//	遥测侧：该设备报告自己发了 0 次
//
// 这个矛盾无法用"用户关了遥测"解释，它精确指向"凭据被第三方程序使用"。
func BuildReclaudeTelemetryPayload(stats ReclaudeTelemetryStats) ([]byte, error) {
	request := reclaudeTelemetryRequest{
		Rollups:          []reclaudeTelemetryRollup{},
		PassthroughHosts: []string{},
	}

	// 周期内没有任何请求就不产出数据点 —— 真客户端空闲时同样不编造。
	if stats.SuccessCount > 0 || stats.FailureCount > 0 {
		request.Rollups = append(request.Rollups, reclaudeTelemetryRollup{
			Success:        stats.SuccessCount,
			Failure:        stats.FailureCount,
			BytesIn:        stats.BytesIn,
			BytesOut:       stats.BytesOut,
			Edge:           stats.Edge,
			LatencyBuckets: bucketizeReclaudeLatencies(stats.Latencies),
			TTFTBuckets:    bucketizeReclaudeLatencies(nil),
		})
	}

	return json.Marshal(request)
}

// bucketizeReclaudeLatencies 把时延样本装进 18 档直方图。
func bucketizeReclaudeLatencies(samples []time.Duration) []int {
	buckets := make([]int, reclaudeLatencyBucketCount)
	for _, sample := range samples {
		ms := sample.Milliseconds()
		placed := false
		for i, upper := range reclaudeLatencyBucketUpperMs {
			if upper == 0 {
				continue // 溢出桶留到最后
			}
			if ms <= upper {
				buckets[i]++
				placed = true
				break
			}
		}
		// 超过最大上界的落进溢出桶，一个样本都不能丢 ——
		// 丢样本会让上报的总数与网关侧的请求数对不上。
		if !placed {
			buckets[reclaudeLatencyBucketCount-1]++
		}
	}
	return buckets
}

// ReclaudeTelemetryCollector 按账号累计一个上报周期内的用量。
//
// 在转发热路径上被调用，所以必须并发安全且足够轻 —— 只做计数与切片追加。
type ReclaudeTelemetryCollector struct {
	mu        sync.Mutex
	byAccount map[int64]*ReclaudeTelemetryStats
}

// NewReclaudeTelemetryCollector 构造采集器。
func NewReclaudeTelemetryCollector() *ReclaudeTelemetryCollector {
	return &ReclaudeTelemetryCollector{byAccount: map[int64]*ReclaudeTelemetryStats{}}
}

// Record 记一次上游调用。
//
// 失败的调用同样要记：它不产生 usage，却实打实占了对方一次请求 ——
// 真客户端的遥测里也有 failure 计数，只报成功会让两侧数字对不上。
func (c *ReclaudeTelemetryCollector) Record(
	accountID int64, success bool, latency time.Duration, edge string,
) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	stats := c.byAccount[accountID]
	if stats == nil {
		stats = &ReclaudeTelemetryStats{}
		c.byAccount[accountID] = stats
	}
	if success {
		stats.SuccessCount++
	} else {
		stats.FailureCount++
	}
	stats.Latencies = append(stats.Latencies, latency)
	if edge != "" {
		stats.Edge = edge
	}
}

// Snapshot 取走该账号的累计值并清零。
//
// 🔴 取走即清零：不清零会让同一批请求被反复上报，累计数无限膨胀，
// 而那个数字对端是拿来和自己的网关记录对账的。
func (c *ReclaudeTelemetryCollector) Snapshot(accountID int64) ReclaudeTelemetryStats {
	if c == nil {
		return ReclaudeTelemetryStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	stats := c.byAccount[accountID]
	if stats == nil {
		return ReclaudeTelemetryStats{}
	}
	delete(c.byAccount, accountID)
	return *stats
}
