package service

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestSanitizeAnthropicRequestFields_StreamOptions(t *testing.T) {
	// stream_options 是 OpenAI 字段，Anthropic 一律拒收，与模型无关。
	models := []string{"claude-opus-4-5-20251101", "claude-sonnet-5", "claude-haiku-4-5-20251001"}
	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			body := []byte(`{"model":"` + model + `","stream_options":{"include_usage":true},"max_tokens":16}`)

			got, changed := sanitizeAnthropicRequestFields(body, model)

			if !changed {
				t.Fatalf("changed = false, want true")
			}
			if gjson.GetBytes(got, "stream_options").Exists() {
				t.Errorf("stream_options must be stripped: %s", got)
			}
			if !gjson.GetBytes(got, "max_tokens").Exists() {
				t.Errorf("max_tokens must be preserved: %s", got)
			}
		})
	}
}

func TestSanitizeAnthropicRequestFields_EffortUnsupportedModel(t *testing.T) {
	t.Run("haiku 剥离 output_config.effort", func(t *testing.T) {
		body := []byte(`{"model":"claude-haiku-4-5-20251001","output_config":{"effort":"high"},"max_tokens":16}`)

		got, changed := sanitizeAnthropicRequestFields(body, "claude-haiku-4-5-20251001")

		if !changed {
			t.Fatalf("changed = false, want true")
		}
		if gjson.GetBytes(got, "output_config.effort").Exists() {
			t.Errorf("effort must be stripped: %s", got)
		}
		// output_config 只剩空对象时应整体删除，避免上游对空对象报错
		if gjson.GetBytes(got, "output_config").Exists() {
			t.Errorf("empty output_config must be removed: %s", got)
		}
	})

	t.Run("haiku 但 output_config 还有其他字段时保留容器", func(t *testing.T) {
		body := []byte(`{"model":"claude-haiku-4-5","output_config":{"effort":"high","other":1}}`)

		got, _ := sanitizeAnthropicRequestFields(body, "claude-haiku-4-5")

		if gjson.GetBytes(got, "output_config.effort").Exists() {
			t.Errorf("effort must be stripped: %s", got)
		}
		if !gjson.GetBytes(got, "output_config.other").Exists() {
			t.Errorf("other fields must be preserved: %s", got)
		}
	})

	t.Run("非 haiku 模型保留 effort", func(t *testing.T) {
		body := []byte(`{"model":"claude-opus-5","output_config":{"effort":"high"},"thinking":{"type":"enabled"}}`)

		got, _ := sanitizeAnthropicRequestFields(body, "claude-opus-5")

		if gjson.GetBytes(got, "output_config.effort").String() != "high" {
			t.Errorf("effort must be preserved on non-haiku: %s", got)
		}
	})
}

func TestSanitizeAnthropicRequestFields_AdaptiveThinking(t *testing.T) {
	t.Run("haiku 的 adaptive 转 enabled 并补 budget", func(t *testing.T) {
		body := []byte(`{"model":"claude-haiku-4-5-20251001","thinking":{"type":"adaptive"}}`)

		got, changed := sanitizeAnthropicRequestFields(body, "claude-haiku-4-5-20251001")

		if !changed {
			t.Fatalf("changed = false, want true")
		}
		if gjson.GetBytes(got, "thinking.type").String() != "enabled" {
			t.Errorf("thinking.type should become enabled: %s", got)
		}
		if !gjson.GetBytes(got, "thinking.budget_tokens").Exists() {
			t.Errorf("budget_tokens must be filled in: %s", got)
		}
	})

	t.Run("haiku 已有 budget 时不覆盖", func(t *testing.T) {
		body := []byte(`{"model":"claude-haiku-4-5","thinking":{"type":"adaptive","budget_tokens":2048}}`)

		got, _ := sanitizeAnthropicRequestFields(body, "claude-haiku-4-5")

		if gjson.GetBytes(got, "thinking.budget_tokens").Int() != 2048 {
			t.Errorf("existing budget must be kept: %s", got)
		}
	})

	t.Run("非 haiku 保留 adaptive", func(t *testing.T) {
		body := []byte(`{"model":"claude-opus-4-7","thinking":{"type":"adaptive"}}`)

		got, _ := sanitizeAnthropicRequestFields(body, "claude-opus-4-7")

		if gjson.GetBytes(got, "thinking.type").String() != "adaptive" {
			t.Errorf("adaptive must be preserved on non-haiku: %s", got)
		}
	})
}

func TestSanitizeAnthropicRequestFields_EffortRequiresThinking(t *testing.T) {
	// 上游原话：effort 'max' is not supported when thinking is disabled on this
	// model. Use effort 'high' or below, or enable thinking.
	tests := []struct {
		name       string
		body       string
		wantEffort string
	}{
		{
			name:       "thinking 缺失时 max 降级为 high",
			body:       `{"model":"claude-opus-5","output_config":{"effort":"max"}}`,
			wantEffort: "high",
		},
		{
			name:       "thinking 缺失时 xhigh 降级为 high",
			body:       `{"model":"claude-opus-5","output_config":{"effort":"xhigh"}}`,
			wantEffort: "high",
		},
		{
			name:       "thinking 已启用时保留 max",
			body:       `{"model":"claude-opus-5","output_config":{"effort":"max"},"thinking":{"type":"enabled","budget_tokens":1024}}`,
			wantEffort: "max",
		},
		{
			name:       "thinking 为 adaptive 时保留 max",
			body:       `{"model":"claude-opus-5","output_config":{"effort":"max"},"thinking":{"type":"adaptive"}}`,
			wantEffort: "max",
		},
		{
			name:       "thinking 显式关闭时降级",
			body:       `{"model":"claude-opus-5","output_config":{"effort":"max"},"thinking":{"type":"disabled"}}`,
			wantEffort: "high",
		},
		{
			name:       "high 及以下不受影响",
			body:       `{"model":"claude-opus-5","output_config":{"effort":"high"}}`,
			wantEffort: "high",
		},
		{
			name:       "medium 不受影响",
			body:       `{"model":"claude-opus-5","output_config":{"effort":"medium"}}`,
			wantEffort: "medium",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := sanitizeAnthropicRequestFields([]byte(tt.body), "claude-opus-5")

			if e := gjson.GetBytes(got, "output_config.effort").String(); e != tt.wantEffort {
				t.Errorf("effort = %q, want %q (body=%s)", e, tt.wantEffort, got)
			}
		})
	}
}

func TestSanitizeAnthropicRequestFields_NoOp(t *testing.T) {
	t.Run("干净请求不改动", func(t *testing.T) {
		body := []byte(`{"model":"claude-opus-4-5-20251101","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)

		_, changed := sanitizeAnthropicRequestFields(body, "claude-opus-4-5-20251101")

		if changed {
			t.Errorf("changed = true, want false")
		}
	})

	t.Run("空 body 与非法 JSON 不 panic", func(t *testing.T) {
		if _, changed := sanitizeAnthropicRequestFields(nil, "claude-opus-5"); changed {
			t.Errorf("nil body: changed = true")
		}
		if _, changed := sanitizeAnthropicRequestFields([]byte(`not json`), "claude-opus-5"); changed {
			t.Errorf("invalid json: changed = true")
		}
	})
}
