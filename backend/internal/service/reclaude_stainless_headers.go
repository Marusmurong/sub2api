package service

import "strings"

// overrideReclaudeStainlessHeaders 用账号真机指纹覆盖内层 x-stainless-* 头。
//
// 🔴 第六次撤销（2026-09-25）根因修复。真客户端的 x-stainless-os/arch 由
// claude-cli SDK 运行时读 process.platform / process.arch 现算（反编译到函数体：
// vs(process.platform): darwin→MacOS, linux→Linux, win32→windows；
// Rs(process.arch): x64→x64, arm64→arm64），**恒等于真机**。而 CC 伪装给
// 所有 OAuth 账号注入 MacOS（给 Anthropic 用），装进 reclaude 信封内层就与
// 登录档案（真机 OS）矛盾。这里用建号采集的真机 env 覆盖回真值。
//
// 🔴 只覆盖有真机快照的字段：缺快照时保留 CC 伪装值（降级），不凭空改成一个
// 同样对不上的值。node/js/timeout/retry-count 那几个真客户端与我们一致，不动。
func overrideReclaudeStainlessHeaders(headers map[string]string, account *Account) {
	if headers == nil || account == nil {
		return
	}
	snapshot := parseReclaudeMachineEnv(account.GetCredential(CredKeyReclaudeMachineEnv))

	// os / arch 来自真机快照（machine_env 里的 arch 已是 SDK 命名 x64/arm64，
	// 与 x-stainless-arch 同源；os 需从平台名映射成 SDK 的大小写形态）。
	if os := reclaudeStainlessOSFromEnv(account, snapshot); os != "" {
		headers["x-stainless-os"] = os
	}
	if snapshot.Arch != "" {
		headers["x-stainless-arch"] = snapshot.Arch
	}
	// runtime-version 跟 claude-cli **内嵌** node 运行时走（machine_env.node_version
	// 存的就是它，不是系统 node）。归一 v 前缀：真值恒带 v（v26.3.0），
	// 若采集落库成裸 26.3.0，补回 v，否则与真值前缀不符。
	if nv := reclaudeNormalizeNodeVersion(snapshot.NodeVersion); nv != "" {
		headers["x-stainless-runtime-version"] = nv
	}

	// 🔴 x-stainless-helper-method 真客户端流式**也不发**（7/7 抓包内层无此头），
	// 而 CC 伪装在流式时会加 stream。删掉 —— 多一个头就是一处可判别差异。
	delete(headers, "x-stainless-helper-method")

	// UA 后缀：真客户端 /v1/messages 是 (external, sdk-cli)，CC 伪装默认 (external, cli)。
	// 只改后缀，版本段不动。
	if ua := headers["user-agent"]; strings.Contains(ua, "(external, cli)") {
		headers["user-agent"] = strings.Replace(ua, "(external, cli)", "(external, sdk-cli)", 1)
	}

	// 🔴 anthropic-beta 对齐 reclaude 真客户端(2.1.280)，但**必须与我们实际
	// 发出的 body 对称**，不是照抄真客户端。
	//
	// 共享 CC 伪装模板是给 Anthropic 直连校准的(2.1.263,14 token)；reclaude
	// 真客户端是 2.1.280(18 token)。两个上游对 beta 集要求不同，只在 reclaude
	// 信封路径覆盖，共享模板一行不动。
	//
	// 🔴 关键陷阱：真客户端 18 token 含 server-side-fallback，因为它 body 带
	// fallbacks。而我们**删掉了 fallbacks**(红线,防下游白嫖信用,见
	// stripFallbackFieldsForMimic)。beta 声称支持 fallback 而 body 没有 =
	// header/body 不对称 → 可能 400 或可判别。所以覆盖集里**剔除
	// server-side-fallback / fallback-credit** —— 与我们的 body 对称,
	// 而不是与真客户端对称。宁可与真客户端差这两个 token,也不能自造 body 矛盾。
	if headers["anthropic-beta"] != "" {
		headers["anthropic-beta"] = reclaudeInnerBetaHeader
	}
}

// reclaudeInnerBetaHeader 是 reclaude 出站 /v1/messages 的 anthropic-beta。
//
// ✅ 基于真客户端 2.1.280 抓包 18 token(逐字逐序)，**剔除 2 个 fallback 相关**:
//   - server-side-fallback-2026-06-01  ← 我们删了 body.fallbacks,不能声称支持
//   - fallback-credit-2026-06-01       ← 同上,信用消费红线
//
// 剩 16 token。⚠️ 随 claude-cli 版本漂移,抬版本按新抓包重核。
var reclaudeInnerBetaHeader = strings.Join([]string{
	"claude-code-20250219",
	"oauth-2025-04-20",
	"context-1m-2025-08-07",
	"interleaved-thinking-2025-05-14",
	"thinking-token-count-2026-05-13",
	"context-management-2025-06-27",
	"prompt-caching-scope-2026-01-05",
	"mid-conversation-system-2026-04-07",
	"per-turn-control-2026-07-01",
	"mid-conversation-tool-changes-2026-07-01",
	"advanced-tool-use-2025-11-20",
	"mid-conversation-system-clear-at-2026-08-21",
	"effort-2025-11-24",
	// server-side-fallback / fallback-credit 剔除(见上,body 无 fallbacks)
	"thinking-binding-controls-2026-08-01",
	"extended-cache-ttl-2025-04-11",
	"cache-diagnosis-2026-04-07",
}, ",")

// reclaudeStainlessOSFromEnv 求 x-stainless-os 的 SDK 形态值。
//
// 优先从 client_platform（如 linux/amd64）取平台名再映射；machine_env 没有
// 独立 os 字段，平台名是唯一可靠来源。映射规则与 claude-cli 的 vs() 一致。
func reclaudeStainlessOSFromEnv(account *Account, _ ReclaudeMachineEnv) string {
	platform := strings.TrimSpace(account.GetCredential(CredKeyReclaudeClientPlatform))
	osName := platform
	if idx := strings.IndexByte(platform, '/'); idx >= 0 {
		osName = platform[:idx]
	}
	switch strings.ToLower(osName) {
	case "linux":
		return "Linux"
	case "darwin":
		return "MacOS"
	case "win32", "windows":
		return "windows"
	default:
		return ""
	}
}

// reclaudeNormalizeNodeVersion 归一 node 版本的 v 前缀。
//
// 真值恒带 v（v26.3.0）。采集/落库若丢了前缀（裸 26.3.0），补回；
// 空串原样返回（调用方据此不覆盖，保留 DefaultHeaders 的 v26.3.0）。
func reclaudeNormalizeNodeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		return "v" + v
	}
	return v
}
