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
		// Held at v24.3.0 to match the TLS profile in pkg/tlsfingerprint, which
		// was captured from Node.js 24.x. Real 2.1.220 clients report v26.3.0,
		// so this is knowingly inconsistent with them — but consistent with our
		// own ClientHello, which is the comparison upstream can actually make.
		// Change it only together with a re-captured TLS profile.
		"X-Stainless-Runtime":         "node",
		"X-Stainless-Runtime-Version": "v24.3.0",
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
