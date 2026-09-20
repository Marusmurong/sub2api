//go:build unit

package handler

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const realDeviceID = "464da8518a6cb7f7c307c0bef761fd9041887457d8a2f466a818f59934c43005"

func forgedDeviceConfig(mode string) *config.Config {
	cfg := &config.Config{}
	cfg.Gateway.RepeatPayloadGuard = config.RepeatPayloadGuardConfig{
		Mode:         config.RepeatPayloadGuardModeOff,
		SmallProbe:   config.RepeatSmallProbeConfig{Mode: config.RepeatPayloadGuardModeOff, MaxBodyBytes: 4096},
		ForgedDevice: config.RepeatForgedDeviceConfig{Mode: mode},
	}
	return cfg
}

// probeBody 模拟 2026-09-21 线上抓到的巡检：CC 外衣、device_id 任意、单条 user、无 tools。
func probeBody(t *testing.T, userID string, stream bool, extra string) (*service.ParsedRequest, []byte) {
	t.Helper()
	body := []byte(fmt.Sprintf(`{"model":"claude-opus-5","max_tokens":64,"stream":%t,`+
		`"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],`+
		`"metadata":{"user_id":%q},%s`+
		`"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
		stream, userID, extra))
	parsed, err := service.ParseGatewayRequest(service.NewRequestBodyRef(body), "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	return parsed, body
}

func jsonUserID(device string) string {
	return fmt.Sprintf(`{"device_id":%q,"account_uuid":"","session_id":"066ec8ac-fad2-4ffb-b324-23d07e066e80"}`, device)
}

func TestForgedDeviceProbe_BlocksFakeDeviceIDWithGreeting(t *testing.T) {
	h := &GatewayHandler{cfg: forgedDeviceConfig(config.RepeatPayloadGuardModeBlock)}
	for _, tc := range []struct{ name, userID string }{
		{"json_device_1", jsonUserID("1")},
		{"json_device_short_hex", jsonUserID("abc")},
		{"json_device_63_hex", jsonUserID(realDeviceID[:63])},
		{"unparseable", "not-a-user-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, body := probeBody(t, tc.userID, false, "")
			c, rec := newGuardContext()
			if !h.interceptForgedDeviceProbe(c, parsed, body, "claude-opus-5", false, 100, nil) {
				t.Fatal("应被拦截")
			}
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"stop_reason":"end_turn"`) ||
				!strings.Contains(rec.Body.String(), `"claude-opus-5"`) {
				t.Fatalf("应回官方 message 形态的问候, got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestForgedDeviceProbe_StreamReturnsSSE(t *testing.T) {
	h := &GatewayHandler{cfg: forgedDeviceConfig(config.RepeatPayloadGuardModeBlock)}
	parsed, body := probeBody(t, jsonUserID("1"), true, "")
	c, rec := newGuardContext()
	if !h.interceptForgedDeviceProbe(c, parsed, body, "claude-opus-5", true, 100, nil) {
		t.Fatal("应被拦截")
	}
	out := rec.Body.String()
	for _, want := range []string{"event: message_start", "event: content_block_delta", "event: message_stop"} {
		if !strings.Contains(out, want) {
			t.Fatalf("流式响应缺 %s: %s", want, out)
		}
	}
}

// 真实 Claude Code：64 位 hex 设备 ID，无论 JSON 还是 legacy 格式，都不拦。
func TestForgedDeviceProbe_PassesRealDeviceIDs(t *testing.T) {
	h := &GatewayHandler{cfg: forgedDeviceConfig(config.RepeatPayloadGuardModeBlock)}
	legacy := "user_" + realDeviceID + "_account__session_066ec8ac-fad2-4ffb-b324-23d07e066e80"
	for _, tc := range []struct{ name, userID string }{
		{"json_real", jsonUserID(realDeviceID)},
		{"json_real_uppercase", jsonUserID(strings.ToUpper(realDeviceID))},
		{"legacy_real", legacy},
		{"empty_metadata", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, body := probeBody(t, tc.userID, false, "")
			c, _ := newGuardContext()
			if h.interceptForgedDeviceProbe(c, parsed, body, "claude-opus-5", false, 100, nil) {
				t.Fatal("合法或缺省设备 ID 不应被拦")
			}
		})
	}
}

// 形态门槛：带 tools / 多轮 / 超体积的请求即使设备 ID 伪造也不在这里管。
func TestForgedDeviceProbe_ShapeGates(t *testing.T) {
	h := &GatewayHandler{cfg: forgedDeviceConfig(config.RepeatPayloadGuardModeBlock)}

	t.Run("with_tools", func(t *testing.T) {
		parsed, body := probeBody(t, jsonUserID("1"), false, `"tools":[{"name":"Bash","input_schema":{"type":"object"}}],`)
		c, _ := newGuardContext()
		if h.interceptForgedDeviceProbe(c, parsed, body, "claude-opus-5", false, 100, nil) {
			t.Fatal("带 tools 不应被拦")
		}
	})
	t.Run("above_max_body", func(t *testing.T) {
		cfg := forgedDeviceConfig(config.RepeatPayloadGuardModeBlock)
		cfg.Gateway.RepeatPayloadGuard.SmallProbe.MaxBodyBytes = 64
		hh := &GatewayHandler{cfg: cfg}
		parsed, body := probeBody(t, jsonUserID("1"), false, "")
		c, _ := newGuardContext()
		if hh.interceptForgedDeviceProbe(c, parsed, body, "claude-opus-5", false, 100, nil) {
			t.Fatal("超体积不应被拦")
		}
	})
}

func TestForgedDeviceProbe_Modes(t *testing.T) {
	for _, mode := range []string{config.RepeatPayloadGuardModeOff, config.RepeatPayloadGuardModeObserve} {
		t.Run(mode, func(t *testing.T) {
			h := &GatewayHandler{cfg: forgedDeviceConfig(mode)}
			parsed, body := probeBody(t, jsonUserID("1"), false, "")
			c, rec := newGuardContext()
			if h.interceptForgedDeviceProbe(c, parsed, body, "claude-opus-5", false, 100, nil) {
				t.Fatalf("%s 模式不应写响应", mode)
			}
			if rec.Body.Len() != 0 {
				t.Fatalf("%s 模式不应有响应体", mode)
			}
		})
	}
}

func TestForgedDeviceProbe_NilSafety(t *testing.T) {
	var h *GatewayHandler
	if h.interceptForgedDeviceProbe(nil, nil, nil, "", false, 0, nil) {
		t.Fatal("nil handler 应放行")
	}
	hh := &GatewayHandler{}
	if hh.interceptForgedDeviceProbe(nil, nil, nil, "", false, 0, nil) {
		t.Fatal("nil cfg 应放行")
	}
}
