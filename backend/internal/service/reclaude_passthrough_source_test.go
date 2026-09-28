//go:build unit

package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writePassthroughFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "passthrough.json")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func TestLoadReclaudePassthroughHosts(t *testing.T) {
	t.Run("读真机 passthrough.json 结构", func(t *testing.T) {
		// 真机实测结构:hosts map + no_sni + reportable。
		p := writePassthroughFile(t, `{"updated_at_ms":1790442971267,"no_sni":12,"hosts":{
			"static.reclaude.ai":{"n":2,"listeners":["connect"],"reportable":true},
			"api.ipify.org":{"n":1,"listeners":["connect"],"reportable":true}}}`)
		hosts := LoadReclaudePassthroughHosts(p)
		require.Len(t, hosts, 2)
		// 按 host 名排序:api.ipify.org 在前。
		require.Equal(t, "api.ipify.org", hosts[0].Host)
		require.Equal(t, 1, hosts[0].N)
		require.Equal(t, []string{"connect"}, hosts[0].Listeners)
		require.Equal(t, "static.reclaude.ai", hosts[1].Host)
		require.Equal(t, 2, hosts[1].N)
	})

	t.Run("reportable=false 的 host 不上报", func(t *testing.T) {
		p := writePassthroughFile(t, `{"hosts":{
			"good.example":{"n":1,"listeners":["connect"],"reportable":true},
			"bad.example":{"n":9,"listeners":["connect"],"reportable":false}}}`)
		hosts := LoadReclaudePassthroughHosts(p)
		require.Len(t, hosts, 1)
		require.Equal(t, "good.example", hosts[0].Host)
	})

	t.Run("hosts 空 → nil", func(t *testing.T) {
		p := writePassthroughFile(t, `{"no_sni":31,"hosts":{}}`)
		require.Nil(t, LoadReclaudePassthroughHosts(p))
	})

	t.Run("文件不存在 → nil(降级,不报错)", func(t *testing.T) {
		require.Nil(t, LoadReclaudePassthroughHosts("/nonexistent/passthrough.json"))
	})

	t.Run("坏 JSON → nil(不 panic)", func(t *testing.T) {
		p := writePassthroughFile(t, `{not json`)
		require.NotPanics(t, func() { require.Nil(t, LoadReclaudePassthroughHosts(p)) })
	})

	t.Run("空路径 → nil", func(t *testing.T) {
		require.Nil(t, LoadReclaudePassthroughHosts(""))
	})

	t.Run("listeners 缺失补默认 connect", func(t *testing.T) {
		p := writePassthroughFile(t, `{"hosts":{"h.example":{"n":1}}}`)
		hosts := LoadReclaudePassthroughHosts(p)
		require.Len(t, hosts, 1)
		require.Equal(t, []string{"connect"}, hosts[0].Listeners)
	})
}

func TestReclaudePassthroughFilePath(t *testing.T) {
	t.Run("账号 extra 显式路径优先", func(t *testing.T) {
		acc, _ := probeAccount(t)
		acc.Extra = map[string]any{ExtraKeyReclaudePassthroughFile: "/custom/pt.json"}
		require.Equal(t, "/custom/pt.json", ReclaudePassthroughFilePath(acc))
	})
	t.Run("无配置回落默认路径", func(t *testing.T) {
		acc, _ := probeAccount(t)
		require.Equal(t, reclaudeDefaultPassthroughFile, ReclaudePassthroughFilePath(acc))
	})
	t.Run("非 reclaude 账号 → 空", func(t *testing.T) {
		require.Equal(t, "", ReclaudePassthroughFilePath(&Account{Type: "openai"}))
	})
}

func TestReclaudeTelemetryPayloadWithPassthrough(t *testing.T) {
	t.Run("payload 恒 3 key(含显式 passthrough_overflow=0)", func(t *testing.T) {
		body, err := BuildReclaudeTelemetryPayload(nil, nil)
		require.NoError(t, err)
		var decoded map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(body, &decoded))
		require.ElementsMatch(t,
			[]string{"rollups", "passthrough_hosts", "passthrough_overflow"},
			keysOf(decoded), "真值 body 恰 3 key")
		require.Equal(t, "[]", string(decoded["rollups"]))
		require.Equal(t, "[]", string(decoded["passthrough_hosts"]))
		require.Equal(t, "0", string(decoded["passthrough_overflow"]))
	})

	t.Run("passthrough 填入后逐字段对齐真值形态", func(t *testing.T) {
		pt := []ReclaudeTelemetryPassthroughHost{
			{Host: "static.reclaude.ai", N: 2, Listeners: []string{"connect"}},
		}
		body, err := BuildReclaudeTelemetryPayload(nil, pt)
		require.NoError(t, err)
		require.JSONEq(t,
			`{"rollups":[],"passthrough_hosts":[{"host":"static.reclaude.ai","n":2,"listeners":["connect"]}],"passthrough_overflow":0}`,
			string(body))
	})

	t.Run("三空跳过门槛", func(t *testing.T) {
		require.False(t, ReclaudeTelemetryHasContent(nil, nil), "全空不发")
		require.True(t, ReclaudeTelemetryHasContent(nil,
			[]ReclaudeTelemetryPassthroughHost{{Host: "h", N: 1}}), "有 passthrough 就发")
		require.True(t, ReclaudeTelemetryHasContent(
			[]ReclaudeTelemetryRollup{{}}, nil), "有 rollup 就发")
	})
}
