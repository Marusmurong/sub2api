// Package claude provides constants and helpers for Claude API integration.
package claude

import "strings"

// Claude Code 客户端相关常量

// Beta header 常量
//
// 这里的常量对齐真实 Claude Code CLI 的最新流量（截至 2026-04）。
// 选型参考：与 Parrot (src/transform/cc_mimicry.py) 的 BETAS 保持一致，
// 原因：Anthropic 上游会基于 anthropic-beta 的完整集合判定请求来源；
// 缺少任何"官方 Claude Code 请求才会带"的 beta，都会被降级到第三方额度，
// 对应报错：`Third-party apps now draw from your extra usage, not your plan limits.`
const (
	BetaOAuth                    = "oauth-2025-04-20"
	BetaClaudeCode               = "claude-code-20250219"
	BetaInterleavedThinking      = "interleaved-thinking-2025-05-14"
	BetaFineGrainedToolStreaming = "fine-grained-tool-streaming-2025-05-14"
	BetaTokenCounting            = "token-counting-2024-11-01"
	BetaContext1M                = "context-1m-2025-08-07"
	BetaFastMode                 = "fast-mode-2026-02-01"

	// 新增（对齐官方 CLI 2.1.9x 以来的流量）
	BetaPromptCachingScope = "prompt-caching-scope-2026-01-05"
	BetaEffort             = "effort-2025-11-24"
	BetaRedactThinking     = "redact-thinking-2026-02-12"
	BetaContextManagement  = "context-management-2025-06-27"
	BetaExtendedCacheTTL   = "extended-cache-ttl-2025-04-11"

	// 2026-09-07 本机真实 2.1.263 抓包新增（三族都发的 / 仅 opus 族发的见模板）
	BetaThinkingTokenCount    = "thinking-token-count-2026-05-13"
	BetaMidConversationSystem = "mid-conversation-system-2026-04-07"
	BetaPerTurnControl        = "per-turn-control-2026-07-01"
	BetaAdvisorTool           = "advisor-tool-2026-03-01"

	// server-side refusal fallback beta 字段族（beta Messages API 专有）。
	// 客户端（Claude Code / SDK / OpenCode 等）会默认透传 body.fallbacks /
	// body.fallback_credit_token，上游仅在 anthropic-beta 携带对应 token 时接受；
	// 缺 token 时 Pydantic 拒收："fallbacks: Extra inputs are not permitted"。
	// 仅用于 sanitize 的条件判断（strip-or-keep），禁止加入
	// FullClaudeCodeMimicryBetas / DefaultBetaHeader / APIKeyBetaHeader /
	// Bedrock 白名单：server-side fallback 会换模型、改计费，不能默认打开。
	BetaServerSideFallback   = "server-side-fallback-2026-07-01"
	BetaFallbackCredit       = "fallback-credit-2026-07-01"
	BetaFallbackCreditLegacy = "fallback-credit-2026-06-01"
)

// DroppedBetas 是转发时需要从 anthropic-beta header 中移除的 beta token 列表。
// 这些 token 是客户端特有的，不应透传给上游 API。
var DroppedBetas = []string{}

// DefaultBetaHeader Claude Code 客户端默认的 anthropic-beta header
const DefaultBetaHeader = BetaClaudeCode + "," + BetaOAuth + "," + BetaInterleavedThinking + "," + BetaFineGrainedToolStreaming

// MessageBetaHeaderNoTools /v1/messages 在无工具时的 beta header
//
// NOTE: Claude Code OAuth credentials are scoped to Claude Code. When we "mimic"
// Claude Code for non-Claude-Code clients, we must include the claude-code beta
// even if the request doesn't use tools, otherwise upstream may reject the
// request as a non-Claude-Code API request.
const MessageBetaHeaderNoTools = BetaClaudeCode + "," + BetaOAuth + "," + BetaInterleavedThinking

// MessageBetaHeaderWithTools /v1/messages 在有工具时的 beta header
const MessageBetaHeaderWithTools = BetaClaudeCode + "," + BetaOAuth + "," + BetaInterleavedThinking

