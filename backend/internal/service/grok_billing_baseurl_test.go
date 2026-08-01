//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// 计费探测的基址必须落在 CLI 网关上。
//
// 生产事故（2026-08-01）：账号 29 的 credentials.base_url 是 https://api.x.ai/v1
// （xAI 官方的推理接口），而计费只存在于 https://cli-chat-proxy.grok.com/v1。
// buildGrokBillingURL 直接拿 GetGrokBaseURL() 去拼，于是探测打成
// https://api.x.ai/v1/billing → 持续 404（每 10 分钟一次）。
//
// 后果不只是日志噪音：拿不到 plan/配额任何一项，GrokMediaGenerationEligibility
// 判定为 billing_inconclusive，媒体资格被拒——视频生成返回
// 503 No eligible Grok media accounts。
//
// 实测确认上游本身没问题：直接打 cli-chat-proxy.grok.com/v1/billing 返回 200，
// monthlyLimit=150000（$1500，SuperGrok Heavy）、used=949。
//
// 这与 GetGrokMediaBaseURL 是对称的一对：媒体**不在** CLI 网关上，命中 CLI 网关时
// 改用 API 主机；计费**只在** CLI 网关上，不在时改用 CLI 网关。
func TestGetGrokBillingBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
		reason  string
	}{
		{
			name:    "官方 API 主机 → 回到 CLI 网关",
			baseURL: "https://api.x.ai/v1",
			want:    xai.DefaultCLIBaseURL,
			reason:  "api.x.ai 上没有 /billing，这是生产上 404 的成因",
		},
		{
			name:    "已经是 CLI 网关 → 保持",
			baseURL: xai.DefaultCLIBaseURL,
			want:    xai.DefaultCLIBaseURL,
		},
		{
			name:    "第三方中转 → 保持",
			baseURL: "https://relay.example.test/v1",
			want:    "https://relay.example.test/v1",
			reason:  "运维刻意指向自建中转时，计费探测应跟随同一上游",
		},
		{
			name:    "区域主机 → 回到 CLI 网关",
			baseURL: "https://api.us-east-1.x.ai/v1",
			want:    xai.DefaultCLIBaseURL,
			reason:  "xAI 的区域推理主机同样不提供 /billing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Account{
				Platform:    PlatformGrok,
				Type:        AccountTypeOAuth,
				Credentials: map[string]any{"base_url": tt.baseURL},
			}
			if got := a.GetGrokBillingBaseURL(); got != tt.want {
				t.Errorf("GetGrokBillingBaseURL() = %q, want %q  (%s)", got, tt.want, tt.reason)
			}
		})
	}
}

// 非 Grok 账号返回空，与同族的其它 getter 一致。
func TestGetGrokBillingBaseURL_NonGrok(t *testing.T) {
	a := &Account{Platform: PlatformAnthropic}
	if got := a.GetGrokBillingBaseURL(); got != "" {
		t.Errorf("非 Grok 账号应返回空, got %q", got)
	}
}
