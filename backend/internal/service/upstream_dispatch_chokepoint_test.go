package service

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 结构性护栏：service 层只允许 dispatch 文件直接调用 httpUpstream.DoWithTLS。
//
// 为什么用测试而不是 code review：DoWithTLS 的调用点会随上游合并不断新增
// （本次核对实际有 19 处，比初版设计文档写的 12 处还多 7 处）。漏改一处的
// 后果不是报错，而是**带着空 Bearer 把原始请求直接打到 api.anthropic.com**
// ——既泄漏请求内容又必然 401，且不会有任何人注意到。
//
// 新增调用点时不要往 allowlist 里加文件，改走 s.doUpstream(...)。
func TestDoWithTLSIsFunneledThroughDispatch(t *testing.T) {
	const chokepoint = "httpUpstream.DoWithTLS("

	// allowlist 只有三个条目，各自的理由都必须是「它是链路终点，且目标不是 Anthropic」：
	allowed := map[string]string{
		// 分发本身。
		"gateway_upstream_dispatch.go": "唯一收口点",
		// reclaude 分支的终点。它发出去的是**信封**（打到 reclaude 网关），
		// 不是原始 Anthropic 请求，所以不存在本测试要防的那种泄漏；
		// 反过来让它走 doUpstream 会无限递归。
		// 「发的确实是信封而不是原始请求」由 TestReclaudeUpstream_Do_Request 保证。
		"reclaude_upstream.go": "reclaude 分支终点，发的是信封",
		// reclaude 控制面终点（心跳 / 健康检查 / 账号状态）。它发的是不带信封的
		// 裸 GET，但目标 host 在构造请求**之前**就被 ReclaudeAllowedGatewayHosts
		// 硬校验过（RequireAllowlist），结构上到不了 api.anthropic.com；
		// 让它走 doUpstream 会被当成推理流量装箱，语义是错的。
		// 「目标确实被白名单钉死」由 TestReclaudeGatewayProbe 保证。
		"reclaude_probe.go": "reclaude 控制面终点，目标 host 经白名单校验",
	}

	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	var violations []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if _, ok := allowed[name]; ok {
			continue
		}

		content, err := os.ReadFile(filepath.Clean(name))
		require.NoError(t, err)

		for i, line := range strings.Split(string(content), "\n") {
			if strings.Contains(line, chokepoint) && !strings.HasPrefix(strings.TrimSpace(line), "//") {
				violations = append(violations, name+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
	}

	require.Emptyf(t, violations,
		"以下调用点绕过了 dispatch 收口，reclaude 账号会从这里把明文打到 Anthropic：\n%s",
		strings.Join(violations, "\n"))
}

// httpUpstream.Do（非 TLS 变体）是同一条泄漏路径，设计文档没提到它。
//
// 现状是安全的：全部 Do 调用点都在平台专属链路上（Grok / Gemini / Antigravity /
// CN provider / OpenAI / Ollama / Seedance），PlatformAnthropic 的账号结构上到不了。
// 所以这里不做全局收口（改 30 多处平台代码的风险远大于收益），只钉死
// Anthropic 转发路径这几个文件：将来谁往里加一个裸 Do，这条断言先红。
func TestAnthropicForwardPathDoesNotUseRawDo(t *testing.T) {
	anthropicForwardFiles := []string{
		"gateway_forward.go",
		"gateway_forward_as_chat_completions.go",
		"gateway_forward_as_responses.go",
		"gateway_count_tokens.go",
		"gateway_anthropic_passthrough.go",
		"gateway_bedrock.go",
	}

	for _, name := range anthropicForwardFiles {
		content, err := os.ReadFile(filepath.Clean(name))
		require.NoErrorf(t, err, "文件不存在，可能已被上游重命名：%s", name)

		for i, line := range strings.Split(string(content), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			require.NotContainsf(t, line, "httpUpstream.Do(",
				"%s:%d 在 Anthropic 转发路径上直接调用了 httpUpstream.Do，reclaude 账号会从这里泄漏：%s",
				name, i+1, trimmed)
		}
	}
}