// CountTokensBetaHeader count_tokens 请求使用的 anthropic-beta header
const CountTokensBetaHeader = BetaClaudeCode + "," + BetaOAuth + "," + BetaInterleavedThinking + "," + BetaTokenCounting

// HaikuBetaHeader Haiku 模型在 OAuth 真实客户端透传路径上的默认 anthropic-beta header。
// OAuth mimic 路径统一使用 FullClaudeCodeMimicryBetas。
const HaikuBetaHeader = BetaOAuth + "," + BetaInterleavedThinking

// APIKeyBetaHeader API-key 账号建议使用的 anthropic-beta header（不包含 oauth）
const APIKeyBetaHeader = BetaClaudeCode + "," + BetaInterleavedThinking + "," + BetaFineGrainedToolStreaming

// APIKeyHaikuBetaHeader Haiku 模型在 API-key 账号下使用的 anthropic-beta header（不包含 oauth / claude-code）
const APIKeyHaikuBetaHeader = BetaInterleavedThinking

// DefaultCacheControlTTL 是网关代理为自己生成的 cache_control 块默认使用的 ttl。
// 真实 Claude Code CLI 当前使用 "1h"，但本仓策略是"客户端透传 ttl 优先；
// 客户端缺省时统一使用 5m"，这样既不浪费 1h 缓存额度，也保留客户端自定义能力。
const DefaultCacheControlTTL = "5m"

// CLICurrentVersion 是内置的 Claude Code CLI 伪装版本号基线（三段 semver）。
// 用于 billing attribution block 中的 cc_version=X.Y.Z.{fp} 前缀以及 fingerprint 计算。
// 必须与 DefaultHeaders["User-Agent"] 中的版本号严格一致；不一致会被 Anthropic 判第三方。
//
// ⚠️ 读取实际生效的版本号请用 CLIVersion()，它会叠加 SUB2API_CLAUDE_CLI_VERSION 覆盖。
// 直接引用本常量只在"表达内置基线"时才正确（例如覆盖值的下限校验）。
//
// 基线取 2.1.257 而非上游的 2.1.220：Anthropic 对新模型设客户端版本下限，
// claude-fable-5-1 要求 >= 2.1.251，用 2.1.220 会被上游以
// "Claude Code X.Y.Z does not support this model" 直接拒掉。
// 2.1.257 的出口面已按 docs/CC_2.1.220_EGRESS_SPEC.md §3 复核（2026-09-02）。
// 2026-09-07 抬到 2.1.263（当时 npm latest，下游真实用户占比 46%）：本机原生
// 二进制抓样与 2.1.257 相比，UA 之外的 HTTP 头、Runtime/Package 版本、TLS
// ClientHello（JA3/JA4）全部相同，只有 opus/fable 的 beta 多了
// per-turn-control-2026-07-01（beta 集合已按三族模板对齐，见 mimicryBetaTemplate*）。
// 抬版本前必须重抓样本核对头与 TLS，方法见 tlsfingerprint/claudecode_clienthello_test.go。
const CLICurrentVersion = "2.1.263"

// MimicryBetaGates 是账号级门控的 beta 开关。真实 CLI 只在账号具备对应权限时
// 才发这些 beta，所以不能按模型无脑发：发了号没有的门控 beta，形态反而不对。
// 取值来源见 Account.MimicryBetaGates()（账号 extra 里的标记）。
type MimicryBetaGates struct {
	Context1M      bool // 1M 上下文权限（context-1m-2025-08-07）
	FallbackCredit bool // 额外用量信用（fallback-credit-2026-06-01）
}

