package adminprobe

import (
	"net/http"
	"testing"
)

func TestApplyAndIsTrusted(t *testing.T) {
	h := make(http.Header)
	if IsTrusted(h) {
		t.Fatal("empty header must not be trusted")
	}
	Apply(h)
	if !IsTrusted(h) {
		t.Fatal("after Apply, header must be trusted")
	}
	if h.Get(HeaderName) != Token() {
		t.Fatalf("header value = %q, want Token()", h.Get(HeaderName))
	}
}

func TestSpoofedValueRejected(t *testing.T) {
	h := make(http.Header)
	h.Set(HeaderName, "1")
	if IsTrusted(h) {
		t.Fatal("fixed spoof value must not be trusted")
	}
	h.Set(HeaderName, "not-the-token")
	if IsTrusted(h) {
		t.Fatal("wrong token must not be trusted")
	}
}

func TestApplyOverwritesExisting(t *testing.T) {
	h := make(http.Header)
	h.Set(HeaderName, "spoofed")
	// Non-canonical casing should also be cleared.
	h["x-sub2api-admin-probe"] = []string{"also-spoofed"}
	Apply(h)
	if !IsTrusted(h) {
		t.Fatal("Apply must overwrite spoofed values")
	}
	if h.Get(HeaderName) == "spoofed" {
		t.Fatal("spoofed value still present")
	}
}

func TestConfigureSharedSecret(t *testing.T) {
	// Snapshot and restore so other package tests keep a valid token.
	prev := Token()
	t.Cleanup(func() {
		mu.Lock()
		token = prev
		mu.Unlock()
	})

	Configure("shared-secret-for-cluster")
	h1 := make(http.Header)
	Apply(h1)
	// Simulate another instance with same Configure input.
	Configure("shared-secret-for-cluster")
	if !IsTrusted(h1) {
		t.Fatal("same Configure input must accept prior Apply")
	}
	Configure("different-secret")
	if IsTrusted(h1) {
		t.Fatal("different Configure input must reject prior Apply")
	}
}

func TestConfigureEmptyNoOp(t *testing.T) {
	prev := Token()
	Configure("")
	Configure("   ")
	if Token() != prev {
		t.Fatal("empty Configure must be no-op")
	}
}
