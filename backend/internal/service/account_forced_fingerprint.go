package service

import (
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
)

// Account forced HTTP identity lives in accounts.extra["fingerprint"].
// When present (or when TLS fingerprint is enabled and we can derive a matching
// profile), the gateway MUST NOT seed/merge OS/Arch/runtime from client headers.
//
// Schema (all fields optional; missing ones fall back to TLS-profile defaults or
// claude.DefaultHeaders):
//
//	extra.fingerprint = {
//	  "os": "MacOS",
//	  "arch": "arm64",
//	  "runtime": "node",
//	  "runtime_version": "v26.3.0",
//	  "package_version": "0.112.1",
//	  "cli_version": "2.1.220",
//	  "ua_suffix": "(external, cli)",
//	  "lang": "js",
//	  "client_id": "<64-hex optional>"
//	}

const extraKeyFingerprint = "fingerprint"

// forcedFingerprintSpec is the account-bound HTTP identity (before ClientID resolve).
type forcedFingerprintSpec struct {
	OS             string
	Arch           string
	Runtime        string
	RuntimeVersion string
	PackageVersion string
	CLIVersion     string
	UASuffix       string
	Lang           string
	ClientID       string // optional sticky id stored in extra
	Source         string // "extra" | "tls_profile"
}

// HasForcedFingerprint reports whether this account should use a locked identity
// (explicit extra.fingerprint and/or TLS fingerprint enabled).
func (a *Account) HasForcedFingerprint() bool {
	if a == nil {
		return false
	}
	if fpMap := a.rawFingerprintMap(); len(fpMap) > 0 {
		return true
	}
	// 与 resolveForcedFingerprintSpec 保持同一判据：按平台定型的账号也是「强制身份」，
	// 否则 persistFingerprintClientID 会把它当 legacy 路径，ClientID 不落库。
	return a.IsTLSFingerprintEnabled() || a.ClientPlatform() != ClientPlatformUnknown
}

func (a *Account) rawFingerprintMap() map[string]any {
	if a == nil || a.Extra == nil {
		return nil
	}
	raw, ok := a.Extra[extraKeyFingerprint]
	if !ok || raw == nil {
		return nil
	}
	switch m := raw.(type) {
	case map[string]any:
		return m
	case map[string]string:
		out := make(map[string]any, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out
	default:
		return nil
	}
}

// resolveForcedFingerprintSpec builds the locked identity for an account.
// Priority: extra.fingerprint fields → TLS-profile-aligned defaults → claude.DefaultHeaders.
// Returns nil when the account has neither explicit fingerprint nor TLS enablement.
func (a *Account) resolveForcedFingerprintSpec() *forcedFingerprintSpec {
	if a == nil {
		return nil
	}
	fpMap := a.rawFingerprintMap()
	tlsOn := a.IsTLSFingerprintEnabled()
	clientPlatform := a.ClientPlatform()
	if len(fpMap) == 0 && !tlsOn && clientPlatform == ClientPlatformUnknown {
		return nil
	}

	// Base defaults: if TLS is on, align with the Claude Code 2.1.257–2.1.263 capture
	// (profile 6 / built-in default). Otherwise start from claude.DefaultHeaders.
	base := defaultsForTLSAlignedIdentity()
	if !tlsOn {
		base = defaultsFromClaudeDefaultHeaders()
	}
	switch {
	case len(fpMap) > 0:
		base.Source = "extra"
	case clientPlatform != ClientPlatformUnknown:
		base.Source = "client_platform"
	default:
		base.Source = "tls_profile"
	}

	// 按平台分池：账号定型后 OS / Arch 跟平台走（Runtime / Package / TLS 全平台相同，
	// 见 ClientPlatformIdentity）。显式 fingerprint 字段仍可在下面覆盖它。
	if os, arch, ok := ClientPlatformIdentity(clientPlatform); ok {
		base.OS, base.Arch = os, arch
	}

	if v := mapString(fpMap, "os", "OS", "stainless_os"); v != "" {
		base.OS = normalizeStainlessOS(v)
	}
	if v := mapString(fpMap, "arch", "Arch", "stainless_arch"); v != "" {
		base.Arch = strings.TrimSpace(v)
	}
	if v := mapString(fpMap, "runtime", "Runtime"); v != "" {
		base.Runtime = strings.TrimSpace(v)
	}
	if v := mapString(fpMap, "runtime_version", "runtimeVersion", "RuntimeVersion"); v != "" {
		base.RuntimeVersion = normalizeNodeVersion(v)
	}
	if v := mapString(fpMap, "package_version", "packageVersion", "PackageVersion"); v != "" {
		base.PackageVersion = strings.TrimSpace(v)
	}
	if v := mapString(fpMap, "cli_version", "cliVersion", "CLIVersion"); v != "" {
		base.CLIVersion = strings.TrimPrefix(strings.TrimSpace(v), "v")
	}
	if v := mapString(fpMap, "ua_suffix", "uaSuffix", "UASuffix"); v != "" {
		base.UASuffix = strings.TrimSpace(v)
	}
	if v := mapString(fpMap, "lang", "Lang", "stainless_lang"); v != "" {
		base.Lang = strings.TrimSpace(v)
	}
	if v := mapString(fpMap, "client_id", "clientId", "ClientID"); v != "" {
		base.ClientID = strings.TrimSpace(v)
	}

	// Hard legality: keep OS/Arch pairs that exist in the wild.
	base.OS, base.Arch = coerceOSArch(base.OS, base.Arch)
	return &base
}

func defaultsForTLSAlignedIdentity() forcedFingerprintSpec {
	// The TLS side is a macOS arm64 Claude Code 2.1.257 native binary
	// (re-captured on 2.1.263, 2026-09-07: identical ClientHello and headers)
	// (Bun 1.4 / BoringSSL): the production tls_fingerprint_profiles row every
	// active account binds (id=6) and the dialer's built-in fallback are both
	// that capture (2026-09-07). The HTTP identity below is what that same
	// binary reports — the stainless runtime string is still "node" even
	// though the process is Bun. Keep these two sides in lockstep.
	return forcedFingerprintSpec{
		OS:             "MacOS",
		Arch:           "arm64",
		Runtime:        "node",
		RuntimeVersion: "v26.3.0",
		PackageVersion: "0.112.1",
		CLIVersion:     claude.CLICurrentVersion,
		UASuffix:       "(external, cli)",
		Lang:           "js",
	}
}

func defaultsFromClaudeDefaultHeaders() forcedFingerprintSpec {
	return forcedFingerprintSpec{
		OS:             normalizeStainlessOS(claude.DefaultHeaders()["X-Stainless-OS"]),
		Arch:           claude.DefaultHeaders()["X-Stainless-Arch"],
		Runtime:        claude.DefaultHeaders()["X-Stainless-Runtime"],
		RuntimeVersion: normalizeNodeVersion(claude.DefaultHeaders()["X-Stainless-Runtime-Version"]),
		PackageVersion: claude.DefaultHeaders()["X-Stainless-Package-Version"],
		CLIVersion:     claude.CLICurrentVersion,
		UASuffix:       "(external, cli)",
		Lang:           firstNonEmptyFingerprint(claude.DefaultHeaders()["X-Stainless-Lang"], "js"),
	}
}

func (s *forcedFingerprintSpec) toFingerprint() *Fingerprint {
	if s == nil {
		return nil
	}
	ua := fmt.Sprintf("claude-cli/%s %s", s.CLIVersion, s.UASuffix)
	if s.CLIVersion == "" {
		ua = defaultFingerprint().UserAgent
	}
	return &Fingerprint{
		ClientID:                s.ClientID,
		UserAgent:               strings.TrimSpace(ua),
		StainlessLang:           firstNonEmptyFingerprint(s.Lang, "js"),
		StainlessPackageVersion: firstNonEmptyFingerprint(s.PackageVersion, "0.112.1"),
		StainlessOS:             firstNonEmptyFingerprint(s.OS, "MacOS"),
		StainlessArch:           firstNonEmptyFingerprint(s.Arch, "arm64"),
		StainlessRuntime:        firstNonEmptyFingerprint(s.Runtime, "node"),
		StainlessRuntimeVersion: firstNonEmptyFingerprint(s.RuntimeVersion, "v26.3.0"),
	}
}

func mapString(m map[string]any, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case string:
				if strings.TrimSpace(t) != "" {
					return strings.TrimSpace(t)
				}
			}
		}
	}
	return ""
}

