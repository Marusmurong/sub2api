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
	// runtime-version 跟 claude-cli 内嵌 node 走（machine_env.node_version），
	// 与 UA 的版本自洽。
	if snapshot.NodeVersion != "" {
		headers["x-stainless-runtime-version"] = snapshot.NodeVersion
	}

	// 🔴 x-stainless-helper-method 真客户端流式**也不发**（7/7 抓包内层无此头），
	// 而 CC 伪装在流式时会加 stream。删掉 —— 多一个头就是一处可判别差异。
	delete(headers, "x-stainless-helper-method")

	// UA 后缀：真客户端 /v1/messages 是 (external, sdk-cli)，CC 伪装默认 (external, cli)。
	// 只改后缀，版本段不动。
	if ua := headers["user-agent"]; strings.Contains(ua, "(external, cli)") {
		headers["user-agent"] = strings.Replace(ua, "(external, cli)", "(external, sdk-cli)", 1)
	}
}

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
