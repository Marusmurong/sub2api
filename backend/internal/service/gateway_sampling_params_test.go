package service

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestStripDeprecatedSamplingParams(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		model       string
		wantChanged bool
		wantAbsent  []string
		wantPresent []string
	}{
		{
			name:        "新世代模型剥离全部采样参数",
			body:        `{"model":"claude-sonnet-5","max_tokens":1024,"temperature":0.7,"top_p":0.9,"top_k":40}`,
			model:       "claude-sonnet-5",
			wantChanged: true,
			wantAbsent:  []string{"temperature", "top_p", "top_k"},
			wantPresent: []string{"model", "max_tokens"},
		},
		{
			name:        "老模型原样保留",
			body:        `{"model":"claude-opus-4-5-20251101","temperature":0.7,"top_p":0.9}`,
			model:       "claude-opus-4-5-20251101",
			wantChanged: false,
			wantPresent: []string{"temperature", "top_p"},
		},
		{
			name:        "新世代模型但 body 本就没有采样参数",
			body:        `{"model":"claude-opus-4-8","max_tokens":1024}`,
			model:       "claude-opus-4-8",
			wantChanged: false,
			wantPresent: []string{"model", "max_tokens"},
		},
		{
			name:        "只带 temperature 时只删 temperature",
			body:        `{"model":"claude-opus-4-7","temperature":1}`,
			model:       "claude-opus-4-7",
			wantChanged: true,
			wantAbsent:  []string{"temperature"},
		},
		{
			name:        "model 参数为空时回退读 body.model",
			body:        `{"model":"claude-fable-5","temperature":0.5}`,
			model:       "",
			wantChanged: true,
			wantAbsent:  []string{"temperature"},
		},
		{
			name:        "model 参数为空且 body.model 是老模型时保留",
			body:        `{"model":"claude-sonnet-4-5-20250929","temperature":0.5}`,
			model:       "",
			wantChanged: false,
			wantPresent: []string{"temperature"},
		},
		{
			name:        "空 body 不炸",
			body:        ``,
			model:       "claude-sonnet-5",
			wantChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			original := []byte(tt.body)

			// Act
			got, changed := stripDeprecatedSamplingParams(original, tt.model)

			// Assert
			if changed != tt.wantChanged {
				t.Fatalf("changed = %v, want %v (body=%s)", changed, tt.wantChanged, got)
			}
			for _, field := range tt.wantAbsent {
				if gjson.GetBytes(got, field).Exists() {
					t.Errorf("field %q should have been stripped, body=%s", field, got)
				}
			}
			for _, field := range tt.wantPresent {
				if !gjson.GetBytes(got, field).Exists() {
					t.Errorf("field %q should have been preserved, body=%s", field, got)
				}
			}
			// 不得就地改写调用方持有的 body
			if tt.wantChanged && string(original) != tt.body {
				t.Errorf("original body was mutated in place: %s", original)
			}
		})
	}
}

func TestForceStripSamplingParams(t *testing.T) {
	t.Run("无视模型一律剥离", func(t *testing.T) {
		// Arrange: 老模型 body，前置判定本会保留
		body := []byte(`{"model":"claude-opus-4-5-20251101","temperature":0.7,"top_k":40}`)

		// Act
		got, changed := forceStripSamplingParams(body)

		// Assert
		if !changed {
			t.Fatalf("changed = false, want true")
		}
		for _, field := range []string{"temperature", "top_k"} {
			if gjson.GetBytes(got, field).Exists() {
				t.Errorf("field %q should have been stripped, body=%s", field, got)
			}
		}
		if !gjson.GetBytes(got, "model").Exists() {
			t.Errorf("model should have been preserved, body=%s", got)
		}
	})

	t.Run("没有采样参数时返回 changed=false", func(t *testing.T) {
		body := []byte(`{"model":"claude-sonnet-5","max_tokens":8}`)

		_, changed := forceStripSamplingParams(body)

		if changed {
			t.Fatalf("changed = true, want false")
		}
	})

	t.Run("非法 JSON 不 panic 且不误报", func(t *testing.T) {
		body := []byte(`not json`)

		_, changed := forceStripSamplingParams(body)

		if changed {
			t.Fatalf("changed = true, want false")
		}
	})
}
