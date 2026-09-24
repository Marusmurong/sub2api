package service

import (
	"io"
	"sync"
	"time"
)

// reclaudeMeasuredBody 包住响应体，在读完/关闭时结算这次调用的耗时与字节数。
//
// 🔴 为什么非包不可：TTFT 在响应头到达时就知道了，但 dur 与 bytes 要等整段
// 流读完。在收到响应头的那一刻就结算，等于把 TTFT 当成 dur 上报 ——
// 对端看到的是一批「首字节即结束」的请求，而真值里 dur 明显大于 ttft
// （样本：ttft 落在 500~750ms 档，dur 落在 2~3s 档）。
// 编出来的数字比不发更糟：不发只是缺席，编错是主动提供一个矛盾的证词。
type reclaudeMeasuredBody struct {
	inner io.ReadCloser
	bytes int64

	once   sync.Once
	finish func(bytes int64)
}

// newReclaudeMeasuredBody 包装响应体。finish 在流结束（EOF 或 Close）时恰好被调用一次。
func newReclaudeMeasuredBody(inner io.ReadCloser, finish func(bytes int64)) io.ReadCloser {
	if inner == nil {
		return nil
	}
	return &reclaudeMeasuredBody{inner: inner, finish: finish}
}

func (b *reclaudeMeasuredBody) Read(p []byte) (int, error) {
	n, err := b.inner.Read(p)
	b.bytes += int64(n)
	if err != nil {
		// 包括 io.EOF：正常读完也要结算，不能只依赖 Close ——
		// 有的调用方读到 EOF 就不再 Close。
		b.settle()
	}
	return n, err
}

func (b *reclaudeMeasuredBody) Close() error {
	b.settle()
	return b.inner.Close()
}

// settle 结算且只结算一次。
//
// EOF 与 Close 通常都会发生，sync.Once 保证这次调用不会被记两遍 ——
// 记两遍会让 n 与网关侧的请求数对不上，而那正是对账用的字段。
func (b *reclaudeMeasuredBody) settle() {
	b.once.Do(func() {
		if b.finish != nil {
			b.finish(b.bytes)
		}
	})
}

// reclaudeCallTimer 记录一次上游调用的时间点。
type reclaudeCallTimer struct {
	startedAt time.Time
	ttft      time.Duration
}

func newReclaudeCallTimer(now time.Time) *reclaudeCallTimer {
	return &reclaudeCallTimer{startedAt: now}
}

// markTTFT 在响应头到达时记首字节时延。
func (t *reclaudeCallTimer) markTTFT(now time.Time) {
	if t.ttft == 0 {
		t.ttft = now.Sub(t.startedAt)
	}
}

func (t *reclaudeCallTimer) elapsed(now time.Time) time.Duration {
	return now.Sub(t.startedAt)
}
