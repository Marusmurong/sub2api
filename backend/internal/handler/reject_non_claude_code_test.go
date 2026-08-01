//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
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

// 拦截只对 Anthropic 生效。
//
// 生产事故（2026-07-31 19:31 启用后立刻发现）：key 82 是 Grok 客户端
// （grok-pager/0.2.117 grok-shell/0.2.117），它的 UA 当然不是 claude-cli，
// 于是被一起拦掉、吞吐直接归零。
//
// 整套理由——Claude Code 伪装做不到位、Anthropic 吊销订阅账号——只对 Anthropic
// OAuth 成立。Grok / OpenAI / Gemini 的客户端本来就不该有 claude-cli 的 UA，
// 拿这把尺子去量它们是纯粹的误伤。
func TestShouldRejectForPlatform(t *testing.T) {
	tests := []struct {
		name     string
		platform string
		want     bool
	}{
		{name: "anthropic 生效", platform: "anthropic", want: true},
		{name: "grok 不生效", platform: "grok", want: false},
		{name: "openai 不生效", platform: "openai", want: false},
		{name: "gemini 不生效", platform: "gemini", want: false},
		{name: "平台未知时不生效", platform: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rejectAppliesToPlatform(tt.platform); got != tt.want {
				t.Errorf("rejectAppliesToPlatform(%q) = %v, want %v", tt.platform, got, tt.want)
			}
		})
	}
}

// 拦截必须打上「本地策略拒绝」标记，否则会以系统错误的形式出现在运维监控页。
//
// 这是策略性拒绝，不是故障：把它计进错误率，运维面板上就永远挂着一片红，
// 真正的上游异常反而被淹没。既有的 BetaBlockedError 与 responses 子路径守卫
// 都走的是同一个标记。
func TestRejectMarksOpsBusinessLimited(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "Go-http-client/2.0")

	h := &GatewayHandler{cfg: &config.Config{}}
	h.cfg.Gateway.RejectNonClaudeCodeClients = true

	if !h.rejectNonClaudeCodeClient(c, service.PlatformAnthropic, nil) {
		t.Fatal("应当拦截 Go-http-client")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("状态码 = %d, want 403", w.Code)
	}
	if !service.HasOpsClientBusinessLimited(c) {
		t.Error("必须标记为本地策略拒绝，否则会计进运维监控的错误率")
	}
}

// 放行的请求不得留下该标记。
func TestRejectDoesNotMarkWhenAllowed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.220 (external, cli)")

	h := &GatewayHandler{cfg: &config.Config{}}
	h.cfg.Gateway.RejectNonClaudeCodeClients = true

	if h.rejectNonClaudeCodeClient(c, service.PlatformAnthropic, nil) {
		t.Fatal("真 Claude Code 不应被拦")
	}
	if service.HasOpsClientBusinessLimited(c) {
		t.Error("放行的请求不得留下策略拒绝标记")
	}
}

// claude-code/ 前缀也是官方 Claude Code。
//
// 生产实测（拦截启用后）：UA 为 claude-code/2.1.132 的请求被拦了 124 次。
// 判定用的 claudeCodeUAPattern 只认 ^claude-cli/，而部分版本/构建自称 claude-code/。
// 这是官方客户端被误伤，必须放行。
//
// 只在本拦截器内放宽，不动 ClaudeCodeValidator 的正则：那个正则还被
// group.claude_code_only 与客户端版本闸使用，放宽它会连带改变那两处的语义。
func TestNonClaudeCodeUA_AcceptsClaudeCodePrefix(t *testing.T) {
	accepted := []string{
		"claude-code/2.1.132",
		"claude-code/2.1.220 (external, cli)",
		"Claude-Code/2.1.132", // 大小写不敏感
	}
	for _, ua := range accepted {
		if isNonClaudeCodeUserAgent(ua) {
			t.Errorf("官方客户端被误拦: %q", ua)
		}
	}

	// 反向保护：形似但不是官方客户端的仍要拦。
	rejected := []string{
		"claude-coder/1.0",
		"my-claude-code-proxy/1.0",
		"axonhub/1.0",
		"Go-http-client/2.0",
	}
	for _, ua := range rejected {
		if !isNonClaudeCodeUserAgent(ua) {
			t.Errorf("非官方客户端应被拦: %q", ua)
		}
	}
}
