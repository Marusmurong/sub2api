package claude

import "testing"

func TestSupportsSamplingParams(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  bool
	}{
		// —— 仍然接受采样参数的老模型（白名单）——
		{"opus 4.5 dated", "claude-opus-4-5-20251101", true},
		{"opus 4.5 short", "claude-opus-4-5", true},
		{"sonnet 4.5 dated", "claude-sonnet-4-5-20250929", true},
		{"sonnet 4.5 short", "claude-sonnet-4-5", true},
		{"haiku 4.5 dated", "claude-haiku-4-5-20251001", true},
		{"opus 4 initial", "claude-opus-4-20250514", true},
		{"opus 4.1", "claude-opus-4-1-20250805", true},
		{"sonnet 4 initial", "claude-sonnet-4-20250514", true},
		{"claude 3 opus", "claude-3-opus-20240229", true},
		{"claude 3.5 sonnet", "claude-3-5-sonnet-20241022", true},
		{"claude 3.5 haiku", "claude-3-5-haiku-20241022", true},
		{"claude 3.7 sonnet", "claude-3-7-sonnet-20250219", true},

		// —— 已废弃采样参数的新世代（不在白名单，一律剥离）——
		{"opus 4.6", "claude-opus-4-6", false},
		{"opus 4.7", "claude-opus-4-7", false},
		{"opus 4.8", "claude-opus-4-8", false},
		{"sonnet 4.6", "claude-sonnet-4-6", false},
		{"sonnet 5", "claude-sonnet-5", false},
		{"fable 5", "claude-fable-5", false},

		// —— 未来新模型默认剥离（白名单策略的核心：不认识就剥）——
		{"unknown future opus", "claude-opus-9-9", false},
		{"unknown future family", "claude-newmodel-1", false},
		{"empty", "", false},

		// —— 关键回归：版本号后缀不得被当作日期后缀吞掉 ——
		// "claude-opus-4-7" 不能因为前缀 "claude-opus-4" 命中白名单。
		{"opus 4.7 must not match opus-4 base", "claude-opus-4-7-20260417", false},
		{"sonnet 4.6 must not match sonnet-4 base", "claude-sonnet-4-6-20260218", false},

		// —— Bedrock / Vertex 厂商前缀与后缀归一化 ——
		{"bedrock opus 4.5", "anthropic.claude-opus-4-5-20251101-v1:0", true},
		{"bedrock cross-region opus 4.5", "us.anthropic.claude-opus-4-5-20251101-v1:0", true},
		{"bedrock opus 4.7", "us.anthropic.claude-opus-4-7-v1:0", false},
		{"vertex opus 4.5", "claude-opus-4-5@20251101", true},
		{"vertex sonnet 5", "claude-sonnet-5@20260701", false},

		// —— Bedrock 无日期、仅厂商版本后缀（本仓 bedrock_request_test.go 的既有写法）——
		{"bedrock sonnet 4.5 no date", "us.anthropic.claude-sonnet-4-5-v1", true},
		{"bedrock sonnet 4.5 v1:0", "us.anthropic.claude-sonnet-4-5-v1:0", true},
		{"bedrock opus 4.6 no date", "us.anthropic.claude-opus-4-6-v1", false},
		{"bedrock claude 3.5 sonnet v2", "us.anthropic.claude-3-5-sonnet-20241022-v2:0", true},
		// 'v' 开头才算厂商后缀，纯数字段仍是版本号段
		{"bedrock opus 4.7 bare", "us.anthropic.claude-opus-4-7", false},
		{"alias latest", "claude-3-5-sonnet-latest", true},

		// —— 大小写与空白宽容 ——
		{"uppercase", "Claude-Opus-4-5-20251101", true},
		{"padded", "  claude-sonnet-4-5-20250929  ", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SupportsSamplingParams(tt.model); got != tt.want {
				t.Errorf("SupportsSamplingParams(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestIsSamplingParamsDeprecatedError(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want bool
	}{
		{
			name: "anthropic temperature deprecated",
			msg:  "`temperature` is deprecated for this model.",
			want: true,
		},
		{
			name: "anthropic top_p deprecated",
			msg:  "`top_p` is deprecated for this model.",
			want: true,
		},
		{
			name: "anthropic top_k deprecated",
			msg:  "`top_k` is deprecated for this model.",
			want: true,
		},
		{
			name: "unsupported parameter phrasing",
			msg:  "Unsupported parameter: temperature",
			want: true,
		},
		{
			name: "not supported phrasing",
			msg:  "temperature is not supported for this model",
			want: true,
		},
		{
			name: "uppercase",
			msg:  "`Temperature` Is Deprecated For This Model.",
			want: true,
		},
		// —— 不得误判的其他 400 ——
		{
			name: "unrelated deprecation",
			msg:  "`max_tokens_to_sample` is deprecated for this model.",
			want: false,
		},
		{
			name: "temperature range error is not a deprecation",
			msg:  "temperature: Input should be less than or equal to 1",
			want: false,
		},
		{
			name: "signature error",
			msg:  "The thinking block signature is invalid",
			want: false,
		},
		{
			name: "empty",
			msg:  "",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSamplingParamsDeprecatedError(tt.msg); got != tt.want {
				t.Errorf("IsSamplingParamsDeprecatedError(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}