// 按模型族的出口 beta 模板（2026-09-07 本机真实 2.1.263 抓包，顺序照抄线上头）。
//
// 三族的成分与顺序都不同：haiku 把 claude-code 排在第 6 位，sonnet 没有
// per-turn-control，只有 opus/fable 族带 fallback-credit。模板里带 * 的是条件项：
// context-1m / fallback-credit 由账号门控决定，fast-mode 由 body.speed 决定。
// 抬 CLI 版本时先重抓三族各一次，对着这三张表核，有出入才改。
var (
	mimicryBetaTemplateOpus = []string{
		BetaClaudeCode,
		BetaOAuth,
		BetaContext1M, // * 门控；位置取自 2.1.257 抓包
		BetaInterleavedThinking,
		BetaThinkingTokenCount,
		BetaContextManagement,
		BetaPromptCachingScope,
		BetaMidConversationSystem,
		BetaPerTurnControl,
		BetaAdvisorTool,
		BetaEffort,
		BetaFallbackCreditLegacy, // * 门控；2.1.263 发的是 2026-06-01 这个 id
		BetaExtendedCacheTTL,
		BetaFastMode, // * body.speed=fast
	}
	mimicryBetaTemplateSonnet = []string{
		BetaClaudeCode,
		BetaOAuth,
		BetaContext1M, // * 门控
		BetaInterleavedThinking,
		BetaThinkingTokenCount,
		BetaContextManagement,
		BetaPromptCachingScope,
		BetaMidConversationSystem,
		BetaAdvisorTool,
		BetaEffort,
		BetaExtendedCacheTTL,
		BetaFastMode, // *
	}
	mimicryBetaTemplateHaiku = []string{
		BetaOAuth,
		BetaInterleavedThinking,
		BetaThinkingTokenCount,
		BetaContextManagement,
		BetaPromptCachingScope,
		BetaClaudeCode,
		BetaAdvisorTool,
		BetaExtendedCacheTTL,
		BetaFastMode, // *
	}

	// conditionalMimicryBetas 是模板里的条件项；不在这里的模板项一律发。
	conditionalMimicryBetas = map[string]struct{}{
		BetaContext1M:            {},
		BetaFallbackCreditLegacy: {},
		BetaFastMode:             {},
	}
)

// featureBetaAllowlist 是唯一允许从客户端透传上去的 beta。
//
// 伪装路径原则上只发固定集合——beta 集合的成分与大小本身就是客户端指纹，让它随下游
// 变化，等于把"一个账号多个客户端"直接写在请求里（生产抓包实测单账号出现过 2/5/6/7
// 四种集合大小）。但少数 beta 是真功能开关而非身份标记，丢掉会静默削弱客户能力，
// 故按白名单放行。
//
// 加新条目前先自问：它是"客户端是谁"还是"这次请求要什么能力"。只有后者才能进。
var featureBetaAllowlist = map[string]struct{}{
	// 1M 上下文：丢掉会让长上下文请求直接超限失败。按模型的放行/过滤仍由 beta
	// policy（getBetaPolicyFilterSet）决定，这里只决定是否进入候选集合。
	BetaContext1M: {},
}

// mimicryBetaTemplateForModel 按模型名归族。bedrock/vertex 形式的模型名也含族名。
// 未知模型按 opus 族——那是集合最大的一族，也是当前主力模型所在族。
//
// 注意这比旧的固定 7 项集合"激进"：opus 模板含 per-turn-control / mid-conversation-system
// 等，若上游将来发布一个名字里既无 sonnet 也无 haiku 的新族且不支持这些 beta，会 400。
// 抬 CLI 版本重抓样本时，顺带确认线上出现的模型名都能落进正确的族（看 usage_logs.model）。
func mimicryBetaTemplateForModel(model string) []string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "haiku"):
		return mimicryBetaTemplateHaiku
	case strings.Contains(m, "sonnet"):
		return mimicryBetaTemplateSonnet
	default:
		return mimicryBetaTemplateOpus
	}
}

// MimicryBetasForModel 返回伪装路径的出口 beta 列表：按模型族取模板，条件项按
// 账号门控 / 客户端功能白名单 / body 能力开关决定，顺序即模板顺序。
//
// clientBeta 为客户端原始 anthropic-beta 头（逗号分隔，可为空），只有
// featureBetaAllowlist 内的会被采纳，其余一律丢弃。fastMode 为 body 带
// speed:"fast"（真实 CLI 那时一定带 fast-mode，而 speed 字段是原生透传的，只补 beta
// 不剥字段，审计 M-3）。
func MimicryBetasForModel(model, clientBeta string, fastMode bool, gates MimicryBetaGates) []string {
	enabled := map[string]bool{
		BetaContext1M:            gates.Context1M,
		BetaFallbackCreditLegacy: gates.FallbackCredit,
		BetaFastMode:             fastMode,
	}
	for _, p := range strings.Split(clientBeta, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := featureBetaAllowlist[p]; ok {
			enabled[p] = true
		}
	}
	template := mimicryBetaTemplateForModel(model)
	out := make([]string, 0, len(template))
	for _, b := range template {
		if _, conditional := conditionalMimicryBetas[b]; conditional && !enabled[b] {
			continue
		}
		out = append(out, b)
	}
	return out
}

