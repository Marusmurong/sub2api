package service

import (
	"encoding/json"
	"sync"
	"time"
)

// ReclaudeTelemetryWindowSecs 是一个 rollup 窗口的长度。
//
// ✅ 真值：`~/.reclaude/telemetry-pending.json` 里每条 rollup 的 window_secs
// 恒为 300，相邻 window_start_ms 间隔 299998~300076ms。
const ReclaudeTelemetryWindowSecs = 300

// ReclaudeTelemetryInterval 是上报周期，与窗口长度同步。
const ReclaudeTelemetryInterval = ReclaudeTelemetryWindowSecs * time.Second

// reclaudeTelemetryBucketCount 是两个直方图的档数。
//
// ✅ 真值：ttft_buckets 与 dur_buckets **都是定长 18**。
// 对端按固定长度数组解析，多一档少一档都说明发的不是同一个客户端。
const reclaudeTelemetryBucketCount = 18

// reclaudeTelemetryBucketUpperMs 是各档上界（毫秒），末档为无上界溢出桶。
//
// ⚠️ **档位边界未经实测**：落盘样本只给了各档计数，没给边界定义。
// 这里沿用指数递增的阶梯（真实推理时延跨百毫秒到分钟级，线性分桶会让
// 九成样本挤在头两档）。样本里的分布（集中在 index 5~9）与该阶梯自洽，
// 但**不构成证明**。若日后抓到边界真值，改这里即可，结构不用动。
var reclaudeTelemetryBucketUpperMs = [reclaudeTelemetryBucketCount]int64{
	50, 100, 200, 300, 500, 750, 1000, 1500, 2000,
	3000, 5000, 7500, 10000, 15000, 20000, 30000, 60000, 0, // 0 = 无上界
}

// ReclaudeTelemetryRollup 是一个 300 秒窗口的汇总，字段与顺序对齐真客户端。
//
// ✅ 真值：`~/.reclaude/telemetry-pending.json`（真客户端因设备被撤销而
// 积压在本地的待上报体，10 条 rollup）。字段名与顺序逐字对齐该文件。
//
// 🔴 三条在样本里交叉验证过的不变式，改动时不要破坏：
//   - `n == ok_n + fail_forward_n + fail_write_n + http_error_n`
//   - `sum(ttft_buckets) == ttft_n`
//   - `sum(dur_buckets) == ok_n` —— **dur 只记成功请求**。
//     样本 1（ok_n=2）dur_buckets 合计 2；样本 2（ok_n=0）dur 全 0 且
//     dur_ms_sum=0。把失败请求的耗时也记进去会立刻破坏这条关系。
type ReclaudeTelemetryRollup struct {
	Edge          string `json:"edge"`
	WindowStartMs int64  `json:"window_start_ms"`
	WindowSecs    int    `json:"window_secs"`
	TTFTBuckets   []int  `json:"ttft_buckets"`
	DurBuckets    []int  `json:"dur_buckets"`
	BytesSum      int64  `json:"bytes_sum"`
	DurMsSum      int64  `json:"dur_ms_sum"`
	N             int    `json:"n"`
	TTFTN         int    `json:"ttft_n"`
	OkN           int    `json:"ok_n"`
	FailForwardN  int    `json:"fail_forward_n"`
	FailWriteN    int    `json:"fail_write_n"`
	HTTPErrorN    int    `json:"http_error_n"`
}

// ReclaudeTelemetryPassthroughHost 是一条 MITM 放行主机的统计。
//
// ✅ 真值：`[{"host":"api.relayapi.me","n":86,"listeners":["connect"]}]`。
// 早先我们把它写成 []string —— 结构整个错了。
type ReclaudeTelemetryPassthroughHost struct {
	Host      string   `json:"host"`
	N         int      `json:"n"`
	Listeners []string `json:"listeners"`
}

// ReclaudeTelemetryRequest 是 POST /client/telemetry 的请求体。
//
// 🔴 2026-09-29 逐字节复核真值抓包(reclaude-live-2026-09-27/interactive 两份
// telemetry body)：顶层**恰好 3 个键**，passthrough_overflow=0 是**显式带的**
// (`{"rollups":[],"passthrough_hosts":[...],"passthrough_overflow":0}`)。此前注释
// 说"只有两个键 + omitempty"是样本没逐字节看清的误判 —— 改为显式发 0(去 omitempty),
// 与真值 3-key 一致;omitempty 会在 0 时省略字段,发出 2-key,与真值不符。
type ReclaudeTelemetryRequest struct {
	Rollups []ReclaudeTelemetryRollup `json:"rollups"`
	// 🔴 必须是 [] 而不是 null：null 在对端看来是「字段缺失」，空数组才是
	// 「没有数据」。有同机 passthrough.json 时填真值观测,否则空(见 passthrough source)。
	PassthroughHosts    []ReclaudeTelemetryPassthroughHost `json:"passthrough_hosts"`
	PassthroughOverflow int64                              `json:"passthrough_overflow"`
}

