package service

import (
	"fmt"
	"strings"
	"time"
)

// reclaude 生命周期流量的内层 UA。
//
// ✅ 真值：同一次会话的 26 条抓包里出现**四种** UA，按调用方分工严格区分：
//   - axios/1.15.2 —— Claude Code 内部 HTTP 客户端发的控制面 GET
//   - claude-cli/… (external, sdk-cli) —— CLI 本体（推理、grove、mcp-registry）
//   - claude-code/… —— 遥测上报（event_logging）
//   - Bun/1.4.3 —— SDK eval 端点（我们不合成，见下方说明）
//
// 🔴 全部统一成一种 UA 是 D13：真值异构而我们单一，按 UA group by 一眼可见。
const (
	reclaudeInnerUAAxios = "axios/1.15.2"
	// 带版本号的两个由 reclaudeInnerUA* 函数按账号的 client_version 拼出来 ——
	// 写死版本号会让整个设备群停在同一个版本上（D9）。
	reclaudeInnerUACLIFormat       = "claude-cli/%s (external, sdk-cli)"
	reclaudeInnerUAClaudeCodeForma = "claude-code/%s"
)

// ReclaudeLifecycleRequest 描述一条要合成的生命周期请求。
//
// 只描述**形状**，不含凭据与 traceId —— 那两项由转发器在发送时统一注入，
// 和推理请求走完全相同的路径（同一条代理、同一套签名、同一个 profile）。
type ReclaudeLifecycleRequest struct {
	Method string
	// URL 是内层的完整 Anthropic URL，原样进信封 meta。
	URL string
	// Headers 是内层头。取值逐字对齐抓包 —— 少一个 anthropic-beta
	// 就是一条「这个客户端不是它自称的那个版本」的证据。
	Headers map[string]string
	// NeedsAuthorization 为 false 时不注入 Authorization。
	//
	// 🔴 mcp-registry 真值里**没有** authorization 头，却带 x-organization-uuid。
	// 给它补上 Authorization 是「照着别的请求抄」的直接特征。
	NeedsAuthorization bool
	// Delay 是相对会话起点的发送时刻，用来复刻真值的时序。
	Delay time.Duration
}

// reclaudeBootstrapAcceptHeaders 是每条生命周期请求都带的三个头。
//
// ✅ 真值：26/26 条内层请求的 accept / accept-encoding / connection 完全一致。
func reclaudeBootstrapAcceptHeaders() map[string]string {
	return map[string]string{
		"accept":          "application/json, text/plain, */*",
		"accept-encoding": "gzip, compress, deflate, br",
		// 真值恒为 close，不是 keep-alive —— 外层信封的 keepalive 字段是另一回事。
		"connection": "close",
		"host":       "api.anthropic.com",
	}
}

// BuildReclaudeBootstrapRequests 产出一次「Claude Code 启动」的引导流量。
//
// ✅ 真值时序（2026-09-23 抓包，143742.675 起算）：
//
//	+0.000  POST /api/eval/sdk-…                        Bun/1.4.3       ← 不合成
//	+0.004  GET  /api/oauth/profile                     axios
//	+0.005  GET  /v1/mcp_servers?…&include_additional…  axios
//	+0.520  GET  /v1/mcp_servers?…&include_additional…  axios
//	+2.030  GET  /v1/mcp_servers?…&include_additional…  axios
//	+2.034  GET  /v1/mcp_servers?limit=1000             axios
//	+3.160  GET  /api/claude_code_penguin_mode          axios
//	+3.161  GET  /mcp-registry/v0/servers?…             claude-cli（无 auth）
//	+3.162  GET  /api/claude_code_grove                 claude-cli
//
// 🔴 **不合成 /api/eval/sdk-…**：它的路径里带一个我们编不出来的 SDK 实例 ID
// （`sdk-zAZezfDKGoZuXXKe`），而且 UA 是 Bun —— 那是 SDK 侧的东西，不是 CLI。
// 编一个假 ID 发过去，比不发更容易穿帮：对端只要校验 ID 是否属于真实 SDK 会话
// 就能抓出来。**缺席是可以解释的（没装 SDK），伪造不行。**
//
// mcp_servers 重复四次是真值 —— Claude Code 在启动过程中确实反复拉它
// （不同阶段各拉一次）。抹平成一次反而与真值不符。
func BuildReclaudeBootstrapRequests(clientVersion string) []ReclaudeLifecycleRequest {
	axios := func(url string, delay time.Duration, beta string) ReclaudeLifecycleRequest {
		headers := reclaudeBootstrapAcceptHeaders()
		headers["user-agent"] = reclaudeInnerUAAxios
		headers["content-type"] = "application/json"
		if beta != "" {
			headers["anthropic-beta"] = beta
		}
		return ReclaudeLifecycleRequest{
			Method: "GET", URL: url, Headers: headers,
			NeedsAuthorization: true, Delay: delay,
		}
	}

	mcpServersURL := "https://api.anthropic.com/v1/mcp_servers?limit=1000&include_additional_installs=true"
	mcpHeaders := func() map[string]string {
		headers := reclaudeBootstrapAcceptHeaders()
		headers["user-agent"] = reclaudeInnerUAAxios
		headers["content-type"] = "application/json"
		headers["anthropic-beta"] = "mcp-servers-2025-12-04"
		headers["anthropic-version"] = "2023-06-01"
		headers["mcp-protocol-version"] = "2025-11-25"
		// ✅ 真值里的定值：base64 of {"roots":{"listChanged":true},"elicitation":{}}
		headers["anthropic-mcp-client-capabilities"] =
			"eyJyb290cyI6eyJsaXN0Q2hhbmdlZCI6dHJ1ZX0sImVsaWNpdGF0aW9uIjp7fX0="
		return headers
	}
	mcp := func(url string, delay time.Duration) ReclaudeLifecycleRequest {
		return ReclaudeLifecycleRequest{
			Method: "GET", URL: url, Headers: mcpHeaders(),
			NeedsAuthorization: true, Delay: delay,
		}
	}

	cliHeaders := func() map[string]string {
		headers := reclaudeBootstrapAcceptHeaders()
		headers["user-agent"] = reclaudeInnerUACLI(clientVersion)
		return headers
	}

	groveHeaders := cliHeaders()
	groveHeaders["anthropic-beta"] = "oauth-2025-04-20"

	registryHeaders := cliHeaders()
	registryHeaders["content-type"] = "application/json"

	return []ReclaudeLifecycleRequest{
		axios("https://api.anthropic.com/api/oauth/profile", 4*time.Millisecond, ""),
		mcp(mcpServersURL, 5*time.Millisecond),
		mcp(mcpServersURL, 520*time.Millisecond),
		mcp(mcpServersURL, 2030*time.Millisecond),
		mcp("https://api.anthropic.com/v1/mcp_servers?limit=1000", 2034*time.Millisecond),
		axios("https://api.anthropic.com/api/claude_code_penguin_mode",
			3160*time.Millisecond, "oauth-2025-04-20"),
		{
			Method: "GET",
			URL: "https://api.anthropic.com/mcp-registry/v0/servers?version=latest&limit=100" +
				"&visibility=commercial%2Cgsuite%2Centerprise%2Chealth",
			Headers: registryHeaders,
			// 🔴 真值里这条没有 Authorization。补上就是抄错。
			NeedsAuthorization: false,
			Delay:              3161 * time.Millisecond,
		},
		{
			Method:             "GET",
			URL:                "https://api.anthropic.com/api/claude_code_grove",
			Headers:            groveHeaders,
			NeedsAuthorization: true,
			Delay:              3162 * time.Millisecond,
		},
	}
}

