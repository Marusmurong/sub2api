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

// tools[-1] 缓存断点写死 ttl=5m。真 CC 自己管断点，其 system 可能用 ttl=1h；
// 上游按 tools → system → messages 校验 TTL 单调性，我们插的 5m 排在 1h 前面就 400。
func TestApplyToolsLastCacheBreakpoint_InjectsFiveMinuteTTL(t *testing.T) {
	body := []byte(`{"model":"m","tools":` + toolsJSON("Bash", "Read") + `}`)

	out := applyToolsLastCacheBreakpoint(body)

	require.Equal(t, "5m", gjson.GetBytes(out, "tools.1.cache_control.ttl").String(),
		"锁住这个写死的 5m —— 它与客户端 system 的 1h 冲突，故对真 CC 不得注入")
}

// 客户端已自带带 ttl 的断点时不覆盖（既有行为，确认未被本次改动破坏）
func TestApplyToolsLastCacheBreakpoint_RespectsExistingTTL(t *testing.T) {
	body := []byte(`{"model":"m","tools":[{"name":"Bash","description":"d","input_schema":{"type":"object"},` +
		`"cache_control":{"type":"ephemeral","ttl":"1h"}}]}`)

	out := applyToolsLastCacheBreakpoint(body)

	require.Equal(t, "1h", gjson.GetBytes(out, "tools.0.cache_control.ttl").String())
}

// ===== TTL 单调性 =====
//
// 上游要求 cache_control 的 TTL 沿 tools -> system -> messages 单调不减。我们在最前面
// 的 tools 上插 5m，而客户端 system 可能用 1h，这一插就把合法请求变成非法：
//   400 system.N.cache_control.ttl: a ttl='1h' block must not come after a ttl='5m' block
// 生产实测 15/39 组请求的 5m 是我们加的，其中 2 组后面跟着 1h。

func TestApplyToolsLastCacheBreakpoint_SkipsWhenSystemUsesLongerTTL(t *testing.T) {
	body := []byte(`{"model":"m","tools":` + toolsJSON("Bash", "Read") +
		`,"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`)

	out := applyToolsLastCacheBreakpoint(body)

	require.False(t, gjson.GetBytes(out, "tools.1.cache_control").Exists(),
		"system 用 1h 时不得在 tools 上插 5m —— 宁可少一个缓存断点，也不要整个请求被拒")
}

func TestApplyToolsLastCacheBreakpoint_SkipsWhenMessagesUseLongerTTL(t *testing.T) {
	body := []byte(`{"model":"m","tools":` + toolsJSON("Bash", "Read") +
		`,"messages":[{"role":"user","content":[{"type":"text","text":"x",` +
		`"cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`)

	out := applyToolsLastCacheBreakpoint(body)

	require.False(t, gjson.GetBytes(out, "tools.1.cache_control").Exists(),
		"messages 里的 1h 同样在 tools 之后，规则一致")
}

// 只有 5m 或完全没有 cache_control 时，断点照常打——不能因为这条守卫把既有行为废掉。
func TestApplyToolsLastCacheBreakpoint_StillAppliesWithoutLongerTTL(t *testing.T) {
	for name, extra := range map[string]string{
		"无 cache_control": ``,
		"system 用 5m":     `,"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"5m"}}]`,
		"system 无 ttl":    `,"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}]`,
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"model":"m","tools":` + toolsJSON("Bash", "Read") + extra + `}`)
			out := applyToolsLastCacheBreakpoint(body)
			require.Equal(t, "5m", gjson.GetBytes(out, "tools.1.cache_control.ttl").String())
		})
	}
}
