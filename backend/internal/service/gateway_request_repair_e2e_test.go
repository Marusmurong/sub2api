package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 端到端验证请求修复真的作用在"最终发给上游的 body / header"上，而不是只在
// 单元函数里正确。这类测试能挡住"忘了在某条路径接上 sanitize"或"把 sanitize
// 挪到 NewRequest 之后"这类 regression —— 单测检测函数本身是发现不了的。

func newRepairTestContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return c
}

// readRepairTestUpstreamBody 读取最终写入上游请求的 body。
// 独立于 gateway_context_management_test.go 里的同类 helper：那个文件带
// //go:build unit 标签，默认 go test 不编译，本文件刻意不加标签以进入默认套件。
func readRepairTestUpstreamBody(t *testing.T, req *http.Request) []byte {
	t.Helper()
	require.NotNil(t, req.Body)
	b, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	return b
}

func newRepairTestOAuthAccount() *Account {
	return &Account{
		ID: 901, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-tok"},
		Status:      StatusActive,
		Schedulable: true,
	}
}

// —— 直连路径：采样参数剥离 ——

func TestBuildUpstreamRequest_StripsDeprecatedSamplingParamsEndToEnd(t *testing.T) {
	c := newRepairTestContext(t)
	svc := &GatewayService{cfg: &config.Config{}}

	// claude-sonnet-5 属于新世代：temperature/top_p/top_k 必须在 outgoing body 中消失
	body := []byte(`{"model":"claude-sonnet-5","temperature":0.7,"top_p":0.9,"top_k":40,"max_tokens":64,"messages":[]}`)

	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, newRepairTestOAuthAccount(), body,
		"oauth-tok", "oauth", "claude-sonnet-5", false, true,
	)
	require.NoError(t, err)

	out := readRepairTestUpstreamBody(t, req)
	require.False(t, gjson.GetBytes(out, "temperature").Exists(), "outgoing body 不得含 temperature: %s", out)
	require.False(t, gjson.GetBytes(out, "top_p").Exists(), "outgoing body 不得含 top_p: %s", out)
	require.False(t, gjson.GetBytes(out, "top_k").Exists(), "outgoing body 不得含 top_k: %s", out)
	require.Equal(t, int64(64), gjson.GetBytes(out, "max_tokens").Int(), "其余字段必须保留")
}

func TestBuildUpstreamRequest_PreservesSamplingParamsForLegacyModelEndToEnd(t *testing.T) {
	c := newRepairTestContext(t)
	svc := &GatewayService{cfg: &config.Config{}}

	// claude-opus-4-5 仍支持采样参数：单独 temperature 必须原样透传，不能静默改变生成行为
	body := []byte(`{"model":"claude-opus-4-5-20251101","temperature":0.7,"max_tokens":64,"messages":[]}`)

	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, newRepairTestOAuthAccount(), body,
		"oauth-tok", "oauth", "claude-opus-4-5-20251101", false, true,
	)
	require.NoError(t, err)

	out := readRepairTestUpstreamBody(t, req)
	require.InDelta(t, 0.7, gjson.GetBytes(out, "temperature").Float(), 1e-9,
		"老模型的 temperature 必须原样保留: %s", out)
}

func TestBuildUpstreamRequest_StripsTopPWhenBothSamplingParamsOnLegacyModelEndToEnd(t *testing.T) {
	c := newRepairTestContext(t)
	svc := &GatewayService{cfg: &config.Config{}}

	// 老模型仍支持采样，但 temperature 与 top_p 互斥：保留 temperature，删除 top_p
	body := []byte(`{"model":"claude-opus-4-5-20251101","temperature":0.7,"top_p":0.9,"max_tokens":64,"messages":[]}`)

	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, newRepairTestOAuthAccount(), body,
		"oauth-tok", "oauth", "claude-opus-4-5-20251101", false, true,
	)
	require.NoError(t, err)

	out := readRepairTestUpstreamBody(t, req)
	require.InDelta(t, 0.7, gjson.GetBytes(out, "temperature").Float(), 1e-9,
		"temperature 必须保留: %s", out)
	require.False(t, gjson.GetBytes(out, "top_p").Exists(),
		"top_p 必须被剥离（互斥）: %s", out)
	require.Equal(t, int64(64), gjson.GetBytes(out, "max_tokens").Int())
}

// —— 直连路径：schema 字段净化 ——

func TestBuildUpstreamRequest_SanitizesSchemaFieldsEndToEnd(t *testing.T) {
	c := newRepairTestContext(t)
	svc := &GatewayService{cfg: &config.Config{}}

	// stream_options 是 OpenAI 字段；haiku 不支持 effort 与 adaptive thinking
	body := []byte(`{"model":"claude-haiku-4-5","stream_options":{"include_usage":true},` +
		`"output_config":{"effort":"high"},"thinking":{"type":"adaptive"},"messages":[]}`)

	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, newRepairTestOAuthAccount(), body,
		"oauth-tok", "oauth", "claude-haiku-4-5", false, true,
	)
	require.NoError(t, err)

	out := readRepairTestUpstreamBody(t, req)
	require.False(t, gjson.GetBytes(out, "stream_options").Exists(), "stream_options 必须被删除: %s", out)
	require.False(t, gjson.GetBytes(out, "output_config").Exists(), "haiku 的 output_config 必须被删除: %s", out)
	require.Equal(t, "enabled", gjson.GetBytes(out, "thinking.type").String(),
		"haiku 的 adaptive 必须转为 enabled: %s", out)
	require.True(t, gjson.GetBytes(out, "thinking.budget_tokens").Exists(),
		"转换后必须补上 budget_tokens: %s", out)
}