// FullClaudeCodeMimicryBetas 返回 opus 族的无条件集合（不含门控与 fast-mode）。
// 保留给不知道模型的调用方与测试；知道模型时用 MimicryBetasForModel。
func FullClaudeCodeMimicryBetas() []string {
	return MimicryBetasForModel("", "", false, MimicryBetaGates{})
}

// MimicryBetasWithClientFeatures / MimicryBetasForRequest 是不带模型信息的旧入口，
// 等价于 opus 族、无账号门控。
func MimicryBetasWithClientFeatures(clientBeta string) []string {
	return MimicryBetasForRequest(clientBeta, false)
}

func MimicryBetasForRequest(clientBeta string, fastMode bool) []string {
	return MimicryBetasForModel("", clientBeta, fastMode, MimicryBetaGates{})
}

// DefaultHeaders 是 Claude Code 客户端默认请求头。
var DefaultHeaders = map[string]string{
	// Keep these in sync with recent Claude CLI traffic to reduce the chance
	// that Claude Code-scoped OAuth credentials are rejected as "non-CLI" usage.
	// 版本参考：对齐 Parrot (src/transform/cc_mimicry.py:49) 的 CLI_USER_AGENT。
	// CLIVersion() 而非 CLICurrentVersion：前者叠加 SUB2API_CLAUDE_CLI_VERSION
	// 覆盖，后者只是内置基线。两处若不一致，UA 与 body 里的 cc_version 会互相矛盾。
	"User-Agent":       "claude-cli/" + CLIVersion() + " (external, cli)",
	"X-Stainless-Lang": "js",
	// 2026-09-07 本机抓包（2.1.257 Bun 原生 arm64 → 本机假端点，OAuth/API-key 两种模式
	// 头集合一致）实测：Package-Version 0.112.1、Runtime-Version v26.3.0、
	// Accept-Encoding "gzip, deflate, br, zstd"、显式 Connection: keep-alive。线上 TLS
	// profile 6 与该客户端 ClientHello 逐字段一致（17 cipher / 13 ext 同序 / ALPN 仅
	// http/1.1 / 无 GREASE），所以 HTTP 层可以直接照抄，不必再为旧的 Node 24 profile 压低
	// 版本。改任何一项必须重新抓包，见 docs/UPSTREAM_EXPOSURE_AUDIT_2026-09-07.html 附录。
	"X-Stainless-Package-Version": "0.112.1",
	// MacOS/arm64 rather than Linux/arm64. Measured against the fingerprints
	// real clients present to this gateway: of 23 samples, 20 reported
	// Windows/x64 and 3 MacOS/arm64 — not one reported Linux/arm64. Mimicking a
	// combination no real user reports puts every account served through
	// mimicry alone in its cell of the (version x os x arch) space, which is
	// the opposite of what mimicry is for.
	"X-Stainless-OS":              "MacOS",
	"X-Stainless-Arch":            "arm64",
	"X-Stainless-Runtime":         "node",
	"X-Stainless-Runtime-Version": "v26.3.0",
	// 恒为 0：真实 CLI 主查询建 SDK 时 maxRetries:0，重试由 CLI 自己的循环发起全新调用，
	// 头永远不递增（本机对假 529 端点连发 3 次实测）。不要按尝试序号改它。
	"X-Stainless-Retry-Count": "0",
	// Go 的 transport 默认只发 gzip 且不发 Connection；真实客户端两者都显式发。
	// 响应侧 repository/http_upstream.go 的 decompressResponseBody 已支持 gzip/deflate/br/zstd。
	"Accept-Encoding":     "gzip, deflate, br, zstd",
	"Connection":          "keep-alive",
	"X-Stainless-Timeout": "600",
	"X-App":               "cli",
	"Anthropic-Dangerous-Direct-Browser-Access": "true",
}

