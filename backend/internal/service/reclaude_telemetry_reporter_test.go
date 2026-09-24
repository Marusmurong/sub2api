//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type recordingTelemetryPoster struct {
	payloads [][]byte
	err      error
}

func (p *recordingTelemetryPoster) PostReclaudeTelemetry(
	_ context.Context, _ *Account, payload []byte,
) (*http.Response, error) {
	p.payloads = append(p.payloads, payload)
	if p.err != nil {
		return nil, p.err
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(`{"ok":true,"accepted":1,"stored":1}`)),
	}, nil
}

func telemetryAccount() *Account {
	return &Account{ID: 42, Platform: PlatformAnthropic, Type: AccountTypeReclaude}
}

func TestReclaudeTelemetryReporter(t *testing.T) {
	at := telemetryAt(t, "2026-09-24T10:00:00Z")

	t.Run("启动后立刻发一次，即使没有任何数据", func(t *testing.T) {
		// 🔴 真客户端是「先 flush 再进 ticker」。刚上线就沉默五分钟
		// 与真值的启动时序对不上。
		poster := &recordingTelemetryPoster{}
		reporter := NewReclaudeTelemetryReporter(poster, NewReclaudeTelemetryCollector())

		reporter.ReportAt(context.Background(), telemetryAccount(), at)

		require.Len(t, poster.payloads, 1)
		require.JSONEq(t, `{"rollups":[],"passthrough_hosts":[]}`, string(poster.payloads[0]))
	})

	t.Run("同一个窗口内不重复发", func(t *testing.T) {
		poster := &recordingTelemetryPoster{}
		reporter := NewReclaudeTelemetryReporter(poster, NewReclaudeTelemetryCollector())
		account := telemetryAccount()

		// 心跳每分钟调一次，窗口是 5 分钟 —— 中间四次必须静默。
		reporter.ReportAt(context.Background(), account, at)
		for i := 1; i < 5; i++ {
			reporter.ReportAt(context.Background(), account, at.Add(time.Duration(i)*time.Minute))
		}

		require.Len(t, poster.payloads, 1)
	})

	t.Run("跨到下一个窗口才再发", func(t *testing.T) {
		poster := &recordingTelemetryPoster{}
		reporter := NewReclaudeTelemetryReporter(poster, NewReclaudeTelemetryCollector())
		account := telemetryAccount()

		reporter.ReportAt(context.Background(), account, at)
		reporter.ReportAt(context.Background(), account, at.Add(ReclaudeTelemetryInterval))

		require.Len(t, poster.payloads, 2)
	})

	t.Run("上报体里带上已关闭窗口的 rollup", func(t *testing.T) {
		collector := NewReclaudeTelemetryCollector()
		poster := &recordingTelemetryPoster{}
		reporter := NewReclaudeTelemetryReporter(poster, collector)
		account := telemetryAccount()

		collector.RecordAt(account.ID, ReclaudeTelemetrySample{
			Edge: "asia.route.reclaude.ai", Outcome: ReclaudeOutcomeOk,
			TTFT: 600 * time.Millisecond, Duration: 2 * time.Second, Bytes: 1234,
		}, at)
		reporter.ReportAt(context.Background(), account, at.Add(ReclaudeTelemetryInterval))

		require.Len(t, poster.payloads, 1)
		var body ReclaudeTelemetryRequest
		require.NoError(t, json.Unmarshal(poster.payloads[0], &body))
		require.Len(t, body.Rollups, 1)
		require.Equal(t, "asia.route.reclaude.ai", body.Rollups[0].Edge)
		require.Equal(t, 1, body.Rollups[0].OkN)
		require.Equal(t, int64(1234), body.Rollups[0].BytesSum)
	})

	t.Run("模拟关机期间整段静默", func(t *testing.T) {
		poster := &recordingTelemetryPoster{}
		reporter := NewReclaudeTelemetryReporter(poster, NewReclaudeTelemetryCollector())
		account := telemetryAccount()
		account.Extra = map[string]any{
			ExtraKeyReclaudeOfflineWindow: map[string]any{
				"timezone": "UTC", "start_hour": float64(0), "duration_hours": float64(23),
			},
		}

		reporter.ReportAt(context.Background(), account, at)

		require.Empty(t, poster.payloads, "关机期间发遥测等于关机没生效")
	})

	t.Run("上报失败不重发同一个窗口", func(t *testing.T) {
		// 重试会把同一个 window_start_ms 再发一遍，而对端按这个键记账。
		poster := &recordingTelemetryPoster{err: errors.New("dial timeout")}
		reporter := NewReclaudeTelemetryReporter(poster, NewReclaudeTelemetryCollector())
		account := telemetryAccount()

		reporter.ReportAt(context.Background(), account, at)
		reporter.ReportAt(context.Background(), account, at.Add(time.Minute))

		require.Len(t, poster.payloads, 1)
	})

	t.Run("非 reclaude 账号与 nil 依赖不发也不 panic", func(t *testing.T) {
		poster := &recordingTelemetryPoster{}
		reporter := NewReclaudeTelemetryReporter(poster, NewReclaudeTelemetryCollector())

		require.NotPanics(t, func() {
			reporter.ReportAt(context.Background(), nil, at)
			reporter.ReportAt(context.Background(), &Account{ID: 1, Type: AccountTypeOAuth}, at)
			var nilReporter *ReclaudeTelemetryReporter
			nilReporter.ReportAt(context.Background(), telemetryAccount(), at)
		})
		require.Empty(t, poster.payloads)
	})
}

func TestReclaudeMeasuredBody(t *testing.T) {
	t.Run("读完后结算一次，带上字节数", func(t *testing.T) {
		var gotBytes int64
		calls := 0
		body := newReclaudeMeasuredBody(io.NopCloser(strings.NewReader("hello world")),
			func(n int64) { calls++; gotBytes = n })

		read, err := io.ReadAll(body)

		require.NoError(t, err)
		require.Equal(t, "hello world", string(read))
		require.Equal(t, int64(11), gotBytes)
		require.Equal(t, 1, calls)
	})

	t.Run("读完又 Close 也只结算一次", func(t *testing.T) {
		// 记两遍会让 n 与网关侧的请求数对不上。
		calls := 0
		body := newReclaudeMeasuredBody(io.NopCloser(strings.NewReader("abc")),
			func(int64) { calls++ })

		_, _ = io.ReadAll(body)
		require.NoError(t, body.Close())

		require.Equal(t, 1, calls)
	})

	t.Run("没读完就 Close 仍然结算已读字节", func(t *testing.T) {
		var gotBytes int64
		body := newReclaudeMeasuredBody(io.NopCloser(strings.NewReader("0123456789")),
			func(n int64) { gotBytes = n })

		buf := make([]byte, 4)
		_, _ = body.Read(buf)
		require.NoError(t, body.Close())

		require.Equal(t, int64(4), gotBytes)
	})

	t.Run("nil body 返回 nil 而不是包一层空壳", func(t *testing.T) {
		require.Nil(t, newReclaudeMeasuredBody(nil, func(int64) {}))
	})
}
