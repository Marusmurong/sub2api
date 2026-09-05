//go:build unit

package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func smallProbeConfig(mode string) *config.Config {
	cfg := &config.Config{}
	cfg.Gateway.RepeatPayloadGuard = config.RepeatPayloadGuardConfig{
		Mode:                 config.RepeatPayloadGuardModeOff, // 大请求那套关掉，证明两者独立
		MinBodyBytes:         200000,
		WindowMinutes:        30,
		MessagesThreshold:    8,
		CountTokensThreshold: 40,
		SmallProbe: config.RepeatSmallProbeConfig{
			Mode:         mode,
			MaxBodyBytes: 4096,
			Threshold:    3,
		},
	}
	return cfg
}

// pingBody 模拟线上抓到的中转探活：带 claude-cli 风格 system 与 session，单条 user，
// 无 tools，500 字节量级。suffix 用于制造不同指纹。
func pingBody(t *testing.T, suffix string, stream bool) (*service.ParsedRequest, []byte) {
	t.Helper()
	body := []byte(fmt.Sprintf(`{"model":"claude-opus-4-7","max_tokens":64,"stream":%t,`+
		`"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],`+
		`"metadata":{"user_id":"{\"device_id\":\"abc\",\"session_id\":\"066ec8ac-fad2-4ffb-b324-23d07e066e80\"}"},`+
		`"messages":[{"role":"user","content":[{"type":"text","text":"Reply with the single word ready.%s"}]}]}`,
		stream, suffix))
	parsed, err := service.ParseGatewayRequest(service.NewRequestBodyRef(body), "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	return parsed, body
}

// 阈值之内照常放行（真发上游），越过阈值后本地回一句问候而不是报错——
// 对方是在测活，回错误它会把我们标为不可用；回正常问候它继续用我们，但不再花上游。
func TestInterceptRepeatSmallProbe_RepliesGreetingAfterThreshold(t *testing.T) {
	cache := newFakeRepeatPayloadCache()
	h := &GatewayHandler{cfg: smallProbeConfig(config.RepeatPayloadGuardModeBlock), repeatPayloadCache: cache}
	parsed, body := pingBody(t, "", true)

	for i := 1; i <= 3; i++ {
		c, rec := newGuardContext()
		if h.interceptRepeatSmallProbe(c, parsed, body, "claude-opus-4-7", true, 63, nil) {
			t.Fatalf("第 %d 次不应被拦（阈值 3）", i)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("第 %d 次不应写出响应", i)
		}
	}

	c, rec := newGuardContext()
	if !h.interceptRepeatSmallProbe(c, parsed, body, "claude-opus-4-7", true, 63, nil) {
		t.Fatal("第 4 次应被拦")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("拦截应伪装成正常 200，实际 %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "event: message_start") || !strings.Contains(out, "Hello! How can I help you today?") {
		t.Fatalf("流式请求应回 SSE 问候，实际 %s", out)
	}
	if !strings.Contains(out, `"model":"claude-opus-4-7"`) {
		t.Fatalf("伪造响应应回显请求的 model，实际 %s", out)
	}
}

func TestInterceptRepeatSmallProbe_NonStreamReturnsJSONGreeting(t *testing.T) {
	cache := newFakeRepeatPayloadCache()
	h := &GatewayHandler{cfg: smallProbeConfig(config.RepeatPayloadGuardModeBlock), repeatPayloadCache: cache}
	parsed, body := pingBody(t, "", false)

	for i := 0; i < 3; i++ {
		c, _ := newGuardContext()
		h.interceptRepeatSmallProbe(c, parsed, body, "claude-opus-4-7", false, 63, nil)
	}
	c, rec := newGuardContext()
	if !h.interceptRepeatSmallProbe(c, parsed, body, "claude-opus-4-7", false, 63, nil) {
		t.Fatal("第 4 次应被拦")
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"stop_reason":"end_turn"`) {
		t.Fatalf("非流式应回完整 message JSON，实际 %d %s", rec.Code, rec.Body.String())
	}
}

// 真实 Claude Code 会话必带 tools——带 tools 的小请求不检测、不碰计数器。
func TestInterceptRepeatSmallProbe_SkipsRequestsWithTools(t *testing.T) {
	cache := newFakeRepeatPayloadCache()
	h := &GatewayHandler{cfg: smallProbeConfig(config.RepeatPayloadGuardModeBlock), repeatPayloadCache: cache}
	body := []byte(`{"model":"claude-opus-4-7","tools":[{"name":"Bash","input_schema":{"type":"object"}}],` +
		`"messages":[{"role":"user","content":"hi"}]}`)
	parsed, err := service.ParseGatewayRequest(service.NewRequestBodyRef(body), "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	for i := 0; i < 20; i++ {
		c, _ := newGuardContext()
		if h.interceptRepeatSmallProbe(c, parsed, body, "claude-opus-4-7", true, 63, nil) {
			t.Fatalf("带 tools 的请求第 %d 次被拦", i+1)
		}
	}
	if cache.callCount() != 0 {
		t.Fatalf("带 tools 的请求不应访问计数器，实际 %d 次", cache.callCount())
	}
}

// 多轮对话（messages > 1）不是探活形态，不检测。
func TestInterceptRepeatSmallProbe_SkipsMultiTurn(t *testing.T) {
	cache := newFakeRepeatPayloadCache()
	h := &GatewayHandler{cfg: smallProbeConfig(config.RepeatPayloadGuardModeBlock), repeatPayloadCache: cache}
	body := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"a"},` +
		`{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`)
	parsed, err := service.ParseGatewayRequest(service.NewRequestBodyRef(body), "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	for i := 0; i < 20; i++ {
		c, _ := newGuardContext()
		if h.interceptRepeatSmallProbe(c, parsed, body, "claude-opus-4-7", true, 63, nil) {
			t.Fatalf("多轮请求第 %d 次被拦", i+1)
		}
	}
	if cache.callCount() != 0 {
		t.Fatalf("多轮请求不应访问计数器，实际 %d 次", cache.callCount())
	}
}

// 超过体积上限的请求归大请求那套管，这里不碰。
func TestInterceptRepeatSmallProbe_SkipsBodiesAboveLimit(t *testing.T) {
	cache := newFakeRepeatPayloadCache()
	h := &GatewayHandler{cfg: smallProbeConfig(config.RepeatPayloadGuardModeBlock), repeatPayloadCache: cache}
	parsed := largeBody(t, "") // 4KB+ 单条 user、无 tools
	for i := 0; i < 20; i++ {
		c, _ := newGuardContext()
		if h.interceptRepeatSmallProbe(c, parsed, parsed.Body.Bytes(), "claude-fable-5", true, 63, nil) {
			t.Fatalf("大请求第 %d 次被小探针拦截", i+1)
		}
	}
	if cache.callCount() != 0 {
		t.Fatalf("大请求不应进小探针计数器，实际 %d 次", cache.callCount())
	}
}

// 不同指纹、不同 key 各自计数。
func TestInterceptRepeatSmallProbe_Isolation(t *testing.T) {
	cache := newFakeRepeatPayloadCache()
	h := &GatewayHandler{cfg: smallProbeConfig(config.RepeatPayloadGuardModeBlock), repeatPayloadCache: cache}

	for i := 0; i < 10; i++ {
		c, _ := newGuardContext()
		parsed, body := pingBody(t, fmt.Sprintf("-%d", i), true)
		if h.interceptRepeatSmallProbe(c, parsed, body, "claude-opus-4-7", true, 63, nil) {
			t.Fatalf("第 %d 份不同内容不应被拦", i+1)
		}
	}
	parsed, body := pingBody(t, "", true)
	for i := 0; i < 4; i++ {
		c, _ := newGuardContext()
		h.interceptRepeatSmallProbe(c, parsed, body, "claude-opus-4-7", true, 63, nil)
	}
	c, _ := newGuardContext()
	if h.interceptRepeatSmallProbe(c, parsed, body, "claude-opus-4-7", true, 64, nil) {
		t.Fatal("另一个 api_key 不应受影响")
	}
}

// 与大请求那套共用 Redis 但命名空间独立：small 计数不得污染 msg 计数，反之亦然。
func TestInterceptRepeatSmallProbe_ScopeIsolatedFromLargeGuard(t *testing.T) {
	cache := newFakeRepeatPayloadCache()
	cfg := smallProbeConfig(config.RepeatPayloadGuardModeBlock)
	cfg.Gateway.RepeatPayloadGuard.Mode = config.RepeatPayloadGuardModeBlock
	cfg.Gateway.RepeatPayloadGuard.MinBodyBytes = 1024
	cfg.Gateway.RepeatPayloadGuard.MessagesThreshold = 3
	h := &GatewayHandler{cfg: cfg, repeatPayloadCache: cache}
	parsed, body := pingBody(t, "", true)

	// 小探针打到阈值。
	for i := 0; i < 4; i++ {
		c, _ := newGuardContext()
		h.interceptRepeatSmallProbe(c, parsed, body, "claude-opus-4-7", true, 63, nil)
	}
	// 同一指纹在大请求 scope 下应从 0 开始计（何况它体积不够，根本不检测）。
	c, _ := newGuardContext()
	if h.rejectRepeatPayload(c, parsed, service.RepeatPayloadScopeMessages, 63, nil) {
		t.Fatal("小探针计数不得污染大请求 scope")
	}
}

// Redis 故障 / 未注入一律 fail-open。
func TestInterceptRepeatSmallProbe_FailsOpen(t *testing.T) {
	cache := newFakeRepeatPayloadCache()
	cache.err = errors.New("redis down")
	h := &GatewayHandler{cfg: smallProbeConfig(config.RepeatPayloadGuardModeBlock), repeatPayloadCache: cache}
	parsed, body := pingBody(t, "", true)
	for i := 0; i < 20; i++ {
		c, _ := newGuardContext()
		if h.interceptRepeatSmallProbe(c, parsed, body, "claude-opus-4-7", true, 63, nil) {
			t.Fatalf("Redis 故障时第 %d 次被拦，应当 fail-open", i+1)
		}
	}
	h2 := &GatewayHandler{cfg: smallProbeConfig(config.RepeatPayloadGuardModeBlock)}
	c, _ := newGuardContext()
	if h2.interceptRepeatSmallProbe(c, parsed, body, "claude-opus-4-7", true, 63, nil) {
		t.Fatal("未注入计数器时应放行")
	}
}

// 模式开关：off 完全不检测，observe 命中也放行。
func TestInterceptRepeatSmallProbe_Modes(t *testing.T) {
	tests := []struct {
		name        string
		mode        string
		wantBlocked bool
		wantCalls   int
	}{
		{name: "off 不检测", mode: config.RepeatPayloadGuardModeOff, wantBlocked: false, wantCalls: 0},
		{name: "笔误按 off", mode: "observ", wantBlocked: false, wantCalls: 0},
		{name: "observe 命中也放行", mode: config.RepeatPayloadGuardModeObserve, wantBlocked: false, wantCalls: 6},
		{name: "block 命中即回问候", mode: config.RepeatPayloadGuardModeBlock, wantBlocked: true, wantCalls: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := newFakeRepeatPayloadCache()
			h := &GatewayHandler{cfg: smallProbeConfig(tt.mode), repeatPayloadCache: cache}
			parsed, body := pingBody(t, "", true)
			blocked := false
			for i := 0; i < 6; i++ {
				c, _ := newGuardContext()
				if h.interceptRepeatSmallProbe(c, parsed, body, "claude-opus-4-7", true, 63, nil) {
					blocked = true
				}
			}
			if blocked != tt.wantBlocked {
				t.Fatalf("拦截 = %v，期望 %v", blocked, tt.wantBlocked)
			}
			if cache.callCount() != tt.wantCalls {
				t.Fatalf("计数器调用 %d 次，期望 %d", cache.callCount(), tt.wantCalls)
			}
		})
	}
}