// ReclaudeTelemetrySample 是热路径上报的一次调用。
type ReclaudeTelemetrySample struct {
	// Edge 是这次调用实际发往的网关标签，与信封 meta 的 edge 同源。
	Edge string
	// TTFT 是首字节时延；未测到时传 0（不会计入 ttft_n）。
	TTFT time.Duration
	// Duration 是整段耗时，**只在 Outcome 为 Ok 时有意义**。
	Duration time.Duration
	Bytes    int64
	Outcome  ReclaudeTelemetryOutcome
}

// ReclaudeTelemetryOutcome 是一次调用的四种归类，与 rollup 的四个计数器一一对应。
type ReclaudeTelemetryOutcome int

const (
	// ReclaudeOutcomeOk 上游返回 2xx。
	ReclaudeOutcomeOk ReclaudeTelemetryOutcome = iota
	// ReclaudeOutcomeFailForward 请求没能送到上游（连接/代理失败）。
	ReclaudeOutcomeFailForward
	// ReclaudeOutcomeFailWrite 响应回写下游失败。
	ReclaudeOutcomeFailWrite
	// ReclaudeOutcomeHTTPError 上游返回非 2xx。
	ReclaudeOutcomeHTTPError
)

// reclaudeWindowAccumulator 累计单个账号在单个窗口内的样本。
type reclaudeWindowAccumulator struct {
	edge         string
	ttftSamples  []time.Duration
	durSamples   []time.Duration
	bytesSum     int64
	durMsSum     int64
	n            int
	ttftN        int
	okN          int
	failForwardN int
	failWriteN   int
	httpErrorN   int
}

// ReclaudeTelemetryCollector 按「账号 + 窗口起点」累计样本。
//
// 在转发热路径上被调用，所以必须并发安全且足够轻 —— 只做计数与切片追加。
//
// 🔴 按窗口分桶而不是只留一个当前桶：上报是每 300 秒一次，而请求随时在进。
// 只留一个桶的话，跨越窗口边界的那批请求会被算进错误的窗口，
// window_start_ms 与桶内数据对不上 —— 而那正是对端拿来对账的字段。
type ReclaudeTelemetryCollector struct {
	mu       sync.Mutex
	byWindow map[reclaudeWindowKey]*reclaudeWindowAccumulator
}

type reclaudeWindowKey struct {
	accountID     int64
	windowStartMs int64
}

// NewReclaudeTelemetryCollector 构造采集器。
func NewReclaudeTelemetryCollector() *ReclaudeTelemetryCollector {
	return &ReclaudeTelemetryCollector{
		byWindow: map[reclaudeWindowKey]*reclaudeWindowAccumulator{},
	}
}

// ReclaudeTelemetryWindowStartMs 把时刻对齐到所属窗口的起点（毫秒）。
func ReclaudeTelemetryWindowStartMs(at time.Time) int64 {
	windowMs := int64(ReclaudeTelemetryWindowSecs) * 1000
	return (at.UnixMilli() / windowMs) * windowMs
}

// RecordAt 记一次调用。
//
// 失败的调用同样要记：它不产生 usage，却实打实占了对方一次请求 ——
// 真客户端的 rollup 里 fail/http_error 计数是主要成分（样本里 39/39 全是
// http_error），只报成功会让两侧数字对不上。
func (c *ReclaudeTelemetryCollector) RecordAt(
	accountID int64, sample ReclaudeTelemetrySample, at time.Time,
) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	key := reclaudeWindowKey{accountID: accountID, windowStartMs: ReclaudeTelemetryWindowStartMs(at)}
	acc := c.byWindow[key]
	if acc == nil {
		acc = &reclaudeWindowAccumulator{}
		c.byWindow[key] = acc
	}

	acc.n++
	acc.bytesSum += sample.Bytes
	if sample.Edge != "" {
		acc.edge = sample.Edge
	}
	if sample.TTFT > 0 {
		acc.ttftN++
		acc.ttftSamples = append(acc.ttftSamples, sample.TTFT)
	}

	switch sample.Outcome {
	case ReclaudeOutcomeOk:
		acc.okN++
		// 🔴 只有成功请求进 dur —— 见 ReclaudeTelemetryRollup 的不变式说明。
		acc.durSamples = append(acc.durSamples, sample.Duration)
		acc.durMsSum += sample.Duration.Milliseconds()
	case ReclaudeOutcomeFailForward:
		acc.failForwardN++
	case ReclaudeOutcomeFailWrite:
		acc.failWriteN++
	case ReclaudeOutcomeHTTPError:
		acc.httpErrorN++
	}
}

// DrainClosedWindows 取走某账号**已经关闭**的窗口并清零。
//
// 🔴 只取已关闭的窗口：当前窗口还在收数据，提前上报会让同一个
// window_start_ms 被报两次，而对端是按这个键做记账的。
//
// 取走即清零：不清零会让同一批请求被反复上报，累计数无限膨胀。
// PeekHasClosedWindows 非消费性判断该账号是否有已关闭窗口的 rollup 待发。
//
// 🔴 与 DrainClosedWindows 的区别:只读不删。用于三空跳过门槛在 shouldReport
// 之前判有无内容,避免"先 drain 再被 shouldReport 挡下 → rollup 丢失"。
func (c *ReclaudeTelemetryCollector) PeekHasClosedWindows(accountID int64, now time.Time) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	currentWindow := ReclaudeTelemetryWindowStartMs(now)
	for key := range c.byWindow {
		if key.accountID == accountID && key.windowStartMs < currentWindow {
			return true
		}
	}
	return false
}

