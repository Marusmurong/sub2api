package claude

import (
	"log/slog"
	"os"
	"strings"

	"golang.org/x/mod/semver"
)

// CLIVersionEnv 是 CLICurrentVersion 的可选运维覆盖。
//
// 存在的理由：Anthropic 会对新模型设客户端版本下限（例如 claude-fable-5-1 要求
// claude-cli >= 2.1.251），命中时上游直接返回
// `Claude Code X.Y.Z does not support this model; version A.B.C or newer is required`。
// 在没有本开关之前，这类模型必须等 sub2api 发一个新版本才能使用，
// 而改动本身只是一个常量。xai 包的 XAI_GROK_CLI_VERSION 已经是同样的做法。
const CLIVersionEnv = "SUB2API_CLAUDE_CLI_VERSION"

// resolvedCLIVersion 在包初始化时解析一次。
//
// ⚠️ 故意不做成"每次调用读一次环境变量"：伪装身份必须在一个进程的生命周期内保持恒定。
// User-Agent 头与请求体 billing attribution 块里的 cc_version 由不同代码路径写入，
// 若两次读到不同的值（例如进程运行中有人改了环境变量），同一个请求就会自相矛盾，
// 被上游判为非正版客户端。
var resolvedCLIVersion = resolveCLIVersion(os.Getenv(CLIVersionEnv))

// CLIVersion 返回对外伪装的 Claude Code CLI 版本号（三段 semver）。
//
// 所有需要该版本号的位置都必须走本函数，不要直接引用 CLICurrentVersion——
// 后者只是"没有覆盖时的内置基线"。
func CLIVersion() string {
	return resolvedCLIVersion
}

// CalibratedCLIVersions 是我们**实际抓包标定过**的 CLI 版本集合。
//
// 🔴 只有列在这里的版本才允许生效。原因是版本号不是一个孤立的数字，
// 而是一组同时变化的出站字段中的一个：
//
//	claude-cli/<version>                    ← 版本号
//	X-Stainless-Package-Version: 0.112.1    ← SDK 版本
//	X-Stainless-Runtime-Version: v26.3.0    ← Node 版本
//	anthropic-beta: <三族模板>               ← beta 集合
//	TLS ClientHello profile                 ← 指纹
//
// 上游 v0.2.8 起有「每小时从 GitHub Releases 拉最新稳定版」的同步服务
// （ClaudeCodeVersionSyncService）。若放任它推进，同步到一个我们没抓过包的
// 版本时，发出的是「新版本号 + 旧 SDK + 旧 beta」——**真实世界不存在的组合**。
// 版本号落后只是「旧客户端」，字段组合错位是「伪造客户端」，后者严重得多；
// 且失败完全静默：同步成功、日志正常、无报错，直到账号开始被封才知道。
//
// **加新版本的前提是重新抓包**，并同步核对 constants.go 的 DefaultHeaders
// 各字段与 beta 模板。见 docs/UPSTREAM_EXPOSURE_AUDIT_2026-09-07.html 附录。
// ⚠️ 只列 >= CLICurrentVersion 的版本：低于基线的版本本来就被判据 2 拒掉，
// 列在这里只会造成「标定了却用不了」的矛盾（曾被测试抓到）。
// 抬基线时把更早的条目一并删掉。
var CalibratedCLIVersions = []string{
	// 2026-09-20 抓包核对（Bun 原生 arm64，OAuth/API-key 两种模式头集合一致）；
	// Opus 5.5 要求 >= 2.1.280，当前基线即此值。
	"2.1.280",
}

// isCalibratedCLIVersion 报告该版本是否已抓包标定。
func isCalibratedCLIVersion(version string) bool {
	for _, calibrated := range CalibratedCLIVersions {
		if version == calibrated {
			return true
		}
	}
	return false
}

// IsSupportedCLIVersion 判断运维给的覆盖值是否可用。
//
// 判据有三条，缺一不可：
//  1. 严格三段纯数字（"2.1.251"）。带 -local / -dev / +build 等后缀的版本号会被
//     identity_service 的 fingerprintUserAgentPattern 拒绝，一旦漏进去，该账号的
//     持久指纹会被写成一个不存在的客户端版本，此后所有上游请求都声称这个版本，
//     被判非正版并持续 429——而系统内没有指纹重置入口。
//  2. 不低于内置基线 CLICurrentVersion。向下覆盖没有任何使用场景，
//     却会让 identity_service 的主版本超前检查基准跟着一起降。
//  3. 已在 CalibratedCLIVersions 中抓包标定。这一条挡住上游的版本自动同步
//     拉来的未验证版本 —— 它只推进版本号，不动 SDK/beta/TLS 那几项。
func IsSupportedCLIVersion(version string) bool {
	version = strings.TrimSpace(version)
	if version == "" {
		return false
	}
	// semver 允许 "v1.2" 与预发布/构建元数据，这里都不接受：
	// Canonical 相等可排除省略段，再显式排除预发布与构建元数据。
	canonical := "v" + version
	if !semver.IsValid(canonical) || semver.Canonical(canonical) != canonical {
		return false
	}
	if semver.Prerelease(canonical) != "" || semver.Build(canonical) != "" {
		return false
	}
	if semver.Compare(canonical, "v"+CLICurrentVersion) < 0 {
		return false
	}
	// 🔴 最后一道：必须是抓过包的版本。见 CalibratedCLIVersions 的说明。
	return isCalibratedCLIVersion(version)
}

// resolveCLIVersion 把环境变量的原始值解析成可用的版本号。
// 空值静默回落（未配置是正常状态）；非空但不合法则回落并告警——
// 静默忽略一个显式配置会让运维以为已经生效。
func resolveCLIVersion(raw string) string {
	version := strings.TrimSpace(raw)
	if version == "" {
		return CLICurrentVersion
	}
	if !IsSupportedCLIVersion(version) {
		slog.Warn("ignoring invalid Claude CLI version override; falling back to the built-in pin",
			"env", CLIVersionEnv,
			"value", version,
			"builtin", CLICurrentVersion,
			"requirement", "strict three-part semver, not older than the built-in pin")
		return CLICurrentVersion
	}
	return version
}
