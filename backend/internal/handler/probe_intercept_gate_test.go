package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/adminprobe"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// 这些用例覆盖的是"接入点"而非检测函数：拦截器是否真的按开关生效、探针与问候
// 的优先级、max_tokens=1 是否正确让行、流式/非流式是否走对分支。
// 检测逻辑本身在 probe_intercept_test.go 中单独验证。

func newInterceptTestHandler(probeTools, greeting bool) *GatewayHandler {
	cfg := &config.Config{}
	cfg.Gateway.InterceptProbeTools = probeTools
	cfg.Gateway.InterceptGreeting = greeting
	return &GatewayHandler{cfg: cfg}
}

func newInterceptTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	return c, rec
}

func TestInterceptNonUpstreamRequest_ProbeTool(t *testing.T) {
	// Arrange
	h := newInterceptTestHandler(true, true)
	c, rec := newInterceptTestContext()
	body := []byte(`{"model":"claude-opus-4-8","max_tokens":1024,
		"tools":[{"type":"web_search_20250305"},{"type":"zzz_source_probe_00000000","name":"p"}],
		"messages":[{"role":"user","content":"hi"}]}`)

	// Act
	intercepted := h.interceptNonUpstreamRequest(c, body, "claude-opus-4-8", 1024, false, false, zap.NewNop())

	// Assert
	if !intercepted {
		t.Fatalf("intercepted = false, want true")
	}
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}

	var resp struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v (%s)", err, rec.Body.String())
	}
	// 必须与官方 Anthropic 形态一致,而不是后台规则那种 upstream_error
	if resp.Error.Type != "invalid_request_error" {
		t.Errorf("error.type = %q, want invalid_request_error", resp.Error.Type)
	}
	// 必须回显对方实际发送的 tag 名与下标
	if !strings.Contains(resp.Error.Message, "zzz_source_probe_00000000") {
		t.Errorf("message must echo the probe tag: %q", resp.Error.Message)
	}
	if !strings.HasPrefix(resp.Error.Message, "tools.1:") {
		t.Errorf("message must carry the real index (tools.1): %q", resp.Error.Message)
	}
	if !strings.HasPrefix(resp.RequestID, "req_") {
		t.Errorf("request_id = %q, want req_ prefix", resp.RequestID)
	}

	// 必须标记为本地策略拒绝：这个 400 是我们有意返回的伪装响应，功能正常。
	// 不标记的话，运维错误率会把每一次成功的反探测都记成一次故障——2026-08-03
	// 当天 158 次，占当日计入错误率总量的 10%，真实故障被噪声淹没。
	if !service.HasOpsClientBusinessLimited(c) {
		t.Error("探针拦截未标记 business_limited —— 会污染运维错误率")
	}
}

