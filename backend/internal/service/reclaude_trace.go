package service

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
)

// ReclaudeTraceCapture 承接一次请求的 traceId。
//
// 为什么需要它：统一 user_id / session_id 之后，**我们自己也分不清上游报错是哪个
// 下游客户触发的了**。traceId 是唯一的排障锚点，必须能从转发层传回上层落库
// （usage_logs.upstream_trace_id）。
//
// 走 context 而不是改 Do 的返回值：Do 返回 *http.Response 是整条链路零改动的
// 支点，不能为了带一个字符串把它破坏掉。
type ReclaudeTraceCapture struct {
	mu      sync.Mutex
	traceID string
}

type reclaudeTraceCaptureKey struct{}

// WithReclaudeTraceCapture 在 ctx 上挂一个 traceId 收集器。
func WithReclaudeTraceCapture(ctx context.Context) (context.Context, *ReclaudeTraceCapture) {
	capture := &ReclaudeTraceCapture{}
	return context.WithValue(ctx, reclaudeTraceCaptureKey{}, capture), capture
}

// TraceID 返回本次请求的 traceId；未发生 reclaude 转发时为空串。
func (c *ReclaudeTraceCapture) TraceID() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.traceID
}

func (c *ReclaudeTraceCapture) set(traceID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.traceID = traceID
}

// reclaudeUsageTraceIDPtr 把 ForwardResult 上的 traceId 转成落库用的指针。
//
// 空串必须变成 nil：非 reclaude 链路恒为空，落空串会让 partial index
// （`WHERE upstream_trace_id IS NOT NULL`）收下每一行，而那个索引的前提
// 正是「绝大多数行为 NULL」。
func reclaudeUsageTraceIDPtr(result *ForwardResult) *string {
	if result == nil {
		return nil
	}
	traceID := strings.TrimSpace(result.UpstreamTraceID)
	if traceID == "" {
		return nil
	}
	return &traceID
}

func reclaudeTraceCaptureFromContext(ctx context.Context) *ReclaudeTraceCapture {
	if ctx == nil {
		return nil
	}
	capture, _ := ctx.Value(reclaudeTraceCaptureKey{}).(*ReclaudeTraceCapture)
	return capture
}

// newBytesBody 把已 buffer 的信封包成可重放的请求体。
//
// 用 bytes.Reader 而不是裸 io.Reader：http.NewRequestWithContext 会据此填好
// GetBody，重定向或 HTTP/2 重试时请求体才能被重放 —— 而信封是签过名的，
// 重放必须逐字节一致。
func newBytesBody(payload []byte) io.Reader {
	return bytes.NewReader(payload)
}
