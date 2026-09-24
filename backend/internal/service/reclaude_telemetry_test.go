//go:build unit

package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func telemetryAt(t *testing.T, iso string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, iso)
	require.NoError(t, err)
	return at
}

func TestReclaudeTelemetryPayloadShape(t *testing.T) {
	t.Run("顶层键与真值样本一致", func(t *testing.T) {
		// ✅ ~/.reclaude/telemetry-pending.json 顶层只有这两个键。
		raw, err := BuildReclaudeTelemetryPayload(nil)
		require.NoError(t, err)

		var decoded map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &decoded))
		require.ElementsMatch(t, []string{"rollups", "passthrough_hosts"}, keysOf(decoded))
	})

	t.Run("空集合序列化成 [] 而不是 null", func(t *testing.T) {
		// null 在对端看来是「字段缺失」，空数组才是「没有数据」。
		raw, err := BuildReclaudeTelemetryPayload(nil)
		require.NoError(t, err)
		require.JSONEq(t, `{"rollups":[],"passthrough_hosts":[]}`, string(raw))
	})

	t.Run("rollup 字段名逐字对齐真值", func(t *testing.T) {
		raw, err := BuildReclaudeTelemetryPayload([]ReclaudeTelemetryRollup{{
			WindowSecs:  ReclaudeTelemetryWindowSecs,
			TTFTBuckets: make([]int, reclaudeTelemetryBucketCount),
			DurBuckets:  make([]int, reclaudeTelemetryBucketCount),
		}})
		require.NoError(t, err)

		var decoded struct {
			Rollups []map[string]json.RawMessage `json:"rollups"`
		}
		require.NoError(t, json.Unmarshal(raw, &decoded))
		require.Len(t, decoded.Rollups, 1)
		require.ElementsMatch(t, []string{
			"edge", "window_start_ms", "window_secs", "ttft_buckets", "dur_buckets",
			"bytes_sum", "dur_ms_sum", "n", "ttft_n", "ok_n",
			"fail_forward_n", "fail_write_n", "http_error_n",
		}, keysOf(decoded.Rollups[0]))
	})

	t.Run("两个直方图都是定长 18", func(t *testing.T) {
		collector := NewReclaudeTelemetryCollector()
		at := telemetryAt(t, "2026-09-24T10:00:00Z")
		collector.RecordAt(1, ReclaudeTelemetrySample{Outcome: ReclaudeOutcomeOk}, at)

		rollups := collector.DrainClosedWindows(1, at.Add(ReclaudeTelemetryInterval))
		require.Len(t, rollups, 1)
		require.Len(t, rollups[0].TTFTBuckets, 18)
		require.Len(t, rollups[0].DurBuckets, 18)
	})
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// 真值样本里交叉验证过的三条不变式。
func TestReclaudeTelemetryRollupInvariants(t *testing.T) {
	at := telemetryAt(t, "2026-09-24T10:00:00Z")
	collector := NewReclaudeTelemetryCollector()

	for _, sample := range []ReclaudeTelemetrySample{
		{Outcome: ReclaudeOutcomeOk, TTFT: 600 * time.Millisecond, Duration: 2500 * time.Millisecond, Bytes: 100},
		{Outcome: ReclaudeOutcomeOk, TTFT: 700 * time.Millisecond, Duration: 3000 * time.Millisecond, Bytes: 200},
		{Outcome: ReclaudeOutcomeHTTPError, TTFT: 620 * time.Millisecond, Duration: 9 * time.Second, Bytes: 50},
		{Outcome: ReclaudeOutcomeFailWrite, TTFT: 640 * time.Millisecond},
		{Outcome: ReclaudeOutcomeFailForward},
	} {
		collector.RecordAt(7, sample, at)
	}

	rollups := collector.DrainClosedWindows(7, at.Add(ReclaudeTelemetryInterval))
	require.Len(t, rollups, 1)
	r := rollups[0]

	t.Run("n 等于四个归类之和", func(t *testing.T) {
		require.Equal(t, r.N, r.OkN+r.FailForwardN+r.FailWriteN+r.HTTPErrorN)
		require.Equal(t, 5, r.N)
	})

	t.Run("sum(ttft_buckets) 等于 ttft_n", func(t *testing.T) {
		require.Equal(t, r.TTFTN, sumInts(r.TTFTBuckets))
		require.Equal(t, 4, r.TTFTN, "没测到 TTFT 的样本不计入")
	})

	t.Run("sum(dur_buckets) 等于 ok_n —— dur 只记成功请求", func(t *testing.T) {
		require.Equal(t, r.OkN, sumInts(r.DurBuckets))
		require.Equal(t, 2, r.OkN)
	})

	t.Run("dur_ms_sum 不含失败请求的耗时", func(t *testing.T) {
		// 那条 http_error 样本耗时 9s；算进去会变成 14500。
		require.Equal(t, int64(5500), r.DurMsSum)
	})

	t.Run("bytes_sum 含所有样本", func(t *testing.T) {
		require.Equal(t, int64(350), r.BytesSum)
	})
}

