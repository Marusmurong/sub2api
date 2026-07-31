//go:build unit

package service

import (
	"strings"
	"testing"
)

// 客户端发来的工具块结构必须在转发前校验。
//
// 生产实测（2026-07-31）：key 83（一个自建 Go 中转，背后接着 Go SDK / Python SDK /
// CSSwitch 等第三方客户端）持续出现
//
//	400 messages.14.content.0.tool_use.id: Field required
//
// Anthropic 的 schema 里 tool_use.id 是硬性必填，这类请求 100% 会被拒。放它上去
// 等于白白消耗一次订阅账号的调用，并在账号上留下一个格式非法的请求记录——
// 在关联封禁的语境下后者才是主要代价。
//
// 校验必须在**任何改写之前**做：我们自己的剥离会改变块的下标，事后再查就分不清
// 是客户端发错了还是我们改坏了。
func TestDescribeMalformedToolBlock(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantPath string // 期望命中的路径片段；"" 表示应当判为合法
	}{
		{
			name: "tool_use 缺 id",
			body: `{"messages":[{"role":"assistant","content":[
				{"type":"tool_use","name":"Bash","input":{}}]}]}`,
			wantPath: "messages.0.content.0.tool_use.id",
		},
		{
			name: "tool_use 的 id 为空串",
			body: `{"messages":[{"role":"assistant","content":[
				{"type":"tool_use","id":"","name":"Bash","input":{}}]}]}`,
			wantPath: "messages.0.content.0.tool_use.id",
		},
		{
			name: "tool_use 缺 name",
			body: `{"messages":[{"role":"assistant","content":[
				{"type":"tool_use","id":"toolu_1","input":{}}]}]}`,
			wantPath: "messages.0.content.0.tool_use.name",
		},
		{
			name: "tool_result 缺 tool_use_id",
			body: `{"messages":[{"role":"user","content":[
				{"type":"tool_result","content":[{"type":"text","text":"ok"}]}]}]}`,
			wantPath: "messages.0.content.0.tool_result.tool_use_id",
		},
		{
			name: "下标要反映真实位置",
			body: `{"messages":[{"role":"user","content":"hi"},
				{"role":"assistant","content":[
					{"type":"thinking","thinking":"t","signature":"s"},
					{"type":"tool_use","name":"Bash","input":{}}]}]}`,
			wantPath: "messages.1.content.1.tool_use.id",
		},
		{
			name: "合法的 tool_use 不报",
			body: `{"messages":[{"role":"assistant","content":[
				{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"cmd":"ls"}}]}]}`,
		},
		{
			name:     "无工具块不报",
			body:     `{"messages":[{"role":"user","content":"hello"}]}`,
			wantPath: "",
		},
		{
			name:     "字符串 content 不报",
			body:     `{"messages":[{"role":"user","content":"tool_use"}]}`,
			wantPath: "",
		},
		{
			name:     "非法 JSON 不报（交给既有的解析错误路径）",
			body:     `not json`,
			wantPath: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeMalformedToolBlock([]byte(tt.body))
			if tt.wantPath == "" {
				if got != "" {
					t.Errorf("应判为合法，却报了: %s", got)
				}
				return
			}
			if !strings.Contains(got, tt.wantPath) {
				t.Errorf("期望命中 %q，实际: %q", tt.wantPath, got)
			}
		})
	}
}
