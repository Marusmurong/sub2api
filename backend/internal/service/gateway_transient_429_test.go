//go:build unit

package service

import (
	"net/http"
	"testing"
)

// Anthropic 瞬时过载返回的 429 形态（2026-08-05 生产抓样 19/19 一致）：
//
//	HTTP 429
//	{"type":"error","error":{"type":"rate_limit_error","message":"Error"}}
//	X-Should-Retry: true
//	（无任何 anthropic-ratelimit-* 头，无 retry-after）
//
// 这与配额耗尽是两回事：后者带 reset 头、不带 X-Should-Retry。把前者按后者处理，
// 就会在上游说「立刻重试就行」的时候把账号冷却 30 秒并换号——实测一次抖动顺着账号池
// 挨个标记，762 个请求触发 111 次兜底冷却。
func TestUpstreamSaysRetryable(t *testing.T) {
	cases := []struct {
		name string
		hdr  http.Header
		want bool
	}{
		{
			name: "生产实测形态：瞬时过载",
			hdr:  http.Header{"X-Should-Retry": []string{"true"}},
			want: true,
		},
		{
			name: "大小写不敏感（HTTP 头值大小写不受保证）",
			hdr:  http.Header{"X-Should-Retry": []string{"TRUE"}},
			want: true,
		},
		{
			name: "带空白",
			hdr:  http.Header{"X-Should-Retry": []string{" true "}},
			want: true,
		},
		{
			name: "显式 false 不得当作可重试",
			hdr:  http.Header{"X-Should-Retry": []string{"false"}},
			want: false,
		},
		{
			// 真实配额耗尽：带 reset 头、不带 X-Should-Retry。必须走原有的
			// 冷却 + 换号语义，否则会对着一个真没额度的号反复重试。
			name: "配额耗尽形态：有 reset 头、无 X-Should-Retry",
			hdr: http.Header{
				"Anthropic-Ratelimit-Unified-Reset":  []string{"1786000000"},
				"Anthropic-Ratelimit-Unified-Status": []string{"rejected"},
			},
			want: false,
		},
		{name: "头缺失", hdr: http.Header{}, want: false},
		{name: "nil 头", hdr: nil, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := upstreamSaysRetryable(tc.hdr); got != tc.want {
				t.Errorf("upstreamSaysRetryable() = %v, want %v", got, tc.want)
			}
		})
	}
}

// 重试判定：OAuth 账号原本只对 403 做同号重试，现在带 X-Should-Retry 的 429 也应纳入。
//
// 这条断言的是「判定没有被放宽到所有 429」——不带该头的 429 仍然只能走换号，
// 因为那时我们无从知道这个号还有没有额度。
func TestShouldRetry_TransientRateLimitOnly(t *testing.T) {
	svc := &GatewayService{}
	oauth := &Account{Type: AccountTypeOAuth}

	// shouldRetryUpstreamError 本身不看头，OAuth 仍然只认 403 —— 头的判断由调用点
	// 与它做或运算，这样两条规则各自独立可读。
	if svc.shouldRetryUpstreamError(oauth, 429) {
		t.Error("shouldRetryUpstreamError 不应自行放行 429；该判定属于 upstreamSaysRetryable")
	}
	if !svc.shouldRetryUpstreamError(oauth, 403) {
		t.Error("OAuth 的 403 同号重试是既有行为，不得回归")
	}

	// 组合判据：调用点用的就是这个或运算。
	transient := http.Header{"X-Should-Retry": []string{"true"}}
	quota := http.Header{"Anthropic-Ratelimit-Unified-Reset": []string{"1786000000"}}

	retryable := func(code int, h http.Header) bool {
		return svc.shouldRetryUpstreamError(oauth, code) ||
			(code == http.StatusTooManyRequests && upstreamSaysRetryable(h))
	}

	if !retryable(429, transient) {
		t.Error("瞬时 429 应当同号重试")
	}
	if retryable(429, quota) {
		t.Error("配额耗尽的 429 不得同号重试 —— 那个号确实没额度了")
	}
	if retryable(500, transient) {
		t.Error("X-Should-Retry 只对 429 生效，不得泛化到其它状态码")
	}
}