// Model 表示一个 Claude 模型
type Model struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

// DefaultModels Claude Code 客户端支持的默认模型列表
var DefaultModels = []Model{
	{
		ID:          "claude-fable-5-1",
		Type:        "model",
		DisplayName: "Claude Fable 5.1",
		CreatedAt:   "2026-09-01T00:00:00Z",
	},
	{
		ID:          "claude-fable-5",
		Type:        "model",
		DisplayName: "Claude Fable 5",
		CreatedAt:   "2026-06-09T00:00:00Z",
	},
	{
		ID:          "claude-opus-4-5-20251101",
		Type:        "model",
		DisplayName: "Claude Opus 4.5",
		CreatedAt:   "2025-11-01T00:00:00Z",
	},
	{
		ID:          "claude-opus-4-6",
		Type:        "model",
		DisplayName: "Claude Opus 4.6",
		CreatedAt:   "2026-02-06T00:00:00Z",
	},
	{
		ID:          "claude-opus-4-7",
		Type:        "model",
		DisplayName: "Claude Opus 4.7",
		CreatedAt:   "2026-04-17T00:00:00Z",
	},
	{
		ID:          "claude-opus-4-8",
		Type:        "model",
		DisplayName: "Claude Opus 4.8",
		CreatedAt:   "2026-05-29T00:00:00Z",
	},
	{
		ID:          "claude-opus-5",
		Type:        "model",
		DisplayName: "Claude Opus 5",
		CreatedAt:   "2026-07-25T00:00:00Z",
	},
	{
		ID:          "claude-sonnet-5",
		Type:        "model",
		DisplayName: "Claude Sonnet 5",
		CreatedAt:   "2026-07-01T00:00:00Z",
	},
	{
		ID:          "claude-sonnet-4-6",
		Type:        "model",
		DisplayName: "Claude Sonnet 4.6",
		CreatedAt:   "2026-02-18T00:00:00Z",
	},
	{
		ID:          "claude-sonnet-4-5-20250929",
		Type:        "model",
		DisplayName: "Claude Sonnet 4.5",
		CreatedAt:   "2025-09-29T00:00:00Z",
	},
	{
		ID:          "claude-haiku-4-5-20251001",
		Type:        "model",
		DisplayName: "Claude Haiku 4.5",
		CreatedAt:   "2025-10-01T00:00:00Z",
	},
}

// DefaultModelIDs 返回默认模型的 ID 列表
func DefaultModelIDs() []string {
	ids := make([]string, len(DefaultModels))
	for i, m := range DefaultModels {
		ids[i] = m.ID
	}
	return ids
}

// DefaultTestModel 测试时使用的默认模型
const DefaultTestModel = "claude-sonnet-4-5-20250929"

// ModelIDOverrides Claude OAuth 请求需要的模型 ID 映射
var ModelIDOverrides = map[string]string{
	"claude-sonnet-4-5": "claude-sonnet-4-5-20250929",
	"claude-opus-4-5":   "claude-opus-4-5-20251101",
	"claude-haiku-4-5":  "claude-haiku-4-5-20251001",
}

// ModelIDReverseOverrides 用于将上游模型 ID 还原为短名
var ModelIDReverseOverrides = map[string]string{
	"claude-sonnet-4-5-20250929": "claude-sonnet-4-5",
	"claude-opus-4-5-20251101":   "claude-opus-4-5",
	"claude-haiku-4-5-20251001":  "claude-haiku-4-5",
}

// NormalizeModelID 根据 Claude OAuth 规则映射模型
func NormalizeModelID(id string) string {
	if id == "" {
		return id
	}
	if mapped, ok := ModelIDOverrides[id]; ok {
		return mapped
	}
	return id
}

// DenormalizeModelID 将上游模型 ID 转换为短名
func DenormalizeModelID(id string) string {
	if id == "" {
		return id
	}
	if mapped, ok := ModelIDReverseOverrides[id]; ok {
		return mapped
	}
	return id
}
