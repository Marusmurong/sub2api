//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 工具名混淆的适用范围。
//
// 它的用途是把第三方工具链的工具名换成通用假名。真 Claude Code 客户端的工具名本来
// 就是官方的（Task / Bash / Read / WebFetch…），换成假名反而更不像 CC；而且改写不
// 覆盖 tool_reference.tool_name，会让上游直接
//   400 Tool reference 'WebFetch' not found in available tools

func toolsJSON(names ...string) string {
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, `{"name":"`+n+`","description":"d","input_schema":{"type":"object"}}`)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// 超过 dynamicToolMapThreshold(5) 才启用动态混淆
var sixTools = toolsJSON("Task", "Bash", "Read", "Edit", "Write", "WebFetch")

func TestBuildToolNameRewrite_RenamesRealClaudeCodeToolNames(t *testing.T) {
	body := []byte(`{"model":"m","messages":[],"tools":` + sixTools + `}`)

	rw := buildToolNameRewriteFromBody(body)
	require.NotNil(t, rw, "超过阈值应产生混淆映射")

	out := applyToolNameRewriteToBody(body, rw)
	require.NotEqual(t, "WebFetch", gjson.GetBytes(out, "tools.5.name").String(),
		"真实 CC 的工具名会被换成假名——这正是对真 CC 有害的行为，所以调用点按 isClaudeCode 收窄")
}

// 锁住已知缺口：改写不覆盖 tool_reference.tool_name。
// 将来若要对真 CC 恢复混淆，必须先补齐嵌套引用，否则这个 400 会立刻复现。
func TestApplyToolNameRewrite_LeavesToolReferenceStale(t *testing.T) {
	body := []byte(`{"model":"m","tools":` + sixTools +
		`,"messages":[{"role":"assistant","content":[{"type":"tool_reference","tool_name":"WebFetch"}]}]}`)

	rw := buildToolNameRewriteFromBody(body)
	require.NotNil(t, rw)
	out := applyToolNameRewriteToBody(body, rw)

	require.NotEqual(t, "WebFetch", gjson.GetBytes(out, "tools.5.name").String(),
		"工具表里已是假名")
	require.Equal(t, "WebFetch", gjson.GetBytes(out, "messages.0.content.0.tool_name").String(),
		"嵌套引用仍是原名——两者不一致正是上游 400 Tool reference not found 的成因")
}

// 少量工具（≤5）本来就不混淆，收窄不应影响这条既有行为
func TestBuildToolNameRewrite_BelowThresholdStillNoRewrite(t *testing.T) {
	body := []byte(`{"model":"m","messages":[],"tools":` + toolsJSON("Bash", "Read") + `}`)
	require.Nil(t, buildToolNameRewriteFromBody(body))
}
