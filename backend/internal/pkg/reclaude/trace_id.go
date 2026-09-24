package reclaude

import (
	crand "crypto/rand"
	"encoding/hex"
)

// NewTraceID 按真客户端的方式产出信封 meta 的 traceId。
//
// 🔴 **不要用 uuid.NewString()**。
//
// 真客户端（📄 反编译 tunnel.newTraceID）的实现是：
//
//	crand.Read(16B) → hex(32 字符) → 按 8-4-4-4-12 拼接
//
// 它只是长得像 UUID，**不设 version/variant 位**。而 RFC4122 v4 的第 14 位恒为
// '4'、第 19 位恒 ∈ {8,9,a,b}。
//
// 这个差异是决定性的：✅ 26 条真值抓包里只有 1 条偶然符合 v4 形态
// （纯随机期望 26/64 ≈ 0.4，完全吻合），而用 uuid 包就是 100%。服务端一条
// 正则就能把我们从真客户端流量里筛出来，几十个请求即达到天文数字级置信度。
//
// 2026-09-24 排查四台设备连续 device_revoked 时定位到的最强候选成因之一。
func NewTraceID() string {
	var raw [16]byte
	// crypto/rand 在现代 Go 里不会失败（失败即进程级不可恢复），这里忽略错误
	// 与真客户端一致 —— 它同样直接用返回值。
	_, _ = crand.Read(raw[:])

	src := make([]byte, hex.EncodedLen(len(raw)))
	hex.Encode(src, raw[:])

	// 8-4-4-4-12
	out := make([]byte, 0, 36)
	out = append(out, src[0:8]...)
	out = append(out, '-')
	out = append(out, src[8:12]...)
	out = append(out, '-')
	out = append(out, src[12:16]...)
	out = append(out, '-')
	out = append(out, src[16:20]...)
	out = append(out, '-')
	out = append(out, src[20:32]...)
	return string(out)
}
