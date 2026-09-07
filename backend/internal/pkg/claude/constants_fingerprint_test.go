//go:build unit

package claude

import "testing"

// These headers are the environment every mimicked request claims to run in.
// They are a local deviation from upstream, so they get a test: a rebase onto a
// newer sub2api silently reverting them would be invisible otherwise, and the
// symptom — accounts being flagged as third-party weeks later — gives no hint
// as to the cause.
func TestDefaultHeadersMimicAPlausibleEnvironment(t *testing.T) {
	want := map[string]string{
		// Measured against the fingerprints real clients present to this
		// gateway: 20 of 23 samples reported Windows/x64 and 3 MacOS/arm64.
		// Not one reported Linux/arm64, which is what upstream ships.
		"X-Stainless-OS":   "MacOS",
		"X-Stainless-Arch": "arm64",
		// 2026-09-07 本机抓包（2.1.257 Bun 原生 arm64 直连假端点）实测值。线上 TLS
		// profile 6 与该客户端的 ClientHello 逐字段一致，因此 HTTP 层可以直接照抄，
		// 不再需要为旧的 Node 24 profile 压低版本。改这些值必须重新抓包。
		"X-Stainless-Runtime":         "node",
		"X-Stainless-Runtime-Version": "v26.3.0",
		"X-Stainless-Package-Version": "0.112.1",
		"Accept-Encoding":             "gzip, deflate, br, zstd",
		"Connection":                  "keep-alive",
	}
	for k, v := range want {
		if got := DefaultHeaders[k]; got != v {
			t.Errorf("DefaultHeaders[%q] = %q, want %q", k, got, v)
		}
	}
}

// The billing attribution block embeds cc_version, and the header advertises a
// version too. Upstream's own comment says a mismatch gets the request judged
// third-party, so the two are pinned together here rather than trusted to stay
// in step by hand.
func TestUserAgentMatchesCLIVersionConstant(t *testing.T) {
	want := "claude-cli/" + CLICurrentVersion + " (external, cli)"
	if got := DefaultHeaders["User-Agent"]; got != want {
		t.Fatalf("User-Agent = %q, want %q", got, want)
	}
}
