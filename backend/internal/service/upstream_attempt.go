package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// WithUpstreamAttempt 标注本次上游请求是同一账号上的第几次尝试（从 1 起）。
//
// 真实 Anthropic SDK 第 n 次重试会把 X-Stainless-Retry-Count 发成 n；伪装路径此前
// 恒发 "0"，于是上游会看到短时间内同一会话连续多个「第 0 次」请求、request-id 各不
// 相同——这是 2026-09-07 审计的 H-4。只在 service 内部的重试循环里打标，跨账号
// 故障转移不带过去：换到另一个账号，从那个账号的视角就是一个全新的请求。
func WithUpstreamAttempt(ctx context.Context, attempt int) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxkey.UpstreamAttempt, attempt)
}

// nextUpstreamAttemptCtx 给「本次 Forward 调用内的下一次出站」打序号：计数器自增后
// 写进派生 ctx。Forward 的重试循环里除了主循环，还有四处同一轮内的补救重发
// （剥 thinking 签名 / 预算修正 / 剥采样参数），每一处都是对同一账号的又一次真实
// 出站请求，必须一起计数——只给主循环打标会让这些重发继续自称「第 0 次」。
func nextUpstreamAttemptCtx(ctx context.Context, counter *int) context.Context {
	if counter == nil {
		return WithUpstreamAttempt(ctx, 1)
	}
	*counter++
	return WithUpstreamAttempt(ctx, *counter)
}

// upstreamAttemptFromContext 读取尝试序号；未标注时按首次请求（1）处理。
func upstreamAttemptFromContext(ctx context.Context) int {
	if ctx == nil {
		return 1
	}
	if n, ok := ctx.Value(ctxkey.UpstreamAttempt).(int); ok && n > 0 {
		return n
	}
	return 1
}