// BuildReclaudeInferenceFollowups 产出跟随一次推理的生命周期请求。
//
// ✅ 真值：稳态里 Claude Code 在推理前后会复查 mcp_servers
// （143746.349 / 143747.862 / 143747.867 三条，夹在推理之间）。
//
// 只在**会话的前几次推理**跟随：真值里这种复查集中在会话早期，
// 后面的推理（143754 / 143803 / 143822）不再带它。
func BuildReclaudeInferenceFollowups(turn int) []ReclaudeLifecycleRequest {
	if turn < 1 || turn > 2 {
		return nil
	}
	headers := reclaudeBootstrapAcceptHeaders()
	headers["user-agent"] = reclaudeInnerUAAxios
	headers["content-type"] = "application/json"
	headers["anthropic-beta"] = "mcp-servers-2025-12-04"
	headers["anthropic-version"] = "2023-06-01"
	headers["mcp-protocol-version"] = "2025-11-25"
	headers["anthropic-mcp-client-capabilities"] =
		"eyJyb290cyI6eyJsaXN0Q2hhbmdlZCI6dHJ1ZX0sImVsaWNpdGF0aW9uIjp7fX0="

	return []ReclaudeLifecycleRequest{{
		Method:             "GET",
		URL:                "https://api.anthropic.com/v1/mcp_servers?limit=1000&include_additional_installs=true",
		Headers:            headers,
		NeedsAuthorization: true,
		Delay:              550 * time.Millisecond,
	}}
}

// reclaudeInnerUACLI 拼 CLI 的 UA。
//
// 按账号的 client_version 拼，不写死 —— 写死会让整个设备群停在同一个版本上（D9）。
func reclaudeInnerUACLI(clientVersion string) string {
	return fmt.Sprintf(reclaudeInnerUACLIFormat, reclaudeClientVersionOr(clientVersion))
}

// reclaudeInnerUAClaudeCode 拼遥测上报的 UA。
func reclaudeInnerUAClaudeCode(clientVersion string) string {
	return fmt.Sprintf(reclaudeInnerUAClaudeCodeForma, reclaudeClientVersionOr(clientVersion))
}

// reclaudeLifecycleFallbackVersion 是账号没写 client_version 时的兜底。
//
// ⚠️ 兜底值只是让请求发得出去，不是「正确答案」：一批账号共用同一个兜底版本
// 与 D9 是同一个问题。建号流程要求填 client_version，走到这里说明数据不全。
const reclaudeLifecycleFallbackVersion = "2.1.280"

func reclaudeClientVersionOr(clientVersion string) string {
	trimmed := strings.TrimSpace(clientVersion)
	if trimmed == "" {
		return reclaudeLifecycleFallbackVersion
	}
	// 建号表单里可能填成 "v1.4.0"；UA 里真值不带 v 前缀。
	return strings.TrimPrefix(trimmed, "v")
}
