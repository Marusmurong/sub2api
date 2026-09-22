package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 缺陷 1：count_tokens 有自己的一整套请求构造与门控，与主转发路径并行存在。
// 它的两处 IsOAuth() 和主路径是**同一个病**：reclaude 不在其中 ⇒ 发过去的
// 内层请求不是 CC 请求、也没做身份统一，而且不报任何错。
//
// 这条只能用结构性断言：走通它需要一整套 gin + 指纹服务的装配，
// 而失效本身是静默的，语义测试抓不到。
func TestCountTokensGatesCoverReclaude(t *testing.T) {
	content, err := os.ReadFile(filepath.Clean("gateway_count_tokens.go"))
	require.NoError(t, err)

	lines := strings.Split(string(content), "\n")

	t.Run("CC 伪装门控", func(t *testing.T) {
		line := findLineContaining(t, lines, "shouldMimicClaudeCode :=")
		require.Contains(t, line, "UsesClaudeCodeMimicry()",
			"count_tokens 的伪装门控退回了 IsOAuth()，发给 reclaude 的将不是 CC 请求")
	})

	t.Run("身份统一门控", func(t *testing.T) {
		line := findLineContaining(t, lines, "s.identityService != nil")
		require.Contains(t, line, "UsesClaudeCodeMimicry()",
			"count_tokens 的身份统一门控退回了 IsOAuth()，user_id 不会被归一")
	})
}

// 缺陷 2：作息生成函数写了却没人调用 ⇒ 建出来的设备全是「从不关机」，
// 而「从不关机」正是这套机制要消除的特征。
func TestPrepareReclaudeAccountCreate_GeneratesOfflineWindow(t *testing.T) {
	cipher := NewReclaudeCredentialCipher(&fakeEncryptor{})

	t.Run("建号时生成作息", func(t *testing.T) {
		input := reclaudeCreateInput()

		prepared, err := PrepareReclaudeAccountCreate(configuredCfg(), cipher, input, input.Extra)

		require.NoError(t, err)
		window, ok := prepared.Extra[ExtraKeyReclaudeOfflineWindow].(map[string]any)
		require.True(t, ok, "没有生成 offline_window，这台设备会 24h 不休")
		require.NotEmpty(t, window["timezone"])
		require.Positive(t, window["duration_hours"])
	})

	t.Run("已配置作息时保留不覆盖", func(t *testing.T) {
		input := reclaudeCreateInput()
		input.Extra[ExtraKeyReclaudeOfflineWindow] = map[string]any{
			"timezone": "Europe/Berlin", "start_hour": float64(1), "duration_hours": float64(7),
		}

		prepared, err := PrepareReclaudeAccountCreate(configuredCfg(), cipher, input, input.Extra)

		require.NoError(t, err)
		window := prepared.Extra[ExtraKeyReclaudeOfflineWindow].(map[string]any)
		require.Equal(t, "Europe/Berlin", window["timezone"])
	})

	t.Run("不修改入参的 Extra", func(t *testing.T) {
		input := reclaudeCreateInput()

		_, err := PrepareReclaudeAccountCreate(configuredCfg(), cipher, input, input.Extra)

		require.NoError(t, err)
		require.NotContains(t, input.Extra, ExtraKeyReclaudeOfflineWindow)
	})
}

// 缺陷 3：分组隔离与分片约束只在建号路径生效 ⇒ 改组可以绕过它们。
// 而分组隔离本来就是「名字被改乱之后的最后一道防线」，被绕过就没有防线了。
func TestGroupIsolationCoversUpdatePaths(t *testing.T) {
	content, err := os.ReadFile(filepath.Clean("admin_account.go"))
	require.NoError(t, err)

	occurrences := strings.Count(string(content), "checkReclaudeGroupIsolation(ctx")
	require.GreaterOrEqualf(t, occurrences, 3,
		"分组隔离只在 %d 处调用；建号 / 改单个账号的分组 / 批量改分组，三条路径都要覆盖", occurrences)
}

func findLineContaining(t *testing.T, lines []string, marker string) string {
	t.Helper()
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.Contains(line, marker) {
			return line
		}
	}
	t.Fatalf("找不到含 %q 的代码行，可能已被上游重构", marker)
	return ""
}