func TestInterceptNonUpstreamRequest_Greeting(t *testing.T) {
	t.Run("非流式返回完整消息", func(t *testing.T) {
		h := newInterceptTestHandler(true, true)
		c, rec := newInterceptTestContext()
		body := []byte(`{"model":"claude-opus-4-8","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)

		intercepted := h.interceptNonUpstreamRequest(c, body, "claude-opus-4-8", 1024, false, false, zap.NewNop())

		if !intercepted {
			t.Fatalf("intercepted = false, want true")
		}
		if rec.Code != 200 {
			t.Errorf("status = %d, want 200", rec.Code)
		}
		var resp struct {
			Type       string `json:"type"`
			Role       string `json:"role"`
			Model      string `json:"model"`
			StopReason string `json:"stop_reason"`
			Content    []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON: %v (%s)", err, rec.Body.String())
		}
		if resp.Type != "message" || resp.Role != "assistant" {
			t.Errorf("unexpected envelope: %+v", resp)
		}
		if resp.Model != "claude-opus-4-8" {
			t.Errorf("model = %q, want the requested model", resp.Model)
		}
		if resp.StopReason != "end_turn" {
			t.Errorf("stop_reason = %q, want end_turn", resp.StopReason)
		}
		if len(resp.Content) != 1 || resp.Content[0].Text == "" {
			t.Errorf("content must carry a non-empty greeting: %+v", resp.Content)
		}
	})

	t.Run("流式返回 SSE 事件序列", func(t *testing.T) {
		h := newInterceptTestHandler(true, true)
		c, rec := newInterceptTestContext()
		body := []byte(`{"model":"claude-opus-4-8","max_tokens":1024,"messages":[{"role":"user","content":"你好"}]}`)

		intercepted := h.interceptNonUpstreamRequest(c, body, "claude-opus-4-8", 1024, true, false, zap.NewNop())

		if !intercepted {
			t.Fatalf("intercepted = false, want true")
		}
		out := rec.Body.String()
		for _, want := range []string{
			"event: message_start",
			"event: content_block_start",
			"event: content_block_delta",
			"event: content_block_stop",
			"event: message_delta",
			"event: message_stop",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("SSE output missing %q:\n%s", want, out)
			}
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
			t.Errorf("Content-Type = %q, want text/event-stream", ct)
		}
	})
}

func TestInterceptNonUpstreamRequest_LetThrough(t *testing.T) {
	tests := []struct {
		name       string
		probeTools bool
		greeting   bool
		body       string
		maxTokens  int
		reason     string
	}{
		{
			name: "带 tools 的真实会话不拦", probeTools: true, greeting: true,
			body:      `{"tools":[{"name":"Read"}],"messages":[{"role":"user","content":"hi"}]}`,
			maxTokens: 1024, reason: "Claude Code 会话必带工具",
		},
		{
			name: "多轮历史不拦", probeTools: true, greeting: true,
			body:      `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"},{"role":"user","content":"hi"}]}`,
			maxTokens: 1024, reason: "真实对话",
		},
		{
			// 分层拦截后：带 session 才算真实会话，此时带 system 的问候放行。
			// 无 session 的同形请求属于脚本测活，会被宽松档拦下（见下方 Tier 用例）。
			name: "有 session + 带实质 system 不拦", probeTools: true, greeting: true,
			body: `{"metadata":{"user_id":"u_x_account__session_9f8e7d6c-1111-2222-3333-444455556666"},` +
				`"system":"You are a coding assistant","messages":[{"role":"user","content":"hi"}]}`,
			maxTokens: 1024, reason: "真实 Claude Code 会话",
		},
		{
			name: "max_tokens=1 让行给既有 haiku 探测分支", probeTools: true, greeting: true,
			body:      `{"messages":[{"role":"user","content":"hi"}]}`,
			maxTokens: 1, reason: "客户端期待 stop_reason=max_tokens",
		},
		{
			name: "问候开关关闭时不拦", probeTools: true, greeting: false,
			body:      `{"messages":[{"role":"user","content":"hi"}]}`,
			maxTokens: 1024, reason: "配置开关",
		},
		{
			name: "探针开关关闭时不拦探针", probeTools: false, greeting: false,
			body:      `{"tools":[{"type":"zzz_source_probe_00000000"}],"messages":[{"role":"user","content":"x"}]}`,
			maxTokens: 1024, reason: "配置开关",
		},
		{
			name: "真实工具类型不拦", probeTools: true, greeting: true,
			body:      `{"tools":[{"type":"computer_20250124"}],"messages":[{"role":"user","content":"do it"}]}`,
			maxTokens: 1024, reason: "computer_* 是真实类型",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newInterceptTestHandler(tt.probeTools, tt.greeting)
			c, rec := newInterceptTestContext()

			intercepted := h.interceptNonUpstreamRequest(c, []byte(tt.body), "claude-opus-4-8", tt.maxTokens, false, false, zap.NewNop())

			if intercepted {
				t.Errorf("intercepted = true, want false (%s); body written: %s", tt.reason, rec.Body.String())
			}
			if rec.Body.Len() != 0 {
				t.Errorf("let-through path must not write a response: %s", rec.Body.String())
			}
		})
	}
}

