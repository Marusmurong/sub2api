package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 用例的 fixture 里本就带着 cch=00000;——它一直存在于真实流量中。
// 这里以透传口径（recomputeFingerprint=false）验证版本同步与幂等性。
func TestNormalizeBillingHeaderBlockSyncsVersion(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		userAgent string
		wantSub   string // substring expected in result
		unchanged bool   // expect body to remain the same
	}{
		{
			// recomputeFingerprint=false（透传路径）：只同步 semver，保留客户端原 fp。
			//
			// 上游 v0.2.1 的 syncBillingHeaderVersion 在这里无条件重算 fp。我们不采纳：
			// 透传路径的 block 由真实客户端生成，其 fp 取样口径含 transcript 的 isMeta
			// 过滤，从 API body 无法复现——重算只会把一个原本正确的值改错。
			// mimic 路径（block 由我们构造）才需要重算，见下一个用例。
			name:      "replaces cc_version but keeps client fingerprint on passthrough",
			body:      `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81.df2; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}],"messages":[]}`,
			userAgent: "claude-cli/2.1.22 (external, cli)",
			wantSub:   "cc_version=2.1.22.df2",
		},
		{
			name:      "no billing header in system",
			body:      `{"system":[{"type":"text","text":"You are Claude Code."}],"messages":[]}`,
			userAgent: "claude-cli/2.1.22",
			unchanged: true,
		},
		{
			name:      "no system field",
			body:      `{"messages":[]}`,
			userAgent: "claude-cli/2.1.22",
			unchanged: true,
		},
		{
			name:      "user-agent without version",
			body:      `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81; cc_entrypoint=cli; cch=00000;"}],"messages":[]}`,
			userAgent: "Mozilla/5.0",
			unchanged: true,
		},
		{
			name:      "empty user-agent",
			body:      `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81; cc_entrypoint=cli; cch=00000;"}],"messages":[]}`,
			userAgent: "",
			unchanged: true,
		},
		{
			name:      "version already matches",
			body:      `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.22; cc_entrypoint=cli; cch=00000;"}],"messages":[]}`,
			userAgent: "claude-cli/2.1.22",
			unchanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeBillingHeaderBlock([]byte(tt.body), tt.userAgent, false, "")
			if tt.unchanged {
				assert.Equal(t, tt.body, string(result), "body should remain unchanged")
			} else {
				assert.Contains(t, string(result), tt.wantSub)
				// Ensure old semver is gone
				assert.NotContains(t, string(result), "cc_version=2.1.81")
			}
		})
	}
}

// 版本同步与 cch 补齐互不干扰：缺 cch 的 block 在同步版本的同时被补齐。
func TestNormalizeBillingHeaderBlockSyncsVersionAndAddsCCH(t *testing.T) {
	body := `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81.df2; cc_entrypoint=cli;"}],"messages":[]}`

	result := string(normalizeBillingHeaderBlock([]byte(body), "claude-cli/2.1.220 (external, cli)", false, ""))

	assert.Contains(t, result, "cc_version=2.1.220.df2")
	assert.Contains(t, result, " cch=00000;")
	assert.NotContains(t, result, "cc_version=2.1.81")
}

// 采自上游 v0.2.1：验证「实际发出的 UA」与「body 里 billing block 的 cc_version」
// 始终一致，覆盖 mimic / passthrough / 指纹开关关闭三类路径与两个端点。
// 该契约我们原先没有对应用例，是这次合并里值得吸收的部分。
func TestBuildOAuthRequest_BillingMatchesWireUserAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"messages", "count_tokens"} {
		for _, tc := range []struct {
			name      string
			mimic     bool
			identity  bool
			disableFP bool
		}{
			{name: "mimic_overrides_cached_version", mimic: true, identity: true},
			{name: "mimic_without_identity", mimic: true},
			{name: "mimic_with_fingerprint_disabled", mimic: true, identity: true, disableFP: true},
			{name: "passthrough_uses_cached_version", identity: true},
		} {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				resetGatewayForwardingSettingsCacheForTest(t)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				body := []byte(`{"model":"claude-haiku-4-5","system":[{"type":"text","text":""}],"messages":[{"role":"user","content":"hello world"}]}`)
				billing, err := buildBillingAttributionText(body, "2.1.81")
				require.NoError(t, err)
				body, err = sjson.SetBytes(body, "system.0.text", billing)
				require.NoError(t, err)

				cfg := &config.Config{}
				svc := &GatewayService{cfg: cfg}
				cachedUA := "claude-cli/2.9.0 (external, cli)"
				if tc.identity {
					svc.identityService = NewIdentityService(&stubIdentityCache{fingerprint: &Fingerprint{
						UserAgent: cachedUA, ClientID: "test-client", UpdatedAt: time.Now().Unix(),
					}})
				}
				if tc.disableFP {
					svc.settingService = NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
						SettingKeyEnableFingerprintUnification: "false",
					}}, cfg)
				}
				account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
				var req *http.Request
				var wireBody []byte
				if endpoint == "messages" {
					req, wireBody, err = svc.buildUpstreamRequest(context.Background(), c, account,
						body, "test-token", "oauth", "claude-haiku-4-5", false, tc.mimic)
				} else {
					req, wireBody, err = svc.buildCountTokensRequest(context.Background(), c, account,
						body, "test-token", "oauth", "claude-haiku-4-5", tc.mimic)
				}
				require.NoError(t, err)
				defer func() { require.NoError(t, req.Body.Close()) }()
				wantUA := cachedUA
				if tc.mimic {
					wantUA = claude.DefaultHeaders["User-Agent"]
				}
				require.Equal(t, wantUA, getHeaderRaw(req.Header, "User-Agent"))
				version := ExtractCLIVersion(wantUA)
				billingText := gjson.GetBytes(wireBody, "system.0.text").String()

				// 本用例的核心契约：body 里的 cc_version semver 必须等于实际发出的 UA 版本。
				// 这一条对 mimic 与 passthrough 两条路径都成立。
				require.Contains(t, billingText, "cc_version="+version+".")

				// fp 后缀只在 mimic 路径按最终 body 重算：那条路径的 block 由我们构造，
				// 构造后 body 仍会被改写，不重算就与最终发出的 body 不一致。
				// passthrough 的 block 来自真实客户端，其 fp 取样含 transcript 的 isMeta
				// 过滤、无法从 API body 复现，重算反而引入偏差，故保留客户端原值。
				if tc.mimic {
					require.Contains(t, billingText,
						"cc_version="+version+"."+computeClaudeCodeFingerprint(wireBody, version)+";")
				}
				actualBody, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				require.Equal(t, wireBody, actualBody)
			})
		}
	}
}