func (c *ReclaudeTelemetryCollector) DrainClosedWindows(
	accountID int64, now time.Time,
) []ReclaudeTelemetryRollup {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	currentWindow := ReclaudeTelemetryWindowStartMs(now)
	rollups := make([]ReclaudeTelemetryRollup, 0, 4)
	for key, acc := range c.byWindow {
		if key.accountID != accountID || key.windowStartMs >= currentWindow {
			continue
		}
		rollups = append(rollups, acc.toRollup(key.windowStartMs))
		delete(c.byWindow, key)
	}
	sortReclaudeRollupsByWindow(rollups)
	return rollups
}

func (a *reclaudeWindowAccumulator) toRollup(windowStartMs int64) ReclaudeTelemetryRollup {
	return ReclaudeTelemetryRollup{
		Edge:          a.edge,
		WindowStartMs: windowStartMs,
		WindowSecs:    ReclaudeTelemetryWindowSecs,
		TTFTBuckets:   bucketizeReclaudeDurations(a.ttftSamples),
		DurBuckets:    bucketizeReclaudeDurations(a.durSamples),
		BytesSum:      a.bytesSum,
		DurMsSum:      a.durMsSum,
		N:             a.n,
		TTFTN:         a.ttftN,
		OkN:           a.okN,
		FailForwardN:  a.failForwardN,
		FailWriteN:    a.failWriteN,
		HTTPErrorN:    a.httpErrorN,
	}
}

// sortReclaudeRollupsByWindow 按窗口起点升序排列。
//
// map 迭代顺序随机，而真客户端的积压文件里 rollup 是严格按时间递增的 ——
// 乱序上报是一个不必要的、每次都不同的可判别特征。
func sortReclaudeRollupsByWindow(rollups []ReclaudeTelemetryRollup) {
	for i := 1; i < len(rollups); i++ {
		for j := i; j > 0 && rollups[j-1].WindowStartMs > rollups[j].WindowStartMs; j-- {
			rollups[j-1], rollups[j] = rollups[j], rollups[j-1]
		}
	}
}

// BuildReclaudeTelemetryPayload 把若干 rollup 编成上报体。
//
// 🔴 为什么必须发（推翻早先「不发」的决定）：逆向报告 §12.3 认为遥测是
// opt-out、服务端不会强制。那个推理对**真客户端**成立 —— 它关了遥测就同时
// 不发推理。对我们不成立：我们的形态是**发了大量推理、遥测恒为零**，
//
//	网关侧：设备今天有 N 次 /proxy 记录
//	遥测侧：该设备报告自己发了 0 次
//
// 这个矛盾无法用「用户关了遥测」解释，它精确指向「凭据被第三方程序使用」。
func BuildReclaudeTelemetryPayload(
	rollups []ReclaudeTelemetryRollup,
	passthrough []ReclaudeTelemetryPassthroughHost,
) ([]byte, error) {
	if rollups == nil {
		rollups = []ReclaudeTelemetryRollup{}
	}
	if passthrough == nil {
		// 恒发空数组不发 null(对端把 null 当字段缺失)。
		passthrough = []ReclaudeTelemetryPassthroughHost{}
	}
	return json.Marshal(ReclaudeTelemetryRequest{
		Rollups:          rollups,
		PassthroughHosts: passthrough,
		// passthrough_overflow 显式发 0(去 omitempty,对齐真值 3-key)。
		PassthroughOverflow: 0,
	})
}

// ReclaudeTelemetryHasContent 判断这批数据是否值得上报。
//
// 🔴 真客户端 flushTelemetry:rollups + passthrough_hosts + overflow 三者全空则
// **跳过发送**(2026-09-29 反汇编实证)。sub 对齐这个门槛,不每 300s 硬发一个空体
// —— 一个"周期性发空 telemetry"本身是可判别特征。
func ReclaudeTelemetryHasContent(
	rollups []ReclaudeTelemetryRollup,
	passthrough []ReclaudeTelemetryPassthroughHost,
) bool {
	return len(rollups) > 0 || len(passthrough) > 0
}

// bucketizeReclaudeDurations 把时延样本装进 18 档直方图。
func bucketizeReclaudeDurations(samples []time.Duration) []int {
	buckets := make([]int, reclaudeTelemetryBucketCount)
	for _, sample := range samples {
		ms := sample.Milliseconds()
		placed := false
		for i, upper := range reclaudeTelemetryBucketUpperMs {
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
		// 丢样本会破坏 sum(buckets) == n 的不变式，而对端拿它对账。
		if !placed {
			buckets[reclaudeTelemetryBucketCount-1]++
		}
	}
	return buckets
}