func TestBuildUpstreamRequest_HoistsSystemRoleMessageEndToEnd(t *testing.T) {
	c := newRepairTestContext(t)
	svc := &GatewayService{cfg: &config.Config{}}

	body := []byte(`{"model":"claude-opus-4-8","messages":[` +
		`{"role":"system","content":"You are helpful"},{"role":"user","content":"hi"}]}`)

	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, newRepairTestOAuthAccount(), body,
		"oauth-tok", "oauth", "claude-opus-4-8", false, true,
	)
	require.NoError(t, err)

	out := readRepairTestUpstreamBody(t, req)
	require.Equal(t, "You are helpful", gjson.GetBytes(out, "system").String(),
		"system 角色消息必须提升到顶层: %s", out)
	msgs := gjson.GetBytes(out, "messages").Array()
	require.Len(t, msgs, 1, "system 消息必须从 messages 中移除: %s", out)
	require.Equal(t, "user", msgs[0].Get("role").String())
}

// —— 直连路径：computer-use beta 注入 ——

func TestBuildUpstreamRequest_InjectsComputerUseBetaEndToEnd(t *testing.T) {
	c := newRepairTestContext(t)
	svc := &GatewayService{cfg: &config.Config{}}

	body := []byte(`{"model":"claude-opus-4-8","tools":[{"type":"computer_20250124","name":"computer"}],"messages":[]}`)

	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, newRepairTestOAuthAccount(), body,
		"oauth-tok", "oauth", "claude-opus-4-8", false, true,
	)
	require.NoError(t, err)

	beta := getHeaderRaw(req.Header, "anthropic-beta")
	require.True(t, anthropicBetaTokensContains(beta, "computer-use-2025-01-24"),
		"outgoing anthropic-beta 必须含与工具版本对应的 computer-use token, 实际=%q", beta)
}

func TestBuildUpstreamRequest_NoComputerUseBetaWithoutComputerTool(t *testing.T) {
	c := newRepairTestContext(t)
	svc := &GatewayService{cfg: &config.Config{}}

	body := []byte(`{"model":"claude-opus-4-8","tools":[{"type":"web_search_20250305"}],"messages":[]}`)

	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, newRepairTestOAuthAccount(), body,
		"oauth-tok", "oauth", "claude-opus-4-8", false, true,
	)
	require.NoError(t, err)

	beta := getHeaderRaw(req.Header, "anthropic-beta")
	require.False(t, anthropicBetaTokensContains(beta, "computer-use-2025-01-24"),
		"没有 computer 工具时不得注入, 实际=%q", beta)
}

// API-key 账号在客户端未传 beta 时原本不设置该 header；只要 body 有 computer
// 工具就必须破例设置，否则必然 400。这是注入逻辑里最容易漏的一条。
func TestBuildUpstreamRequest_APIKeyWithoutClientBetaStillGetsComputerUse(t *testing.T) {
	c := newRepairTestContext(t)
	svc := &GatewayService{cfg: &config.Config{}}

	account := &Account{
		ID: 902, Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-ant-xxx"},
		Status:      StatusActive,
		Schedulable: true,
	}
	body := []byte(`{"model":"claude-opus-4-8","tools":[{"type":"computer_20251124"}],"messages":[]}`)

	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, account, body,
		"sk-ant-xxx", "apikey", "claude-opus-4-8", false, false,
	)
	require.NoError(t, err)

	beta := getHeaderRaw(req.Header, "anthropic-beta")
	require.True(t, anthropicBetaTokensContains(beta, "computer-use-2025-11-24"),
		"API-key 账号也必须注入 computer-use beta, 实际=%q", beta)
}

// —— 透传路径 ——

func TestBuildUpstreamRequestAnthropicAPIKeyPassthrough_RepairsBodyEndToEnd(t *testing.T) {
	c := newRepairTestContext(t)
	svc := &GatewayService{cfg: &config.Config{}}

	account := &Account{
		ID: 903, Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-ant-xxx"},
		Status:      StatusActive,
		Schedulable: true,
	}
	// 透传路径按 body.model 判定：sonnet-5 属新世代
	body := []byte(`{"model":"claude-sonnet-5","temperature":1,"stream_options":{"include_usage":true},"messages":[]}`)

	req, wireBody, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(
		context.Background(), c, account, body, "sk-ant-xxx",
	)
	require.NoError(t, err)
	require.NotNil(t, req)

	require.False(t, gjson.GetBytes(wireBody, "temperature").Exists(),
		"透传路径也必须剥离 temperature: %s", wireBody)
	require.False(t, gjson.GetBytes(wireBody, "stream_options").Exists(),
		"透传路径也必须删除 stream_options: %s", wireBody)
}
