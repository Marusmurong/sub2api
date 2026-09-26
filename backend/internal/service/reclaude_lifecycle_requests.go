package service

import (
	"fmt"
	"net/url"
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
	reclaudeInnerUACLIFormat       = "claude-cli/%s (external, cli)"
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
	// Body 是请求体；为 nil 即 GET 语义。
	//
	// 🔴 必须与签名覆盖的字节完全一致 —— 签一份发另一份等于自造签名失败。
	Body []byte
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
// ✅ 真值（2026-09-26 同机存活真客户端 2.1.282 抓包，
// docs/captures/reclaude-live-2026-09-26/reccap，第一次启动以 profile 起算）：
//
//	+0.000  POST /api/eval/sdk-…                        Bun/1.4.3     ← 不合成
//	+0.000  GET  /api/oauth/profile                     axios         cache-control: no-cache
//	+0.001  GET  /v1/mcp_servers?…&include_additional…  axios         只 1 次
//	+0.212  GET  /api/oauth/organizations/{org}/skills  claude-cli
//	+0.213  GET  /api/claude_cli/bootstrap?…            claude-code   ← 冷启动才有
//	+0.214  GET  /api/claude_code_penguin_mode          axios         ← 冷启动才有,无 content-type
//	+0.215  GET  /mcp-registry/v0/servers?…  #1         claude-cli    无 auth、无 content-type
//	+0.217  GET  /api/oauth/account/settings            claude-cli
//	+0.218  GET  /api/claude_code_grove                 claude-cli
//	+1.293  GET  /mcp-registry/v0/servers?…  #2
//	+1.933  GET  /mcp-registry/v0/servers?…  #3
//	+2.436  GET  /mcp-registry/v0/servers?…  #4
//
// 第二次启动（35 分钟后）同样的结构，但**没有 bootstrap 和 penguin** ——
// 它们是冷启动（进程首次 / 缓存过期）才拉的，不是每会话必发。
//
// 🔴 **不合成 /api/eval/sdk-…**：它的路径里带一个我们编不出来的 SDK 实例 ID
// （`sdk-zAZezfDKGoZuXXKe`），而且 UA 是 Bun —— 那是 SDK 侧的东西，不是 CLI。
// 编一个假 ID 发过去，比不发更容易穿帮：对端只要校验 ID 是否属于真实 SDK 会话
// 就能抓出来。**缺席是可以解释的（没装 SDK），伪造不行。**
//
// ⚠️ 09-23 那份 SDK 抓包里 mcp_servers 重复了四次、推理之间还有 mcp_servers 复查；
// 09-26 这份（当前基线版本 2.1.282、同机存活）都没有。两份真值不一致时取 09-26，
// 记录在案（RECLAUDE_REVOCATION_VERIFY_PLAN_2026-09-26 §4 L4），不要两种都发。
type ReclaudeBootstrapParams struct {
	// CLIVersion 是 **claude-cli** 版本（machine_env.cli_version，如 2.1.282）。
	// 🔴 内层 claude-cli/claude-code UA 必须用它,不是 reclaude 包装器版本(v1.4.0)——
	// 抓包实证真客户端全是 claude-cli/2.1.282,而旧代码误用 reclaude 版本发成
	// claude-cli/1.4.0(不存在的版本,每条合成请求都在自曝)。
	CLIVersion string
	// OrgUUID 是 reclaude_organization_uuid,给 organizations/skills 端点;空则跳过该条。
	OrgUUID string
	// Model 是本次推理模型,进 claude_cli/bootstrap 的 model 参数;空则省略该参数。
	Model string
	// Cold 为 true 表示冷启动：带 claude_cli/bootstrap 与 penguin_mode。
	// 真值里第二次启动（35 分钟后）没有这两条，见 ReclaudeColdStartGap。
	Cold bool
}

func BuildReclaudeBootstrapRequests(p ReclaudeBootstrapParams) []ReclaudeLifecycleRequest {
	axios := func(url string, delay time.Duration) ReclaudeLifecycleRequest {
		headers := reclaudeBootstrapAcceptHeaders()
		headers["user-agent"] = reclaudeInnerUAAxios
		return ReclaudeLifecycleRequest{
			Method: "GET", URL: url, Headers: headers,
			NeedsAuthorization: true, Delay: delay,
		}
	}
	cli := func(url string, delay time.Duration, beta string) ReclaudeLifecycleRequest {
		headers := reclaudeBootstrapAcceptHeaders()
		headers["user-agent"] = reclaudeInnerUACLI(p.CLIVersion)
		if beta != "" {
			headers["anthropic-beta"] = beta
		}
		return ReclaudeLifecycleRequest{
			Method: "GET", URL: url, Headers: headers,
			NeedsAuthorization: true, Delay: delay,
		}
	}
	registry := func(delay time.Duration) ReclaudeLifecycleRequest {
		r := cli("https://api.anthropic.com/mcp-registry/v0/servers?version=latest&limit=100"+
			"&visibility=commercial%2Cgsuite%2Centerprise%2Chealth", delay, "")
		// 🔴 真值里这条没有 Authorization 也没有 content-type。补上就是抄错。
		r.NeedsAuthorization = false
		return r
	}

	profile := axios("https://api.anthropic.com/api/oauth/profile", 0)
	profile.Headers["content-type"] = "application/json"
	profile.Headers["cache-control"] = "no-cache"

	mcpServers := axios(
		"https://api.anthropic.com/v1/mcp_servers?limit=1000&include_additional_installs=true",
		1*time.Millisecond)
	mcpServers.Headers["content-type"] = "application/json"
	mcpServers.Headers["anthropic-beta"] = "mcp-servers-2025-12-04"
	mcpServers.Headers["anthropic-version"] = "2023-06-01"
	mcpServers.Headers["mcp-protocol-version"] = "2025-11-25"
	// ✅ 真值里的定值：base64 of {"roots":{"listChanged":true},"elicitation":{}}
	mcpServers.Headers["anthropic-mcp-client-capabilities"] =
		"eyJyb290cyI6eyJsaXN0Q2hhbmdlZCI6dHJ1ZX0sImVsaWNpdGF0aW9uIjp7fX0="

	reqs := []ReclaudeLifecycleRequest{profile, mcpServers}

	// organizations/skills：需要 org uuid;缺则跳过(编不出真 org 更危险)。
	if org := strings.TrimSpace(p.OrgUUID); org != "" {
		skills := cli("https://api.anthropic.com/api/oauth/organizations/"+org+
			"/skills/list-skills?include_wiggle_skills=true&entrypoint=cli",
			212*time.Millisecond, "")
		skills.Headers["content-type"] = "application/json"
		skills.Headers["anthropic-version"] = "2023-06-01"
		// ✅ 真值定值:不是 OS 平台,是 SDK 自报的客户端平台标识。
		skills.Headers["anthropic-client-platform"] = "claude_code_cli"
		skills.Headers["x-organization-uuid"] = org
		reqs = append(reqs, skills)
	}

	if p.Cold {
		// claude_cli/bootstrap：UA=claude-code/<cli>,带 model 参数(本次推理模型)。
		bootstrapURL := "https://api.anthropic.com/api/claude_cli/bootstrap?entrypoint=cli"
		if m := strings.TrimSpace(p.Model); m != "" {
			bootstrapURL += "&model=" + url.QueryEscape(m)
		}
		bootstrap := ReclaudeLifecycleRequest{
			Method: "GET", URL: bootstrapURL, Headers: reclaudeBootstrapAcceptHeaders(),
			NeedsAuthorization: true, Delay: 213 * time.Millisecond,
		}
		bootstrap.Headers["user-agent"] = reclaudeInnerUAClaudeCode(p.CLIVersion)
		bootstrap.Headers["content-type"] = "application/json"
		bootstrap.Headers["anthropic-beta"] = "oauth-2025-04-20"

		penguin := axios("https://api.anthropic.com/api/claude_code_penguin_mode", 214*time.Millisecond)
		penguin.Headers["anthropic-beta"] = "oauth-2025-04-20"

		reqs = append(reqs, bootstrap, penguin)
	}

	reqs = append(reqs,
		registry(215*time.Millisecond),
		cli("https://api.anthropic.com/api/oauth/account/settings", 217*time.Millisecond, "oauth-2025-04-20"),
		cli("https://api.anthropic.com/api/claude_code_grove", 218*time.Millisecond, "oauth-2025-04-20"),
		registry(1293*time.Millisecond),
		registry(1933*time.Millisecond),
		registry(2436*time.Millisecond),
	)
	return reqs
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

// BuildReclaudeEventLoggingRequest 把一批事件包成一条内层请求。
//
// ✅ 头逐字对齐真值：UA 是 claude-code/<version>（**不是** claude-cli），
// 另有 x-service-name 与 anthropic-beta —— 这条路径在真客户端里由
// 遥测模块发出，与 CLI 本体的请求分属不同代码路径，头也因此不同。
func BuildReclaudeEventLoggingRequest(
	clientVersion string, body []byte, delay time.Duration,
) ReclaudeLifecycleRequest {
	headers := reclaudeBootstrapAcceptHeaders()
	headers["user-agent"] = reclaudeInnerUAClaudeCode(clientVersion)
	headers["content-type"] = "application/json"
	headers["anthropic-beta"] = "oauth-2025-04-20"
	headers["x-service-name"] = "claude-code"

	return ReclaudeLifecycleRequest{
		Method:             "POST",
		URL:                ReclaudeEventLoggingPath,
		Headers:            headers,
		Body:               body,
		NeedsAuthorization: true,
		Delay:              delay,
	}
}
