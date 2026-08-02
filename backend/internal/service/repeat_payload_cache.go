package service

import (
	"context"
	"time"
)

// RepeatPayloadCache 统计「同一 key 在窗口内提交同一份 payload 的次数」。
//
// 接口在 service 声明、实现在 repository，与站内其它 cache 一致。
type RepeatPayloadCache interface {
	// IncrementRepeatCount 自增并返回当前窗口内的累计次数（含本次）。
	//
	// 固定窗口：TTL 只在首次创建时设置，不随后续命中续期，窗口到期自动清零，
	// 被拦下的客户端因此会自行恢复，不会被永久锁死。
	IncrementRepeatCount(ctx context.Context, scope RepeatPayloadScope, apiKeyID int64, fingerprint string, window time.Duration) (int64, error)
}
