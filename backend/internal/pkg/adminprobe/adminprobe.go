// Package adminprobe marks outbound requests that originate from admin UI
// connectivity checks (account test, channel monitor) so the gateway can
// skip HI/liveness intercept while still blocking external probe traffic.
//
// The marker value is an unguessable process-local token (optionally replaced
// by a shared secret for multi-instance deployments). Clients cannot spoof
// bypass by setting a fixed header value.
package adminprobe

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
)

// HeaderName is the HTTP header used to mark trusted admin probes.
// It is intentionally not advertised; value is a secret, not a boolean flag.
const HeaderName = "X-Sub2API-Admin-Probe"

// ConnectivityTestUserText is the fixed user message used by Admin account
// connectivity tests (AccountTestService). The gateway also treats this exact
// sole-user-text shape as an admin probe so self-looped tests cannot be
// mistaken for external HI liveness traffic.
const ConnectivityTestUserText = "What does the git status command show?"

// UserAgent is set on admin-initiated outbound probes that would otherwise use
// Go's default client UA. Must NOT contain markers from isProbeUserAgent
// ("probe", "monitor", "healthcheck", ...).
const UserAgent = "Sub2API-AdminCheck/1.0"

var (
	mu    sync.RWMutex
	token = generateToken()
)

func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// Extremely unlikely; fall back so the package still works in tests.
		sum := sha256.Sum256([]byte("adminprobe-fallback"))
		return hex.EncodeToString(sum[:])
	}
	return hex.EncodeToString(b)
}

// Configure replaces the process-local token with a shared secret so multi-
// instance deployments accept each other's admin probes. Empty shared is a no-op.
// Prefer a long random value (e.g. JWT secret); short inputs are hashed.
func Configure(shared string) {
	shared = strings.TrimSpace(shared)
	if shared == "" {
		return
	}
	// Always hash so the wire value is not the raw JWT secret if logs leak headers.
	sum := sha256.Sum256([]byte("sub2api-admin-probe-v1:" + shared))
	mu.Lock()
	token = hex.EncodeToString(sum[:])
	mu.Unlock()
}

// Token returns the current marker value (for tests).
func Token() string {
	mu.RLock()
	defer mu.RUnlock()
	return token
}

// Apply sets the trusted admin-probe marker on an outbound request.
// Call after any account-level header overrides so the marker cannot be stripped.
//
// Also normalizes bare Go default User-Agents so logs can attribute admin checks,
// without matching gateway isProbeUserAgent markers.
func Apply(h http.Header) {
	if h == nil {
		return
	}
	mu.RLock()
	v := token
	mu.RUnlock()
	// Drop any client/override casing of the same header first.
	for existing := range h {
		if strings.EqualFold(existing, HeaderName) {
			delete(h, existing)
		}
	}
	h.Set(HeaderName, v)

	ua := strings.TrimSpace(h.Get("User-Agent"))
	if ua == "" || strings.HasPrefix(ua, "Go-http-client/") {
		h.Set("User-Agent", UserAgent)
	}
}

// IsTrusted reports whether the request carries a valid admin-probe marker.
func IsTrusted(h http.Header) bool {
	if h == nil {
		return false
	}
	got := strings.TrimSpace(h.Get(HeaderName))
	if got == "" {
		return false
	}
	mu.RLock()
	want := token
	mu.RUnlock()
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
