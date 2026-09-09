//go:build unit

package service

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
)

// wireIdentityCache 是只读的空指纹缓存：强制身份路径根本不读它，
// 但 IdentityService 需要一个非 nil 的实现。
type wireIdentityCache struct{}

func (wireIdentityCache) GetFingerprint(context.Context, int64) (*Fingerprint, error) {
	return nil, nil
}
func (wireIdentityCache) SetFingerprint(context.Context, int64, *Fingerprint) error { return nil }
func (wireIdentityCache) GetMaskedSessionID(context.Context, int64) (string, error) { return "", nil }
func (wireIdentityCache) SetMaskedSessionID(context.Context, int64, string) error   { return nil }

func newWireIdentityService() *GatewayService {
	return &GatewayService{
		cfg:             &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		identityService: NewIdentityService(wireIdentityCache{}),
	}
}

func newWireIdentityContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	// 下游自报的身份必须一律不落到出站头上
	c.Request.Header.Set("user-agent", "claude-cli/2.1.204 (external, sdk-ts, agent-sdk/0.3.204)")
	c.Request.Header.Set("x-stainless-os", "Linux")
	c.Request.Header.Set("x-stainless-arch", "arm64")
	return c
}

func typedAccount(platform string) *Account {
	return &Account{
		ID: 216, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Extra: map[string]any{
			accountClientPlatformKey:     platform,
			"enable_tls_fingerprint":     true,
			"tls_fingerprint_profile_id": float64(6),
		},
	}
}

// 分池定型的平台必须真的出现在线上出站头里。
//
// 回归的是 2026-09-08 账号 216 的缺口：resolveForcedFingerprintSpec 已经算出
// Windows/x64，但 applyClaudeCodeMimicHeaders 随后用 claude.DefaultHeaders
// 把它覆写回 MacOS/arm64，于是出站头与请求体环境块（Platform: win32）自相矛盾。
func TestClientPlatformReachesWire(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)

	for _, tc := range []struct {
		platform string
		wantOS   string
		wantArch string
	}{
		{"windows-x64", "Windows", "x64"},
		{"linux-x64", "Linux", "x64"},
		{"macos-arm64", "MacOS", "arm64"},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			svc := newWireIdentityService()
			req, _, err := svc.buildUpstreamRequest(context.Background(), newWireIdentityContext(t),
				typedAccount(tc.platform), body, "tok", "oauth", "claude-opus-5", true, true)
			require.NoError(t, err)

			require.Equal(t, tc.wantOS, getHeaderRaw(req.Header, "x-stainless-os"))
			require.Equal(t, tc.wantArch, getHeaderRaw(req.Header, "x-stainless-arch"))

			// 平台之间没有差异的身份项仍然全池统一
			require.Equal(t, claude.DefaultHeaders["User-Agent"], getHeaderRaw(req.Header, "user-agent"))
			require.Equal(t, claude.DefaultHeaders["X-Stainless-Runtime"], getHeaderRaw(req.Header, "x-stainless-runtime"))
			require.Equal(t, claude.DefaultHeaders["X-Stainless-Runtime-Version"], getHeaderRaw(req.Header, "x-stainless-runtime-version"))
			require.Equal(t, claude.DefaultHeaders["X-Stainless-Package-Version"], getHeaderRaw(req.Header, "x-stainless-package-version"))
			require.Equal(t, claude.DefaultHeaders["X-App"], getHeaderRaw(req.Header, "x-app"))
		})
	}
}

// count_tokens 与 messages 打同一批账号，平台头漏在这条路径上等于留了个反例。
func TestClientPlatformReachesWire_CountTokens(t *testing.T) {
	svc := newWireIdentityService()
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)
	req, _, err := svc.buildCountTokensRequest(context.Background(), newWireIdentityContext(t),
		typedAccount("windows-x64"), body, "tok", "oauth", "claude-opus-5", true)
	require.NoError(t, err)
	require.Equal(t, "Windows", getHeaderRaw(req.Header, "x-stainless-os"))
	require.Equal(t, "x64", getHeaderRaw(req.Header, "x-stainless-arch"))
}

// 未定型、无强制身份的遗留账号：出站身份保持全池统一，不得把客户端的 Linux/arm64 带上线。
func TestUntypedAccountKeepsPoolWideIdentity(t *testing.T) {
	svc := newWireIdentityService()
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)
	acct := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	req, _, err := svc.buildUpstreamRequest(context.Background(), newWireIdentityContext(t),
		acct, body, "tok", "oauth", "claude-opus-5", true, true)
	require.NoError(t, err)
	require.Equal(t, claude.DefaultHeaders["X-Stainless-OS"], getHeaderRaw(req.Header, "x-stainless-os"))
	require.Equal(t, claude.DefaultHeaders["X-Stainless-Arch"], getHeaderRaw(req.Header, "x-stainless-arch"))
}

// 出站 UA 与 body 里的 cc_version 必须同源：显式改了 cli_version 的账号两处都要跟着变。
func TestForcedIdentityKeepsBillingVersionInSync(t *testing.T) {
	acct := typedAccount("windows-x64")
	acct.Extra[extraKeyFingerprint] = map[string]any{"cli_version": "2.1.250"}

	svc := newWireIdentityService()
	fp, err := svc.identityService.GetOrCreateFingerprint(context.Background(), acct, http.Header{})
	require.NoError(t, err)
	forced := forcedWireFingerprint(acct, fp)
	require.NotNil(t, forced)
	require.Equal(t, "claude-cli/2.1.250 (external, cli)", forced.UserAgent)
	require.Equal(t, forced.UserAgent, effectiveBillingUserAgent("oauth", true, fp, forced))
}

// 最强的回归：把请求序列化成 wire bytes，直接断言线上真正发出去的字节。
// 只断言 header map 不够——出站头按真实 wire casing 存放，读法一错就会得出
// 相反的结论（2026-09-09 的 [ClaudeMimicDebug] 日志就是这么漏掉 OS 字段的）。
func TestClientPlatformWireBytes(t *testing.T) {
	svc := newWireIdentityService()
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)
	req, _, err := svc.buildUpstreamRequest(context.Background(), newWireIdentityContext(t),
		typedAccount("windows-x64"), body, "tok", "oauth", "claude-opus-5", true, true)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, req.Write(&buf))
	wire := buf.String()
	require.Contains(t, wire, "X-Stainless-OS: Windows\r\n")
	require.Contains(t, wire, "X-Stainless-Arch: x64\r\n")
	require.False(t, strings.Contains(wire, "MacOS"), "windows-x64 账号的出站字节里不应出现 MacOS")
	require.False(t, strings.Contains(wire, "arm64"), "windows-x64 账号的出站字节里不应出现 arm64")

	// 诊断日志必须能看见 OS —— 它是我们核对分池是否生效的唯一线上手段
	line := buildClaudeMimicDebugLine(req, body, typedAccount("windows-x64"), "oauth", true)
	require.Contains(t, line, `x-stainless-os="Windows"`)
}
