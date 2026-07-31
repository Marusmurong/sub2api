//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
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

// 服务端工具（web_search_20250305 等）的 schema 由 Anthropic 定义，不接受
// input_schema；带上它会被 400
//
//	tools.0.web_search_20250305.input_schema: Extra inputs are not permitted
//
// 生产实测（2026-07-31）：来自与 tool_use.id 同一个下游 key（自建 Go 中转，
// 背后是各类第三方 SDK）。我们自己从不给服务端工具加这个字段。
//
// 这里采取「摘掉」而不是「拒绝」，与 tool_use.id 的处理刻意不同：
//   - tool_use.id 缺失 —— 无法凭空编出正确的 id，编了就是伪造数据 → 拒绝
//   - 服务端工具的 input_schema —— 字段本身无意义（schema 由服务端定义）且被上游
//     拒收，摘掉是无损的，摘完请求就能正常完成 → 摘掉
func TestStripServerToolInputSchema(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantChange bool
		checkPath  string // 期望被摘掉的路径；wantChange=false 时忽略
	}{
		{
			name:       "web_search 带 input_schema",
			body:       `{"tools":[{"type":"web_search_20250305","name":"web_search","input_schema":{"type":"object"}}]}`,
			wantChange: true, checkPath: "tools.0.input_schema",
		},
		{
			name:       "其它服务端工具同样处理",
			body:       `{"tools":[{"type":"text_editor_20250728","name":"str_replace","input_schema":{}}]}`,
			wantChange: true, checkPath: "tools.0.input_schema",
		},
		{
			name:       "自定义工具的 input_schema 必须保留",
			body:       `{"tools":[{"name":"Bash","description":"run","input_schema":{"type":"object"}}]}`,
			wantChange: false,
		},
		{
			name:       "type=custom 的工具必须保留",
			body:       `{"tools":[{"type":"custom","name":"X","input_schema":{"type":"object"}}]}`,
			wantChange: false,
		},
		{
			name:       "服务端工具本来就没有 input_schema 时不动",
			body:       `{"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":5}]}`,
			wantChange: false,
		},
		{
			name:       "无 tools 不动",
			body:       `{"messages":[]}`,
			wantChange: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, changed := stripServerToolInputSchema([]byte(tt.body))
			if changed != tt.wantChange {
				t.Fatalf("changed = %v, want %v\n out=%s", changed, tt.wantChange, out)
			}
			if !changed {
				if string(out) != tt.body {
					t.Errorf("未触发时必须原样返回\n want %s\n got  %s", tt.body, out)
				}
				return
			}
			if gjson.GetBytes(out, tt.checkPath).Exists() {
				t.Errorf("%s 应已被摘除: %s", tt.checkPath, out)
			}
			// 其余字段必须完好
			if gjson.GetBytes(out, "tools.0.name").String() == "" {
				t.Errorf("name 被误删: %s", out)
			}
			if gjson.GetBytes(out, "tools.0.type").String() == "" {
				t.Errorf("type 被误删: %s", out)
			}
		})
	}
}
