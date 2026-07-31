//go:build unit

package handler

import (
	"testing"
)

// 非 Claude Code 客户端就地拦截的 UA 判定。
//
// 背景（2026-07-31）：Anthropic 在 8 小时内逐个吊销了 7 个订阅账号
//
//	Token revoked (401): OAuth access token has been revoked
//
// 在此之前这些账号持续收到 "Third-party apps now draw from your extra usage"。
// 该错误 100% 集中在一个下游 key（自建 Go 中转，背后是 Go SDK / Python SDK /
// CSSwitch 等第三方工具），而共用同一批账号的其它 key 零发生——成因在请求形态，
// 不在账号。
//
// 判定只看 UA，刻意**不用** ClaudeCodeValidator 的全套逻辑：实测全站 6945 个请求
// 里，有 854 个 UA 正确、X-App/anthropic-beta/anthropic-version 三个头齐全，仅因
// system prompt 相似度不达标被判非 CC。那 854 个是真 Claude Code 的子请求形态，
// 用全套判定会把自己人一起打掉。只看 UA 就不会误伤它们。
func TestIsNonClaudeCodeUserAgent(t *testing.T) {
	tests := []struct {
		name   string
		ua     string
		want   bool // true = 应当拦截
		reason string
	}{
		{name: "Go SDK", ua: "Go-http-client/2.0", want: true},
		{name: "Go SDK 1.1", ua: "Go-http-client/1.1", want: true},
		{name: "Anthropic Go SDK", ua: "Anthropic/Go 1.19.0", want: true},
		{name: "Python OpenAI SDK", ua: "AsyncOpenAI/Python 2.45.0", want: true},
		{name: "CSSwitch", ua: "CSSwitch/0.2 (+https://github.com/SuperJJ007/CSSwitch)", want: true},
		{name: "curl", ua: "curl/8.7.1", want: true},
		{name: "空 UA", ua: "", want: true, reason: "不表明身份的一律按第三方处理"},

		{name: "真 CC cli", ua: "claude-cli/2.1.220 (external, cli)", want: false},
		{name: "真 CC vscode", ua: "claude-cli/2.1.220 (external, claude-vscode, agent-sdk/0.3.220)", want: false},
		{name: "真 CC desktop", ua: "claude-cli/2.1.219 (external, claude-desktop-3p, agent-sdk/0.3.219)", want: false},
		{name: "真 CC local-agent", ua: "claude-cli/2.1.181 (external, local-agent, agent-sdk/0.3.219)", want: false},
		{name: "旧版本 CC", ua: "claude-cli/2.1.153 (external, cli)", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNonClaudeCodeUserAgent(tt.ua); got != tt.want {
				t.Errorf("isNonClaudeCodeUserAgent(%q) = %v, want %v %s", tt.ua, got, tt.want, tt.reason)
			}
		})
	}
}
