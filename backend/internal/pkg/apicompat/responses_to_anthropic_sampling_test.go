package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Responses → Anthropic 方向的采样参数处理。
// 目标是 Anthropic 上游：新世代模型只要 temperature 字段存在就 400
// ("`temperature` is deprecated for this model.")，所以转换时必须按模型丢弃。
func TestResponsesToAnthropicRequest_SamplingParams(t *testing.T) {
	temp := 0.7
	topP := 0.9

	t.Run("新世代模型剥离 temperature/top_p", func(t *testing.T) {
		// Arrange
		req := &ResponsesRequest{
			Model:       "claude-sonnet-5",
			Input:       json.RawMessage(`"hi"`),
			Temperature: &temp,
			TopP:        &topP,
		}

		// Act
		out, err := ResponsesToAnthropicRequest(req)

		// Assert
		require.NoError(t, err)
		assert.Nil(t, out.Temperature, "deprecated-sampling model: temperature must be stripped")
		assert.Nil(t, out.TopP, "deprecated-sampling model: top_p must be stripped")

		b, err := json.Marshal(out)
		require.NoError(t, err)
		assert.NotContains(t, string(b), `"temperature"`)
		assert.NotContains(t, string(b), `"top_p"`)
	})

	t.Run("老模型保留 temperature/top_p", func(t *testing.T) {
		// Arrange
		req := &ResponsesRequest{
			Model:       "claude-opus-4-5-20251101",
			Input:       json.RawMessage(`"hi"`),
			Temperature: &temp,
			TopP:        &topP,
		}

		// Act
		out, err := ResponsesToAnthropicRequest(req)

		// Assert
		require.NoError(t, err)
		require.NotNil(t, out.Temperature, "legacy model: temperature must be preserved")
		assert.InDelta(t, 0.7, *out.Temperature, 1e-9)
		require.NotNil(t, out.TopP, "legacy model: top_p must be preserved")
		assert.InDelta(t, 0.9, *out.TopP, 1e-9)
	})

	t.Run("未来新模型默认剥离", func(t *testing.T) {
		req := &ResponsesRequest{
			Model:       "claude-opus-9-9",
			Input:       json.RawMessage(`"hi"`),
			Temperature: &temp,
		}

		out, err := ResponsesToAnthropicRequest(req)

		require.NoError(t, err)
		assert.Nil(t, out.Temperature, "unknown model must default to stripped")
	})
}
