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
const CLICurrentVersion = "2.1.257"

// FullClaudeCodeMimicryBetas 返回最"像"真实 Claude Code CLI 的完整 beta 列表，
// 用于 OAuth 账号伪装成 Claude Code 时使用。
// 顺序与真实 CLI 抓包一致。
//
// 使用建议：
//   - OAuth mimic：所有模型（包括 Haiku）都使用这整份列表。
//   - OAuth 真实客户端透传：保留客户端 beta；未提供时使用模型对应默认值。
//   - API-key 账号：不要使用本函数，参见 APIKeyBetaHeader。
//   - 不默认加入 redact-thinking，避免上游抹除 thinking 内容；客户端显式传入时由合并逻辑保留。
func FullClaudeCodeMimicryBetas() []string {
	return []string{
		BetaClaudeCode,
		BetaOAuth,
		BetaInterleavedThinking,
		BetaPromptCachingScope,
		BetaEffort,
		BetaContextManagement,
		BetaExtendedCacheTTL,
	}
}

// featureBetaAllowlist 是唯一允许从客户端透传上去的 beta。
//
// 伪装路径原则上只发固定集合——beta 集合的成分与大小本身就是客户端指纹，让它随下游
// 变化，等于把"一个账号多个客户端"直接写在请求里（生产抓包实测单账号出现过 2/5/6/7
// 四种集合大小）。但少数 beta 是真功能开关而非身份标记，丢掉会静默削弱客户能力，
// 故按白名单放行。
//
// 加新条目前先自问：它是"客户端是谁"还是"这次请求要什么能力"。只有后者才能进。
var featureBetaAllowlist = map[string]struct{}{
	// 1M 上下文：仅 sonnet-5 系列支持，丢掉会让长上下文请求直接超限失败。
	BetaContext1M: {},
}

// canonicalBetaOrder 决定出口 beta 的排列顺序。
//
// 顺序本身也是指纹，所以不能按客户端到达顺序拼接。这里的相对次序取自真实 CLI 抓包：
// context-1m 出现在 oauth 之后、interleaved-thinking 之前；其余各项维持
// FullClaudeCodeMimicryBetas 既有次序不动。
var canonicalBetaOrder = []string{
	BetaClaudeCode,
	BetaOAuth,
	BetaContext1M,
	BetaInterleavedThinking,
	BetaPromptCachingScope,
	BetaEffort,
	BetaContextManagement,
	BetaExtendedCacheTTL,
}

// MimicryBetasWithClientFeatures 返回伪装路径的出口 beta 列表：
// 固定身份集合 ∪（客户端请求了的、且在功能白名单内的 beta），按 canonicalBetaOrder 排列。
//
// clientBeta 为客户端原始 anthropic-beta 头（逗号分隔，可为空）。白名单之外的一律丢弃。
func MimicryBetasWithClientFeatures(clientBeta string) []string {
	want := make(map[string]struct{}, len(canonicalBetaOrder))
	for _, b := range FullClaudeCodeMimicryBetas() {
		want[b] = struct{}{}
	}
	for _, p := range strings.Split(clientBeta, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := featureBetaAllowlist[p]; ok {
			want[p] = struct{}{}
		}
	}

	out := make([]string, 0, len(want))
	for _, b := range canonicalBetaOrder {
		if _, ok := want[b]; ok {
			out = append(out, b)
			delete(want, b)
		}
	}
	// canonicalBetaOrder 未覆盖到的（将来往固定集合里加了新 beta 却忘了排序表）
	// 兜底追加，保证不会被静默丢掉。
	for _, b := range FullClaudeCodeMimicryBetas() {
		if _, ok := want[b]; ok {
			out = append(out, b)
			delete(want, b)
		}
	}
	return out
}

// DefaultHeaders 是 Claude Code 客户端默认请求头。
var DefaultHeaders = map[string]string{
	// Keep these in sync with recent Claude CLI traffic to reduce the chance
	// that Claude Code-scoped OAuth credentials are rejected as "non-CLI" usage.
	// 版本参考：对齐 Parrot (src/transform/cc_mimicry.py:49) 的 CLI_USER_AGENT。
	// CLIVersion() 而非 CLICurrentVersion：前者叠加 SUB2API_CLAUDE_CLI_VERSION
	// 覆盖，后者只是内置基线。两处若不一致，UA 与 body 里的 cc_version 会互相矛盾。
	"User-Agent":                  "claude-cli/" + CLIVersion() + " (external, cli)",
	"X-Stainless-Lang":            "js",
	"X-Stainless-Package-Version": "0.94.0",
	// MacOS/arm64 rather than Linux/arm64. Measured against the fingerprints
	// real clients present to this gateway: of 23 samples, 20 reported
	// Windows/x64 and 3 MacOS/arm64 — not one reported Linux/arm64. Mimicking a
	// combination no real user reports puts every account served through
	// mimicry alone in its cell of the (version x os x arch) space, which is
	// the opposite of what mimicry is for.
	"X-Stainless-OS":   "MacOS",
	"X-Stainless-Arch": "arm64",
	// Left at v24.3.0 on purpose, even though real 2.1.257 clients report
	// v26.3.0: the TLS profile in pkg/tlsfingerprint was captured from Node.js
	// 24.x, so claiming v26 here would make the HTTP layer contradict the
	// ClientHello. An unusual-but-consistent runtime beats a self-contradicting
	// one — revisit together with the TLS profile, not on its own.
	"X-Stainless-Runtime":                       "node",
	"X-Stainless-Runtime-Version":               "v24.3.0",
	"X-Stainless-Retry-Count":                   "0",
	"X-Stainless-Timeout":                       "600",
	"X-App":                                     "cli",
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