func normalizeStainlessOS(os string) string {
	switch strings.ToLower(strings.TrimSpace(os)) {
	case "macos", "mac", "darwin", "osx":
		return "MacOS"
	case "windows", "win32", "win":
		return "Windows"
	case "linux":
		return "Linux"
	default:
		return strings.TrimSpace(os)
	}
}

func normalizeNodeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") && v[0] >= '0' && v[0] <= '9' {
		return "v" + v
	}
	return v
}

func coerceOSArch(os, arch string) (string, string) {
	os = normalizeStainlessOS(os)
	arch = strings.TrimSpace(arch)
	switch os {
	case "MacOS":
		if arch != "arm64" && arch != "x64" {
			arch = "arm64"
		}
	case "Windows":
		// Windows on ARM is vanishingly rare in CLI telemetry; force x64.
		arch = "x64"
	case "Linux":
		if arch != "arm64" && arch != "x64" {
			arch = "x64"
		}
	}
	return os, arch
}

func firstNonEmptyFingerprint(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// forcedWireFingerprint 返回该账号**必须压过全池默认头**的身份，没有则返回 nil。
//
// 为什么需要这层判断：伪装路径最后会用 claude.DefaultHeaders 覆写一遍 UA / x-stainless-*
// （applyClaudeCodeMimicHeaders），那是"全池统一身份"的基线。账号级强制身份
// （extra.fingerprint / 启用 TLS 指纹 / 按平台定型）表达的是"这个号是哪台机器"，
// 必须排在基线之后生效——否则按平台分池算出来的 Windows/x64 永远到不了线上，
// 上游看到的仍是 MacOS/arm64 头配 win32 环境块，正是分池要消除的矛盾。
//
// 遗留路径（指纹从客户端头派生）一律返回 nil：那种指纹会把下游客户的机器特征
// 带上线，统一身份的既有行为不能因为这次修复被打开。
func forcedWireFingerprint(account *Account, fp *Fingerprint) *Fingerprint {
	if account == nil || fp == nil {
		return nil
	}
	if !account.HasForcedFingerprint() {
		return nil
	}
	return fp
}
