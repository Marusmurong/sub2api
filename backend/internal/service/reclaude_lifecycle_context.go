package service

import "context"

type reclaudeSyntheticContextKey struct{}

// WithReclaudeSynthetic 标记一条**我们自己合成**的请求。
//
// 🔴 这个标记是递归防线。合成流量与推理流量走同一个 ReclaudeUpstream.Do，
// 而 Do 里会「看到一次请求就考虑要不要补生命周期流量」——
// 不区分来源的话，每条合成请求又会触发一批新的合成请求，指数爆炸，
// 几秒内就能把账号打成异常。
func WithReclaudeSynthetic(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, reclaudeSyntheticContextKey{}, true)
}

// IsReclaudeSynthetic 报告这条请求是否由我们合成。
func IsReclaudeSynthetic(ctx context.Context) bool {
	return ctx != nil && ctx.Value(reclaudeSyntheticContextKey{}) == true
}
