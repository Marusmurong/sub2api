package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

type memoryIdentityCache struct {
	fp map[int64]*Fingerprint
}

func (c *memoryIdentityCache) GetFingerprint(_ context.Context, accountID int64) (*Fingerprint, error) {
	if c.fp == nil {
		return nil, nil
	}
	if v, ok := c.fp[accountID]; ok {
		cp := *v
		return &cp, nil
	}
	return nil, nil
}

func (c *memoryIdentityCache) SetFingerprint(_ context.Context, accountID int64, fp *Fingerprint) error {
	if c.fp == nil {
		c.fp = map[int64]*Fingerprint{}
	}
	cp := *fp
	c.fp[accountID] = &cp
	return nil
}

func (c *memoryIdentityCache) GetMaskedSessionID(context.Context, int64) (string, error) {
	return "", nil
}

func (c *memoryIdentityCache) SetMaskedSessionID(context.Context, int64, string) error {
	return nil
}

func TestGetOrCreateFingerprint_ForcedExtraIgnoresClientHeaders(t *testing.T) {
	cache := &memoryIdentityCache{}
	// Poison redis with a contradictory Windows fingerprint.
	cache.fp = map[int64]*Fingerprint{
		34: {
			ClientID:                "old-windows-client-id",
			UserAgent:               "claude-cli/2.1.220 (external, cli)",
			StainlessOS:             "Windows",
			StainlessArch:           "x64",
			StainlessRuntime:        "node",
			StainlessRuntimeVersion: "v26.3.0",
			StainlessPackageVersion: "0.94.0",
			StainlessLang:           "js",
		},
	}
	svc := NewIdentityService(cache)

	account := &Account{
		ID:       34,
		Name:     "claude-f59119b5",
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"enable_tls_fingerprint":     true,
			"tls_fingerprint_profile_id": float64(3),
			"fingerprint": map[string]any{
				"os":              "MacOS",
				"arch":            "arm64",
				"runtime":         "node",
				"runtime_version": "v24.3.0",
				"package_version": "0.94.0",
				"cli_version":     "2.1.220",
				"ua_suffix":       "(external, cli)",
				// sticky client_id from extra wins over redis only if set —
				// here we omit to prove redis ClientID is reused while OS is forced.
			},
		},
	}

	headers := http.Header{}
	headers.Set("User-Agent", "claude-cli/2.1.999 (external, cli)")
	headers.Set("X-Stainless-OS", "Windows")
	headers.Set("X-Stainless-Arch", "x64")
	headers.Set("X-Stainless-Runtime-Version", "v26.3.0")

	fp, err := svc.GetOrCreateFingerprint(context.Background(), account, headers)
	require.NoError(t, err)
	require.Equal(t, "MacOS", fp.StainlessOS)
	require.Equal(t, "arm64", fp.StainlessArch)
	require.Equal(t, "v24.3.0", fp.StainlessRuntimeVersion)
	require.Equal(t, "claude-cli/2.1.220 (external, cli)", fp.UserAgent)
	// ClientID stays sticky from redis.
	require.Equal(t, "old-windows-client-id", fp.ClientID)
	// Redis rewritten to forced identity (not Windows).
	require.Equal(t, "MacOS", cache.fp[34].StainlessOS)
}

func TestGetOrCreateFingerprint_TLSEnabledWithoutExtraFingerprintAlignsMacOS(t *testing.T) {
	cache := &memoryIdentityCache{}
	svc := NewIdentityService(cache)
	account := &Account{
		ID:       36,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"enable_tls_fingerprint":     true,
			"tls_fingerprint_profile_id": float64(5),
		},
	}
	headers := http.Header{}
	headers.Set("User-Agent", "claude-cli/2.1.220 (external, cli)")
	headers.Set("X-Stainless-OS", "Windows")
	headers.Set("X-Stainless-Arch", "x64")

	fp, err := svc.GetOrCreateFingerprint(context.Background(), account, headers)
	require.NoError(t, err)
	require.Equal(t, "MacOS", fp.StainlessOS)
	require.Equal(t, "arm64", fp.StainlessArch)
	require.Equal(t, "v24.3.0", fp.StainlessRuntimeVersion)
	require.NotEmpty(t, fp.ClientID)
}

func TestGetOrCreateFingerprint_LegacyStillSeedsFromClient(t *testing.T) {
	cache := &memoryIdentityCache{}
	svc := NewIdentityService(cache)
	account := &Account{
		ID:       99,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{}, // no TLS, no fingerprint
	}

	headers := http.Header{}
	headers.Set("User-Agent", "claude-cli/2.1.220 (external, cli)")
	headers.Set("X-Stainless-OS", "Windows")
	headers.Set("X-Stainless-Arch", "x64")
	headers.Set("X-Stainless-Runtime-Version", "v26.3.0")

	fp, err := svc.GetOrCreateFingerprint(context.Background(), account, headers)
	require.NoError(t, err)
	require.Equal(t, "Windows", fp.StainlessOS)
	require.Equal(t, "x64", fp.StainlessArch)
	require.Equal(t, "v26.3.0", fp.StainlessRuntimeVersion)
}

func TestResolveForcedFingerprint_ExtraClientIDPreferred(t *testing.T) {
	cache := &memoryIdentityCache{
		fp: map[int64]*Fingerprint{
			1: {ClientID: "from-redis"},
		},
	}
	svc := NewIdentityService(cache)
	account := &Account{
		ID:       1,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"fingerprint": map[string]any{
				"os":        "MacOS",
				"arch":      "arm64",
				"client_id": "from-extra-fixed",
			},
		},
	}
	fp, err := svc.GetOrCreateFingerprint(context.Background(), account, http.Header{})
	require.NoError(t, err)
	require.Equal(t, "from-extra-fixed", fp.ClientID)
	require.Equal(t, "MacOS", fp.StainlessOS)
}

func TestCoerceOSArch_WindowsForcesX64(t *testing.T) {
	os, arch := coerceOSArch("windows", "arm64")
	require.Equal(t, "Windows", os)
	require.Equal(t, "x64", arch)
}
