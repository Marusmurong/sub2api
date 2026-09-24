package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 🔴 为什么必须发遥测（推翻早先"不发"的决定）。
//
// 逆向报告 §12.3 的理由是「遥测 opt-out（RECLAUDE_TELEMETRY_DISABLE 可关），
// 服务端不可能把它作为 /proxy 的必要条件」。这个推理对**真客户端**成立，
// 对我们不成立 —— 真客户端关了遥测就同时不发推理；而我们的形态是
// **发了大量推理、遥测恒为零**：
//
//	网关侧：设备今天有 N 次 /proxy 记录（模型、耗时、token、成本俱全）
//	遥测侧：该设备报告自己发了 0 次
//
// 这个矛盾无法用「用户关了遥测」解释，它精确指向「凭据被第三方程序使用」。
// 2026-09-24 连续四台设备在不同条件下（VM/Mac/服务器、daemon 开关、单/双来源）
// 全部被解绑，而这是唯一贯穿所有场景的共同点。
func TestBuildReclaudeTelemetryPayload(t *testing.T) {
	stats := ReclaudeTelemetryStats{
		SuccessCount: 7,
		FailureCount: 1,
		Latencies: []time.Duration{
			800 * time.Millisecond, 1200 * time.Millisecond, 3500 * time.Millisecond,
		},
		BytesIn:  4096,
		BytesOut: 12288,
		Edge:     "www.reclaude.ai",
	}

	t.Run("产出真客户端的三字段结构", func(t *testing.T) {
		payload, err := BuildReclaudeTelemetryPayload(stats)
		require.NoError(t, err)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(payload, &decoded))
		// 与 cli.telemetryUploadRequestWithLeak 对齐（逆向报告 §12）
		require.Contains(t, decoded, "rollups")
		require.Contains(t, decoded, "passthrough_hosts")
		require.Contains(t, decoded, "passthrough_overflow")
	})

	t.Run("rollup 带成功/失败计数与 edge", func(t *testing.T) {
		payload, _ := BuildReclaudeTelemetryPayload(stats)
		var decoded struct {
			Rollups []struct {
				Success int    `json:"success"`
				Failure int    `json:"failure"`
				Edge    string `json:"edge"`
			} `json:"rollups"`
		}
		require.NoError(t, json.Unmarshal(payload, &decoded))
		require.Len(t, decoded.Rollups, 1)
		require.Equal(t, 7, decoded.Rollups[0].Success)
		require.Equal(t, 1, decoded.Rollups[0].Failure)
		require.Equal(t, "www.reclaude.ai", decoded.Rollups[0].Edge)
	})

	t.Run("passthrough_hosts 为空数组而不是 null", func(t *testing.T) {
		// 🔴 我们不做 MITM，本来就没有 passthrough 主机。但必须是 []
		// 而不是 null —— 后者在对端解析时是"字段缺失"，与"没有数据"不同。
		payload, _ := BuildReclaudeTelemetryPayload(stats)

		require.Contains(t, string(payload), `"passthrough_hosts":[]`)
	})

	t.Run("零用量时不产出 rollup", func(t *testing.T) {
		// 一个周期内没有请求就不该编一条 rollup 出来 ——
		// 真客户端空闲时同样不产生数据点。
		payload, err := BuildReclaudeTelemetryPayload(ReclaudeTelemetryStats{Edge: "x"})
		require.NoError(t, err)

		var decoded struct {
			Rollups []any `json:"rollups"`
		}
		require.NoError(t, json.Unmarshal(payload, &decoded))
		require.Empty(t, decoded.Rollups)
	})

	t.Run("时延分桶按真客户端的 18 档", func(t *testing.T) {
		// 逆向报告 §12：TTFT/时延分桶各 18 档。档数不对是结构性差异。
		payload, _ := BuildReclaudeTelemetryPayload(stats)
		var decoded struct {
			Rollups []struct {
				LatencyBuckets []int `json:"latency_buckets"`
			} `json:"rollups"`
		}
		require.NoError(t, json.Unmarshal(payload, &decoded))
		require.Len(t, decoded.Rollups[0].LatencyBuckets, 18)

		// 三个样本必须都落进桶里，一个不丢。
		total := 0
		for _, n := range decoded.Rollups[0].LatencyBuckets {
			total += n
		}
		require.Equal(t, 3, total)
	})
}

// 用量采集器：按账号累计一个周期内的请求，上报后清零。
func TestReclaudeTelemetryCollector(t *testing.T) {
	t.Run("累计成功与失败", func(t *testing.T) {
		c := NewReclaudeTelemetryCollector()
		c.Record(7, true, 900*time.Millisecond, "www.reclaude.ai")
		c.Record(7, true, 1200*time.Millisecond, "www.reclaude.ai")
		c.Record(7, false, 300*time.Millisecond, "www.reclaude.ai")

		stats := c.Snapshot(7)

		require.Equal(t, 2, stats.SuccessCount)
		require.Equal(t, 1, stats.FailureCount)
		require.Len(t, stats.Latencies, 3)
		require.Equal(t, "www.reclaude.ai", stats.Edge)
	})

	t.Run("账号之间互不串数", func(t *testing.T) {
		// 串了会让 A 设备上报 B 设备的用量 —— 比不上报更糟。
		c := NewReclaudeTelemetryCollector()
		c.Record(1, true, time.Second, "a")
		c.Record(2, true, time.Second, "b")

		require.Equal(t, 1, c.Snapshot(1).SuccessCount)
		require.Equal(t, 1, c.Snapshot(2).SuccessCount)
	})

	t.Run("Snapshot 取走即清零", func(t *testing.T) {
		// 不清零会让同一批请求被反复上报，累计数无限膨胀。
		c := NewReclaudeTelemetryCollector()
		c.Record(7, true, time.Second, "x")

		require.Equal(t, 1, c.Snapshot(7).SuccessCount)
		require.Equal(t, 0, c.Snapshot(7).SuccessCount)
	})

	t.Run("没记录过的账号返回零值", func(t *testing.T) {
		require.Equal(t, 0, NewReclaudeTelemetryCollector().Snapshot(99).SuccessCount)
	})

	t.Run("并发写入不 panic", func(t *testing.T) {
		// 转发路径是高并发的，采集器在热路径上。
		c := NewReclaudeTelemetryCollector()
		done := make(chan struct{})
		for i := range 8 {
			go func(n int) {
				defer func() { done <- struct{}{} }()
				for range 50 {
					c.Record(int64(n%3), true, time.Millisecond, "x")
				}
			}(i)
		}
		for range 8 {
			<-done
		}
		total := 0
		for id := range int64(3) {
			total += c.Snapshot(id).SuccessCount
		}
		require.Equal(t, 400, total)
	})
}
