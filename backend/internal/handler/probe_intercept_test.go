package handler

import "testing"

func TestIsProbeToolType(t *testing.T) {
	tests := []struct {
		name     string
		toolType string
		want     bool
	}{
		// —— 人造探针：必须拦 ——
		{"zzz 前缀", "zzz_source_probe_00000000", true},
		{"zzz 前缀大写", "ZZZ_Probe", true},
		{"全零日期", "computer_00000000", true},
		{"月份非法", "web_search_20251305", true},
		{"日期非法", "web_search_20250132", true},
		{"年份过早", "web_search_19990101", true},

		// —— 真实 Anthropic server tool：绝不能误伤 ——
		{"web_search", "web_search_20250305", false},
		{"web_search 新版", "web_search_20260318", false},
		{"bash", "bash_20250124", false},
		{"text_editor", "text_editor_20250728", false},
		{"code_execution", "code_execution_20260521", false},
		{"memory", "memory_20250818", false},
		{"web_fetch", "web_fetch_20260309", false},
		// computer_* 是真实工具类型，之前 400 是因为缺 beta header 而非探针
		{"computer 20241022", "computer_20241022", false},
		{"computer 20250124", "computer_20250124", false},
		{"computer 20251124", "computer_20251124", false},
		// 无日期后缀的真实工具（关键回归：不能把"没有日期"当探针）
		{"tool_search bm25", "tool_search_tool_bm25", false},
		{"tool_search regex", "tool_search_tool_regex", false},
		{"tool_search 带日期", "tool_search_tool_bm25_20251119", false},

		// —— 客户端工具：不参与判定 ——
		{"custom", "custom", false},
		{"function", "function", false},
		{"空", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isProbeToolType(tt.toolType); got != tt.want {
				t.Errorf("isProbeToolType(%q) = %v, want %v", tt.toolType, got, tt.want)
			}
		})
	}
}

func TestDetectProbeToolType(t *testing.T) {
	t.Run("定位到探针及其下标", func(t *testing.T) {
		body := []byte(`{"model":"claude-opus-4-8","tools":[
			{"type":"web_search_20250305","name":"web_search"},
			{"type":"zzz_source_probe_00000000","name":"p"}
		],"messages":[{"role":"user","content":"hi"}]}`)

		toolType, idx, found := detectProbeToolType(body)

		if !found {
			t.Fatalf("found = false, want true")
		}
		if toolType != "zzz_source_probe_00000000" {
			t.Errorf("toolType = %q", toolType)
		}
		if idx != 1 {
			t.Errorf("index = %d, want 1", idx)
		}
	})

	t.Run("全是真实工具时不误报", func(t *testing.T) {
		body := []byte(`{"tools":[{"type":"web_search_20250305"},{"type":"computer_20250124"},{"name":"Read"}]}`)

		if _, _, found := detectProbeToolType(body); found {
			t.Errorf("found = true, want false")
		}
	})

	t.Run("无 tools 字段", func(t *testing.T) {
		if _, _, found := detectProbeToolType([]byte(`{"messages":[]}`)); found {
			t.Errorf("found = true, want false")
		}
	})

	t.Run("非法 JSON 不 panic", func(t *testing.T) {
		if _, _, found := detectProbeToolType([]byte(`not json`)); found {
			t.Errorf("found = true, want false")
		}
	})
}

func TestIsTrivialGreetingRequest(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		// —— 应拦：单条消息 + 无工具 + 无实质 system + 纯问候 ——
		{"hi", `{"messages":[{"role":"user","content":"hi"}]}`, true},
		{"HI 大写", `{"messages":[{"role":"user","content":"HI"}]}`, true},
		{"hello", `{"messages":[{"role":"user","content":"hello"}]}`, true},
		{"hey", `{"messages":[{"role":"user","content":"hey"}]}`, true},
		{"你好", `{"messages":[{"role":"user","content":"你好"}]}`, true},
		{"您好", `{"messages":[{"role":"user","content":"您好"}]}`, true},
		{"带标点", `{"messages":[{"role":"user","content":"Hi!"}]}`, true},
		{"带空白", `{"messages":[{"role":"user","content":"  hi  "}]}`, true},
		{"内容为文本块数组", `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, true},
		{"system 为空字符串", `{"system":"","messages":[{"role":"user","content":"hi"}]}`, true},
		{"tools 为空数组", `{"tools":[],"messages":[{"role":"user","content":"hi"}]}`, true},

		// —— 不应拦：真实会话 ——
		{"带 tools", `{"tools":[{"name":"Read"}],"messages":[{"role":"user","content":"hi"}]}`, false},
		{"多轮历史", `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"},{"role":"user","content":"hi"}]}`, false},
		{"带实质 system", `{"system":"You are a coding assistant","messages":[{"role":"user","content":"hi"}]}`, false},
		{"system 为块数组", `{"system":[{"type":"text","text":"You are Claude Code"}],"messages":[{"role":"user","content":"hi"}]}`, false},
		{"问候后有实质内容", `{"messages":[{"role":"user","content":"hi, 帮我看下这段代码"}]}`, false},
		{"非问候语", `{"messages":[{"role":"user","content":"写一个快排"}]}`, false},
		{"role 非 user", `{"messages":[{"role":"assistant","content":"hi"}]}`, false},
		{"含图片", `{"messages":[{"role":"user","content":[{"type":"image","source":{}},{"type":"text","text":"hi"}]}]}`, false},
		{"空 messages", `{"messages":[]}`, false},
		{"非法 JSON", `not json`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTrivialGreetingRequest([]byte(tt.body)); got != tt.want {
				t.Errorf("isTrivialGreetingRequest(%s) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

func TestGreetingReplyText(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantCJK bool
	}{
		{"中文问候回中文", `{"messages":[{"role":"user","content":"你好"}]}`, true},
		{"英文问候回英文", `{"messages":[{"role":"user","content":"hi"}]}`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := greetingReplyText([]byte(tt.body))
			if got == "" {
				t.Fatalf("reply is empty")
			}
			hasCJK := false
			for _, r := range got {
				if r >= 0x4E00 && r <= 0x9FFF {
					hasCJK = true
					break
				}
			}
			if hasCJK != tt.wantCJK {
				t.Errorf("reply = %q, wantCJK = %v", got, tt.wantCJK)
			}
		})
	}
}

func TestGenerateRealisticRequestID(t *testing.T) {
	id := generateRealisticRequestID()
	if len(id) < 20 {
		t.Errorf("request id too short: %q", id)
	}
	if id[:4] != "req_" {
		t.Errorf("request id must start with req_: %q", id)
	}
	if generateRealisticRequestID() == id {
		t.Errorf("request ids must not repeat")
	}
}
