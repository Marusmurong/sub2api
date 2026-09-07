//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 账号定型后，出站 stainless 的 OS / Arch 跟平台走；其它身份项不变。
func TestResolveForcedFingerprintSpec_ClientPlatformDrivesOSArch(t *testing.T) {
	base := func(extra map[string]any) *Account {
		if extra == nil {
			extra = map[string]any{}
		}
		extra["enable_tls_fingerprint"] = true
		return &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: extra}
	}

	def := base(nil).resolveForcedFingerprintSpec()
	require.NotNil(t, def)
	require.Equal(t, "MacOS", def.OS)
	require.Equal(t, "arm64", def.Arch)
	require.Equal(t, "tls_profile", def.Source)

	win := base(map[string]any{accountClientPlatformKey: "windows-x64"}).resolveForcedFingerprintSpec()
	require.NotNil(t, win)
	require.Equal(t, "Windows", win.OS)
	require.Equal(t, "x64", win.Arch)
	require.Equal(t, "client_platform", win.Source)
	require.Equal(t, def.RuntimeVersion, win.RuntimeVersion, "Runtime 版本全平台相同")
	require.Equal(t, def.PackageVersion, win.PackageVersion)

	linux := base(map[string]any{accountClientPlatformKey: "linux-x64"}).resolveForcedFingerprintSpec()
	require.Equal(t, "Linux", linux.OS)
	require.Equal(t, "x64", linux.Arch)

	// 显式 fingerprint 字段仍然优先于平台
	explicit := base(map[string]any{accountClientPlatformKey: "windows-x64", "fingerprint": map[string]any{"os": "Linux", "arch": "arm64"}}).resolveForcedFingerprintSpec()
	require.Equal(t, "Linux", explicit.OS)
	require.Equal(t, "arm64", explicit.Arch)
	require.Equal(t, "extra", explicit.Source)

	// TLS 关、无 fingerprint、但定了型：仍返回身份（否则平台头无处生效），
	// 且 HasForcedFingerprint 与之同判据（ClientID 才会落库而不是只放 Redis）。
	tlsOff := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{accountClientPlatformKey: "windows-x64"}}
	require.NotNil(t, tlsOff.resolveForcedFingerprintSpec())
	require.Equal(t, "Windows", tlsOff.resolveForcedFingerprintSpec().OS)
	require.True(t, tlsOff.HasForcedFingerprint())
	untyped := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{}}
	require.False(t, untyped.HasForcedFingerprint())
	require.Nil(t, untyped.resolveForcedFingerprintSpec())

	// 未知平台值 = 未定型
	require.Equal(t, ClientPlatformUnknown, (&Account{Extra: map[string]any{accountClientPlatformKey: "amiga"}}).ClientPlatform())
}
