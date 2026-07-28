package service

import "testing"

func TestComputerUseBetaTokenForToolType(t *testing.T) {
	tests := []struct {
		name     string
		toolType string
		want     string
	}{
		{"2024 版", "computer_20241022", "computer-use-2024-10-22"},
		{"2025 版", "computer_20250124", "computer-use-2025-01-24"},
		{"2025 冬版", "computer_20251124", "computer-use-2025-11-24"},
		{"大写宽容", "Computer_20250124", "computer-use-2025-01-24"},

		// 非 computer 工具不产生 token
		{"web_search", "web_search_20250305", ""},
		{"bash", "bash_20250124", ""},
		{"custom", "custom", ""},
		{"空", "", ""},
		// 日期段非法时不猜测
		{"日期长度不对", "computer_2025", ""},
		{"无日期", "computer_use", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computerUseBetaTokenForToolType(tt.toolType); got != tt.want {
				t.Errorf("computerUseBetaTokenForToolType(%q) = %q, want %q", tt.toolType, got, tt.want)
			}
		})
	}
}

func TestRequiredBetaTokensForBody(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "computer 工具需要对应 beta",
			body: `{"tools":[{"type":"computer_20250124","name":"computer"}]}`,
			want: []string{"computer-use-2025-01-24"},
		},
		{
			name: "多个 computer 版本各自需要",
			body: `{"tools":[{"type":"computer_20241022"},{"type":"computer_20251124"}]}`,
			want: []string{"computer-use-2024-10-22", "computer-use-2025-11-24"},
		},
		{
			name: "普通工具不需要",
			body: `{"tools":[{"type":"web_search_20250305"},{"name":"Read"}]}`,
			want: nil,
		},
		{"无 tools", `{"messages":[]}`, nil},
		{"非法 JSON", `not json`, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := requiredBetaTokensForBody([]byte(tt.body))
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestInjectRequiredBetaTokens(t *testing.T) {
	t.Run("空 header 时注入", func(t *testing.T) {
		body := []byte(`{"tools":[{"type":"computer_20250124"}]}`)

		got, changed := injectRequiredBetaTokens("", body, nil)

		if !changed {
			t.Fatalf("changed = false, want true")
		}
		if got != "computer-use-2025-01-24" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("已有 header 时追加且不重复", func(t *testing.T) {
		body := []byte(`{"tools":[{"type":"computer_20250124"}]}`)

		got, changed := injectRequiredBetaTokens("oauth-2025-04-20,computer-use-2025-01-24", body, nil)

		if changed {
			t.Fatalf("changed = true, want false (已存在不应重复注入)")
		}
		if got != "oauth-2025-04-20,computer-use-2025-01-24" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("追加到已有 header 末尾", func(t *testing.T) {
		body := []byte(`{"tools":[{"type":"computer_20250124"}]}`)

		got, changed := injectRequiredBetaTokens("oauth-2025-04-20", body, nil)

		if !changed {
			t.Fatalf("changed = false, want true")
		}
		if got != "oauth-2025-04-20,computer-use-2025-01-24" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("管理员 Beta 策略过滤掉的 token 不注入", func(t *testing.T) {
		body := []byte(`{"tools":[{"type":"computer_20250124"}]}`)
		drop := map[string]struct{}{"computer-use-2025-01-24": {}}

		got, changed := injectRequiredBetaTokens("oauth-2025-04-20", body, drop)

		if changed {
			t.Fatalf("changed = true, want false (被 drop set 拦下)")
		}
		if got != "oauth-2025-04-20" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("无需注入时原样返回", func(t *testing.T) {
		body := []byte(`{"tools":[{"type":"web_search_20250305"}]}`)

		got, changed := injectRequiredBetaTokens("oauth-2025-04-20", body, nil)

		if changed {
			t.Fatalf("changed = true, want false")
		}
		if got != "oauth-2025-04-20" {
			t.Errorf("got %q", got)
		}
	})
}
