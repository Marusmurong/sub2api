package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
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
	intercepted := h.interceptNonUpstreamRequest(c, body, "claude-opus-4-8", 1024, false, zap.NewNop())

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
}

func TestInterceptNonUpstreamRequest_Greeting(t *testing.T) {
	t.Run("非流式返回完整消息", func(t *testing.T) {
		h := newInterceptTestHandler(true, true)
		c, rec := newInterceptTestContext()
		body := []byte(`{"model":"claude-opus-4-8","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)

		intercepted := h.interceptNonUpstreamRequest(c, body, "claude-opus-4-8", 1024, false, zap.NewNop())

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

		intercepted := h.interceptNonUpstreamRequest(c, body, "claude-opus-4-8", 1024, true, zap.NewNop())

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

			intercepted := h.interceptNonUpstreamRequest(c, []byte(tt.body), "claude-opus-4-8", tt.maxTokens, false, zap.NewNop())

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

			got := h.interceptNonUpstreamRequest(c, []byte(tt.body), "claude-opus-4-8", 1024, false, zap.NewNop())

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

	intercepted := h.interceptNonUpstreamRequest(c, body, "claude-opus-4-8", 1024, false, zap.NewNop())

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

	if h.interceptNonUpstreamRequest(c, []byte(`{"messages":[{"role":"user","content":"hi"}]}`), "m", 1024, false, zap.NewNop()) {
		t.Errorf("intercepted = true, want false when cfg is nil")
	}
}
