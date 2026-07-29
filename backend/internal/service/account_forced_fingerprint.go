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
//	  "runtime_version": "v24.3.0",
//	  "package_version": "0.94.0",
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
	return a.IsTLSFingerprintEnabled()
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
	if len(fpMap) == 0 && !tlsOn {
		return nil
	}

	// Base defaults: if TLS is on, align with our Node24 macOS profiles (all current
	// production templates). Otherwise start from claude.DefaultHeaders.
	base := defaultsForTLSAlignedIdentity()
	if !tlsOn {
		base = defaultsFromClaudeDefaultHeaders()
	}
	if len(fpMap) > 0 {
		base.Source = "extra"
	} else {
		base.Source = "tls_profile"
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
	// All production tls_fingerprint_profiles today are macOS arm64 Node 24.x
	// captures. HTTP stainless must match that machine.
	return forcedFingerprintSpec{
		OS:             "MacOS",
		Arch:           "arm64",
		Runtime:        "node",
		RuntimeVersion: "v24.3.0",
		PackageVersion: "0.94.0",
		CLIVersion:     claude.CLICurrentVersion,
		UASuffix:       "(external, cli)",
		Lang:           "js",
	}
}

func defaultsFromClaudeDefaultHeaders() forcedFingerprintSpec {
	return forcedFingerprintSpec{
		OS:             normalizeStainlessOS(claude.DefaultHeaders["X-Stainless-OS"]),
		Arch:           claude.DefaultHeaders["X-Stainless-Arch"],
		Runtime:        claude.DefaultHeaders["X-Stainless-Runtime"],
		RuntimeVersion: normalizeNodeVersion(claude.DefaultHeaders["X-Stainless-Runtime-Version"]),
		PackageVersion: claude.DefaultHeaders["X-Stainless-Package-Version"],
		CLIVersion:     claude.CLICurrentVersion,
		UASuffix:       "(external, cli)",
		Lang:           firstNonEmptyFingerprint(claude.DefaultHeaders["X-Stainless-Lang"], "js"),
	}
}

func (s *forcedFingerprintSpec) toFingerprint() *Fingerprint {
	if s == nil {
		return nil
	}
	ua := fmt.Sprintf("claude-cli/%s %s", s.CLIVersion, s.UASuffix)
	if s.CLIVersion == "" {
		ua = defaultFingerprint.UserAgent
	}
	return &Fingerprint{
		ClientID:                s.ClientID,
		UserAgent:               strings.TrimSpace(ua),
		StainlessLang:           firstNonEmptyFingerprint(s.Lang, "js"),
		StainlessPackageVersion: firstNonEmptyFingerprint(s.PackageVersion, "0.94.0"),
		StainlessOS:             firstNonEmptyFingerprint(s.OS, "MacOS"),
		StainlessArch:           firstNonEmptyFingerprint(s.Arch, "arm64"),
		StainlessRuntime:        firstNonEmptyFingerprint(s.Runtime, "node"),
		StainlessRuntimeVersion: firstNonEmptyFingerprint(s.RuntimeVersion, "v24.3.0"),
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