// 分层拦截的接入点验证：UA 自称探针、以及无 session 的脚本测活。
func TestInterceptNonUpstreamRequest_LivenessTiers(t *testing.T) {
	tests := []struct {
		name      string
		userAgent string
		body      string
		want      bool
		reason    string
	}{
		{
			name:      "UA 自称探针即使内容是实质提问也拦",
			userAgent: "openox-claude-emergency-full-capability-probe",
			body:      `{"messages":[{"role":"user","content":"帮我写一个快排"}]}`,
			want:      true, reason: "最强信号",
		},
		{
			name:      "无 session + 大 system + hi（Go-http-client 形态）",
			userAgent: "Go-http-client/2.0",
			body:      `{"system":"You are Claude Code with a very long prompt","messages":[{"role":"user","content":"hi"}]}`,
			want:      true, reason: "宽松档",
		},
		{
			name:      "无 session + test",
			userAgent: "GatewayClient/1.0",
			body:      `{"messages":[{"role":"user","content":"test"}]}`,
			want:      true, reason: "宽松档扩展文案",
		},
		{
			name:      "有 session + 大 system + hi → 放行",
			userAgent: "claude-cli/2.1.220 (external, cli)",
			body: `{"metadata":{"user_id":"u_x_account__session_9f8e7d6c-1111-2222-3333-444455556666"},` +
				`"system":"You are Claude Code","messages":[{"role":"user","content":"hi"}]}`,
			want: false, reason: "严格档：真实 CLI 会话",
		},
		{
			name:      "无 session 但内容是实质提问 → 放行",
			userAgent: "Go-http-client/2.0",
			body:      `{"messages":[{"role":"user","content":"解释一下 CAP 定理"}]}`,
			want:      false, reason: "非测活文案",
		},
		{
			name:      "无 session 但带 tools → 放行",
			userAgent: "Go-http-client/2.0",
			body:      `{"tools":[{"name":"Read"}],"messages":[{"role":"user","content":"hi"}]}`,
			want:      false, reason: "带工具是真实会话",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newInterceptTestHandler(true, true)
			c, rec := newInterceptTestContext()
			c.Request.Header.Set("User-Agent", tt.userAgent)

			got := h.interceptNonUpstreamRequest(c, []byte(tt.body), "claude-opus-4-8", 1024, false, false, zap.NewNop())

			if got != tt.want {
				t.Errorf("intercepted = %v, want %v (%s); body: %s", got, tt.want, tt.reason, rec.Body.String())
			}
		})
	}
}

func TestInterceptNonUpstreamRequest_ProbeTakesPrecedence(t *testing.T) {
	// 同时命中探针与问候时,应返回探针的 400 而不是问候的 200。
	h := newInterceptTestHandler(true, true)
	c, rec := newInterceptTestContext()
	body := []byte(`{"tools":[{"type":"zzz_probe_00000000"}],"messages":[{"role":"user","content":"hi"}]}`)

	intercepted := h.interceptNonUpstreamRequest(c, body, "claude-opus-4-8", 1024, false, false, zap.NewNop())

	if !intercepted {
		t.Fatalf("intercepted = false, want true")
	}
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400 (probe must win over greeting)", rec.Code)
	}
}

func TestInterceptNonUpstreamRequest_NilConfig(t *testing.T) {
	// cfg 为 nil 时必须安全让行,不能 panic。
	h := &GatewayHandler{}
	c, _ := newInterceptTestContext()

	if h.interceptNonUpstreamRequest(c, []byte(`{"messages":[{"role":"user","content":"hi"}]}`), "m", 1024, false, false, zap.NewNop()) {
		t.Errorf("intercepted = true, want false when cfg is nil")
	}
}

