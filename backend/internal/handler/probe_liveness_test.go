package handler

import "testing"

func TestIsProbeUserAgent(t *testing.T) {
	tests := []struct {
		name string
		ua   string
		want bool
	}{
		// 生产实测：UA 里直接自称 probe
		{"openox 探针", "openox-claude-emergency-full-capability-probe", true},
		{"healthcheck", "acme-healthcheck/1.0", true},
		{"health-check", "svc health-check bot", true},
		{"uptime 监控", "UptimeRobot/2.0", true},
		{"monitor", "SomeMonitor/1.2", true},
		{"大小写不敏感", "MyProbe/1.0", true},

		// 真实客户端不得误伤
		{"claude cli", "claude-cli/2.1.220 (external, cli)", false},
		{"go http", "Go-http-client/2.0", false},
		{"网关", "GatewayClient/1.0", false},
		{"浏览器", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)", false},
		{"空", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isProbeUserAgent(tt.ua); got != tt.want {
				t.Errorf("isProbeUserAgent(%q) = %v, want %v", tt.ua, got, tt.want)
			}
		})
	}
}

func TestHasSessionMarker(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "Claude Code 带 session 的 metadata",
			body: `{"metadata":{"user_id":"user_abc123_account__session_9f8e7d6c-1111-2222-3333-444455556666"}}`,
			want: true,
		},
		{
			name: "metadata 存在但无 session 段",
			body: `{"metadata":{"user_id":"user_abc123"}}`,
			want: false,
		},
		{"无 metadata", `{"messages":[]}`, false},
		{"metadata.user_id 为空", `{"metadata":{"user_id":""}}`, false},
		{"非法 JSON", `not json`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasSessionMarker([]byte(tt.body)); got != tt.want {
				t.Errorf("hasSessionMarker(%s) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

func TestIsLivenessProbeRequest(t *testing.T) {
	const sessionMeta = `"metadata":{"user_id":"u_x_account__session_9f8e7d6c-1111-2222-3333-444455556666"},`

	tests := []struct {
		name string
		body string
		want bool
	}{
		// —— 无 session：宽松档，允许带 system、允许扩展文案 ——
		{
			name: "无 session + 大 system + hi（Go-http-client 的典型形态）",
			body: `{"system":"You are Claude Code, a CLI assistant with many rules...","messages":[{"role":"user","content":"hi"}]}`,
			want: true,
		},
		{
			name: "无 session + test",
			body: `{"messages":[{"role":"user","content":"test"}]}`,
			want: true,
		},
		{"无 session + ping", `{"messages":[{"role":"user","content":"ping"}]}`, true},
		{"无 session + 1+1", `{"messages":[{"role":"user","content":"1+1"}]}`, true},
		{"无 session + 测试", `{"messages":[{"role":"user","content":"测试"}]}`, true},
		{"无 session + 你是谁", `{"messages":[{"role":"user","content":"你是谁"}]}`, true},
		{
			name: "无 session + system 块数组 + 你好",
			body: `{"system":[{"type":"text","text":"长长的系统提示"}],"messages":[{"role":"user","content":"你好"}]}`,
			want: true,
		},

		// —— 有 session：严格档，带 system 就不拦（真实 CLI 会话） ——
		{
			name: "有 session + 大 system + hi → 不拦",
			body: `{` + sessionMeta + `"system":"You are Claude Code","messages":[{"role":"user","content":"hi"}]}`,
			want: false,
		},
		{
			name: "有 session + 纯 hi（无 system）→ 仍拦（严格档命中）",
			body: `{` + sessionMeta + `"messages":[{"role":"user","content":"hi"}]}`,
			want: true,
		},

		// —— 两档都不该拦 ——
		{"带 tools", `{"tools":[{"name":"Read"}],"messages":[{"role":"user","content":"hi"}]}`, false},
		{"多轮历史", `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":"test"}]}`, false},
		{"实质提问", `{"messages":[{"role":"user","content":"帮我写一个快排"}]}`, false},
		{"问候后带诉求", `{"messages":[{"role":"user","content":"hi, 帮我看下这段代码"}]}`, false},
		{"含图片", `{"messages":[{"role":"user","content":[{"type":"image","source":{}},{"type":"text","text":"hi"}]}]}`, false},
		{"空 messages", `{"messages":[]}`, false},
		{"非法 JSON", `not json`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLivenessProbeRequest([]byte(tt.body)); got != tt.want {
				t.Errorf("isLivenessProbeRequest = %v, want %v\nbody=%s", got, tt.want, tt.body)
			}
		})
	}
}
