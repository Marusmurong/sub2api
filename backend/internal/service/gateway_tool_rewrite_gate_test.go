//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// 构造一份足以触发动态混淆的 tools（数量必须 > dynamicToolMapThreshold）。
func toolsBodyBeyondThreshold() []byte {
	return []byte(`{"tools":[
		{"name":"Bash","input_schema":{}},
		{"name":"Read","input_schema":{}},
		{"name":"Edit","input_schema":{}},
		{"name":"Write","input_schema":{}},
		{"name":"Glob","input_schema":{}},
		{"name":"Grep","input_schema":{}},
		{"name":"WebFetch","input_schema":{}},
		{"name":"WebSearch","input_schema":{}}
	],"messages":[{"role":"user","content":"hi"}]}`)
}

// 开关关闭（默认）时不得产生任何混淆映射。
//
// 这条断言的分量：混淆会把工具名改成 search_exe00 / invoke_Bas01 这类带序号的合成名，
// 而请求头与计费块同时声称自己是 claude-cli/2.1.220。两者不可能同时为真，上游据此即可
// 分类为 "Third-party apps"。2026-08-03 的生产数据显示该判定 100% 集中在唯一放行非 CC
// 的分组上，强制 CC 的分组零命中。
func TestBuildMimicToolNameRewrite_DisabledByDefault(t *testing.T) {
	svc := &GatewayService{cfg: &config.Config{}}

	if rw := svc.buildMimicToolNameRewrite(toolsBodyBeyondThreshold()); rw != nil {
		t.Fatalf("开关默认关闭时仍构造了混淆映射: %v", rw.Forward)
	}
}

// 显式打开时行为不变——这是留给回退的路径，必须仍然可用。
func TestBuildMimicToolNameRewrite_EnabledRestoresRewrite(t *testing.T) {
	svc := &GatewayService{cfg: &config.Config{}}
	svc.cfg.Gateway.MimicToolNameRewrite = true

	rw := svc.buildMimicToolNameRewrite(toolsBodyBeyondThreshold())
	if rw == nil {
		t.Fatal("开关打开后仍未构造混淆映射，回退路径已失效")
	}
	if len(rw.Forward) == 0 {
		t.Fatal("混淆映射为空")
	}
	if got, ok := rw.Forward["Bash"]; !ok || got == "Bash" {
		t.Fatalf("Bash 未被改写: %q", got)
	}
}

// cfg 为 nil 时必须安全降级为「不混淆」，不能 panic。
// 服务里多处以 s.cfg != nil 作守卫，这里保持一致。
func TestBuildMimicToolNameRewrite_NilConfigIsSafe(t *testing.T) {
	svc := &GatewayService{}
	if rw := svc.buildMimicToolNameRewrite(toolsBodyBeyondThreshold()); rw != nil {
		t.Fatal("cfg 为 nil 时应视为关闭")
	}

	var nilSvc *GatewayService
	if rw := nilSvc.buildMimicToolNameRewrite(toolsBodyBeyondThreshold()); rw != nil {
		t.Fatal("接收者为 nil 时应视为关闭")
	}
}