func TestInterceptNonUpstreamRequest_AdminProbeSkipsLiveness(t *testing.T) {
	// Admin UI 账号测试 / 渠道监控出站会带密钥头：HI 必须穿透，才能测真实账号。
	h := newInterceptTestHandler(true, true)
	c, rec := newInterceptTestContext()
	adminprobe.Apply(c.Request.Header)
	body := []byte(`{"model":"claude-opus-4-8","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)

	if h.interceptNonUpstreamRequest(c, body, "claude-opus-4-8", 1024, false, false, zap.NewNop()) {
		t.Fatalf("admin probe hi must not be intercepted; body=%s", rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("let-through must not write a response: %s", rec.Body.String())
	}
}

func TestInterceptNonUpstreamRequest_AdminAccountTestBodySkipsLiveness(t *testing.T) {
	// Admin「测试连接」固定探测句：即使密钥头丢失（自环/代理），也不能当 HI 拦掉。
	h := newInterceptTestHandler(true, true)
	c, rec := newInterceptTestContext()
	body := []byte(`{"model":"claude-opus-4-8","max_tokens":1024,"system":[{"type":"text","text":"You are Claude Code"}],"messages":[{"role":"user","content":[{"type":"text","text":"` + adminprobe.ConnectivityTestUserText + `"}]}]}`)

	if h.interceptNonUpstreamRequest(c, body, "claude-opus-4-8", 1024, false, false, zap.NewNop()) {
		t.Fatalf("account test body must not be intercepted; body=%s", rec.Body.String())
	}
}

func TestInterceptNonUpstreamRequest_SpoofedAdminProbeHeaderStillIntercepts(t *testing.T) {
	// 固定值 / 错误 token 不能绕过拦截，否则外部 HI 探针加个头就穿透。
	h := newInterceptTestHandler(true, true)
	c, rec := newInterceptTestContext()
	c.Request.Header.Set(adminprobe.HeaderName, "1")
	body := []byte(`{"model":"claude-opus-4-8","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)

	if !h.interceptNonUpstreamRequest(c, body, "claude-opus-4-8", 1024, false, false, zap.NewNop()) {
		t.Fatal("spoofed admin-probe header must still intercept hi")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 greeting intercept", rec.Code)
	}
}

func TestInterceptNonUpstreamRequest_AdminProbeDoesNotSkipProbeTools(t *testing.T) {
	// 旁路只针对测活问候，人造 zzz 探针工具仍拦截。
	h := newInterceptTestHandler(true, true)
	c, rec := newInterceptTestContext()
	adminprobe.Apply(c.Request.Header)
	body := []byte(`{"tools":[{"type":"zzz_probe_00000000"}],"messages":[{"role":"user","content":"hi"}]}`)

	if !h.interceptNonUpstreamRequest(c, body, "claude-opus-4-8", 1024, false, false, zap.NewNop()) {
		t.Fatal("probe tools must still intercept even with admin-probe marker")
	}
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// 预热类拦截（Warmup / SUGGESTION MODE / max_tokens=1 haiku 探测）上移到账号选择
// 之前后的接入点验证。这些请求原本也不发上游，但会先排并发队列、占账号槽，
// 槽位打满时甚至先拿到 503；现在应恒定秒回。
func TestInterceptNonUpstreamRequest_WarmupTier(t *testing.T) {
	newWarmupHandler := func(warmup bool) *GatewayHandler {
		cfg := &config.Config{}
		cfg.Gateway.InterceptWarmup = warmup
		cfg.Gateway.InterceptProbeTools = true
		cfg.Gateway.InterceptGreeting = true
		return &GatewayHandler{cfg: cfg}
	}

	t.Run("max_tokens=1 + haiku 返回 max_tokens 截断形态", func(t *testing.T) {
		h := newWarmupHandler(true)
		c, rec := newInterceptTestContext()
		body := []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)

		intercepted := h.interceptNonUpstreamRequest(c, body, "claude-haiku-4-5-20251001", 1, false, true, zap.NewNop())

		if !intercepted {
			t.Fatalf("intercepted = false, want true")
		}
		var resp struct {
			StopReason string `json:"stop_reason"`
			Content    []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON: %v (%s)", err, rec.Body.String())
		}
		// 客户端据此判定连通性，形态不能变成 end_turn
		if resp.StopReason != "max_tokens" {
			t.Errorf("stop_reason = %q, want max_tokens", resp.StopReason)
		}
		if len(resp.Content) != 1 || resp.Content[0].Text != "#" {
			t.Errorf("content = %+v, want single \"#\" block", resp.Content)
		}
	})

	t.Run("Warmup 请求被拦", func(t *testing.T) {
		h := newWarmupHandler(true)
		c, rec := newInterceptTestContext()
		body := []byte(`{"model":"claude-opus-4-8","max_tokens":512,"messages":[{"role":"user","content":[{"type":"text","text":"Warmup"}]}]}`)

		if !h.interceptNonUpstreamRequest(c, body, "claude-opus-4-8", 512, false, true, zap.NewNop()) {
			t.Fatalf("intercepted = false, want true; body: %s", rec.Body.String())
		}
	})

	t.Run("开关关闭时 max_tokens=1 让行而非落到问候拦截", func(t *testing.T) {
		h := newWarmupHandler(false)
		c, rec := newInterceptTestContext()
		body := []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)

		if h.interceptNonUpstreamRequest(c, body, "claude-haiku-4-5-20251001", 1, false, true, zap.NewNop()) {
			t.Errorf("intercepted = true, want false; body: %s", rec.Body.String())
		}
		if rec.Body.Len() != 0 {
			t.Errorf("let-through must not write a response: %s", rec.Body.String())
		}
	})
}