func sumInts(values []int) int {
	total := 0
	for _, v := range values {
		total += v
	}
	return total
}

func TestReclaudeTelemetryWindowing(t *testing.T) {
	t.Run("窗口起点对齐到 300 秒边界", func(t *testing.T) {
		at := telemetryAt(t, "2026-09-24T10:07:13Z")
		start := ReclaudeTelemetryWindowStartMs(at)
		require.Equal(t, telemetryAt(t, "2026-09-24T10:05:00Z").UnixMilli(), start)
		require.Zero(t, start%(ReclaudeTelemetryWindowSecs*1000))
	})

	t.Run("当前窗口不被取走", func(t *testing.T) {
		// 🔴 提前上报会让同一个 window_start_ms 被报两次。
		collector := NewReclaudeTelemetryCollector()
		at := telemetryAt(t, "2026-09-24T10:07:13Z")
		collector.RecordAt(1, ReclaudeTelemetrySample{Outcome: ReclaudeOutcomeOk}, at)

		require.Empty(t, collector.DrainClosedWindows(1, at.Add(time.Minute)))
		require.Len(t, collector.DrainClosedWindows(1, at.Add(ReclaudeTelemetryInterval)), 1)
	})

	t.Run("取走即清零，不会重复上报", func(t *testing.T) {
		collector := NewReclaudeTelemetryCollector()
		at := telemetryAt(t, "2026-09-24T10:00:00Z")
		collector.RecordAt(1, ReclaudeTelemetrySample{Outcome: ReclaudeOutcomeOk}, at)
		later := at.Add(ReclaudeTelemetryInterval)

		require.Len(t, collector.DrainClosedWindows(1, later), 1)
		require.Empty(t, collector.DrainClosedWindows(1, later))
	})

	t.Run("多个积压窗口按时间升序返回", func(t *testing.T) {
		// 真客户端的积压文件里 rollup 严格按时间递增；map 迭代是乱序的。
		collector := NewReclaudeTelemetryCollector()
		base := telemetryAt(t, "2026-09-24T10:00:00Z")
		for i := 0; i < 5; i++ {
			collector.RecordAt(1, ReclaudeTelemetrySample{Outcome: ReclaudeOutcomeOk},
				base.Add(time.Duration(i)*ReclaudeTelemetryInterval))
		}

		rollups := collector.DrainClosedWindows(1, base.Add(5*ReclaudeTelemetryInterval))

		require.Len(t, rollups, 5)
		for i := 1; i < len(rollups); i++ {
			require.Less(t, rollups[i-1].WindowStartMs, rollups[i].WindowStartMs)
		}
	})

	t.Run("账号之间互不串数据", func(t *testing.T) {
		collector := NewReclaudeTelemetryCollector()
		at := telemetryAt(t, "2026-09-24T10:00:00Z")
		collector.RecordAt(1, ReclaudeTelemetrySample{Outcome: ReclaudeOutcomeOk}, at)
		collector.RecordAt(2, ReclaudeTelemetrySample{Outcome: ReclaudeOutcomeHTTPError}, at)

		later := at.Add(ReclaudeTelemetryInterval)
		first := collector.DrainClosedWindows(1, later)
		require.Len(t, first, 1)
		require.Equal(t, 1, first[0].OkN)
		require.Zero(t, first[0].HTTPErrorN)
	})
}

func TestReclaudeTelemetryBucketing(t *testing.T) {
	t.Run("超过最大上界落进溢出桶，不丢样本", func(t *testing.T) {
		buckets := bucketizeReclaudeDurations([]time.Duration{10 * time.Minute})
		require.Equal(t, 1, buckets[reclaudeTelemetryBucketCount-1])
		require.Equal(t, 1, sumInts(buckets))
	})

	t.Run("零样本产出全零的定长数组而不是 nil", func(t *testing.T) {
		buckets := bucketizeReclaudeDurations(nil)
		require.Len(t, buckets, reclaudeTelemetryBucketCount)
		require.Zero(t, sumInts(buckets))
	})
}
