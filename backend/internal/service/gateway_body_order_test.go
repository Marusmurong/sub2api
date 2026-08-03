package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type gatewayTTLSettingRepo struct {
	data map[string]string
}

func (r *gatewayTTLSettingRepo) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}

func (r *gatewayTTLSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if r == nil {
		return "", ErrSettingNotFound
	}
	v, ok := r.data[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return v, nil
}

func (r *gatewayTTLSettingRepo) Set(_ context.Context, key, value string) error {
	if r == nil {
		return errors.New("setting repo is nil")
	}
	if r.data == nil {
		r.data = map[string]string{}
	}
	r.data[key] = value
	return nil
}

func (r *gatewayTTLSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string)
	if r == nil {
		return result, nil
	}
	for _, key := range keys {
		if v, ok := r.data[key]; ok {
			result[key] = v
		}
	}
	return result, nil
}

func (r *gatewayTTLSettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	if r == nil {
		return errors.New("setting repo is nil")
	}
	if r.data == nil {
		r.data = map[string]string{}
	}
	for key, value := range settings {
		r.data[key] = value
	}
	return nil
}

func (r *gatewayTTLSettingRepo) GetAll(context.Context) (map[string]string, error) {
	result := make(map[string]string)
	if r == nil {
		return result, nil
	}
	for key, value := range r.data {
		result[key] = value
	}
	return result, nil
}

func (r *gatewayTTLSettingRepo) Delete(_ context.Context, key string) error {
	if r != nil {
		delete(r.data, key)
	}
	return nil
}

func assertJSONTokenOrder(t *testing.T, body string, tokens ...string) {
	t.Helper()

	last := -1
	for _, token := range tokens {
		pos := strings.Index(body, token)
		require.NotEqualf(t, -1, pos, "missing token %s in body %s", token, body)
		require.Greaterf(t, pos, last, "token %s should appear after previous tokens in body %s", token, body)
		last = pos
	}
}

func TestReplaceModelInBody_PreservesTopLevelFieldOrder(t *testing.T) {
	svc := &GatewayService{}
	body := []byte(`{"alpha":1,"model":"claude-3-5-sonnet-latest","messages":[],"omega":2}`)

	result := svc.replaceModelInBody(body, "claude-3-5-sonnet-20241022")
	resultStr := string(result)

	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"model"`, `"messages"`, `"omega"`)
	require.Contains(t, resultStr, `"model":"claude-3-5-sonnet-20241022"`)
}

func TestNormalizeClaudeOAuthRequestBody_PreservesTopLevelFieldOrder(t *testing.T) {
	body := []byte(`{"alpha":1,"model":"claude-3-5-sonnet-latest","temperature":0.2,"system":"You are OpenCode, the best coding agent on the planet.","messages":[],"tool_choice":{"type":"auto"},"omega":2}`)

	result, modelID := normalizeClaudeOAuthRequestBody(body, "claude-3-5-sonnet-latest", claudeOAuthNormalizeOptions{
		injectMetadata: true,
		metadataUserID: "user-1",
	})
	resultStr := string(result)

	require.Equal(t, claude.NormalizeModelID("claude-3-5-sonnet-latest"), modelID)
	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"model"`, `"temperature"`, `"system"`, `"messages"`, `"omega"`, `"tools"`, `"metadata"`, `"max_tokens"`)
	require.Contains(t, resultStr, `"temperature":0.2`)
	require.Contains(t, resultStr, `"system":"`+claudeCodeSystemPrompt+`"`)
	// tools 不再补空数组，而是注入真实 Claude Code 的核心工具集：`tools: []` 是真实
	// 客户端从不产生的形态，等于自证不是编码 agent。见 claudecode_core_tools.go。
	require.NotContains(t, resultStr, `"tools":[]`)
	require.Contains(t, resultStr, `"name":"Bash"`)
	// 随之必须把 tool_choice 覆盖成 none：客户端原来的 auto 指向的是一个不存在的工具集，
	// 注入之后若保留，模型会调用这些工具并返回 tool_use，而下游并未实现它们。
	require.Contains(t, resultStr, `"tool_choice":{"type":"none"}`)
	require.Contains(t, resultStr, `"metadata":{"user_id":"user-1"}`)
	require.Contains(t, resultStr, `"max_tokens":128000`)
}

func TestInjectClaudeCodePrompt_PreservesFieldOrder(t *testing.T) {
	body := []byte(`{"alpha":1,"system":[{"id":"block-1","type":"text","text":"Custom"}],"messages":[],"omega":2}`)

	result := injectClaudeCodePrompt(body, []any{
		map[string]any{"id": "block-1", "type": "text", "text": "Custom"},
	})
	resultStr := string(result)

	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"system"`, `"messages"`, `"omega"`)
	require.Contains(t, resultStr, `{"id":"block-1","type":"text","text":"`+claudeCodeSystemPrompt+`\n\nCustom"}`)
}

func TestEnforceCacheControlLimit_PreservesTopLevelFieldOrder(t *testing.T) {
	body := []byte(`{"alpha":1,"system":[{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}},{"type":"text","text":"s2","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"m1","cache_control":{"type":"ephemeral"}},{"type":"text","text":"m2","cache_control":{"type":"ephemeral"}},{"type":"text","text":"m3","cache_control":{"type":"ephemeral"}}]}],"omega":2}`)

	result := enforceCacheControlLimit(body)
	resultStr := string(result)

	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"system"`, `"messages"`, `"omega"`)
	require.Equal(t, 4, strings.Count(resultStr, `"cache_control"`))
}

func TestEnforceCacheControlLimit_CountsToolsAndPreservesMessageAnchorsFirst(t *testing.T) {
	body := []byte(`{"alpha":1,"system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"m1","cache_control":{"type":"ephemeral"}},{"type":"text","text":"m2","cache_control":{"type":"ephemeral"}},{"type":"text","text":"m3","cache_control":{"type":"ephemeral"}}]}],"tools":[{"name":"a","input_schema":{},"cache_control":{"type":"ephemeral"}}],"omega":2}`)

	result := enforceCacheControlLimit(body)
	resultStr := string(result)

	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"system"`, `"messages"`, `"tools"`, `"omega"`)
	require.Equal(t, 4, strings.Count(resultStr, `"cache_control"`))
	require.True(t, gjson.GetBytes(result, "system.0.cache_control").Exists())
	require.True(t, gjson.GetBytes(result, "messages.0.content.0.cache_control").Exists())
	require.True(t, gjson.GetBytes(result, "messages.0.content.1.cache_control").Exists())
	require.True(t, gjson.GetBytes(result, "messages.0.content.2.cache_control").Exists())
	require.False(t, gjson.GetBytes(result, "tools.0.cache_control").Exists())
}

func TestInjectAnthropicCacheControlTTL1h_OnlyUpdatesExistingEphemeralCacheControl(t *testing.T) {
	body := []byte(`{"alpha":1,"cache_control":{"type":"ephemeral"},"system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral","ttl":"5m"}},{"type":"text","text":"plain"}],"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}},{"type":"text","text":"non","cache_control":{"type":"persistent","ttl":"5m"}}]}],"tools":[{"name":"a","input_schema":{},"cache_control":{"type":"ephemeral"}}],"omega":2}`)

	result := injectAnthropicCacheControlTTL1h(body)
	resultStr := string(result)

	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"cache_control"`, `"system"`, `"messages"`, `"tools"`, `"omega"`)
	require.Equal(t, "1h", gjson.GetBytes(result, "cache_control.ttl").String())
	require.Equal(t, "1h", gjson.GetBytes(result, "system.0.cache_control.ttl").String())
	require.False(t, gjson.GetBytes(result, "system.1.cache_control").Exists())
	require.Equal(t, "1h", gjson.GetBytes(result, "messages.0.content.0.cache_control.ttl").String())
	require.Equal(t, "5m", gjson.GetBytes(result, "messages.0.content.1.cache_control.ttl").String())
	require.Equal(t, "1h", gjson.GetBytes(result, "tools.0.cache_control.ttl").String())
}

func TestGatewayCacheTTLGlobalSetting_TargetResolution(t *testing.T) {
	repo := &gatewayTTLSettingRepo{data: map[string]string{
		SettingKeyEnableAnthropicCacheTTL1hInjection: "true",
	}}
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	svc := &GatewayService{
		settingService: NewSettingService(repo, &config.Config{}),
	}
	account := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	target, ok := svc.resolveCacheTTLUsageOverrideTarget(context.Background(), account)
	require.True(t, ok)
	require.Equal(t, cacheTTLTarget5m, target)

	account.Extra = map[string]any{
		"cache_ttl_override_enabled": true,
		"cache_ttl_override_target":  "1h",
	}
	target, ok = svc.resolveCacheTTLUsageOverrideTarget(context.Background(), account)
	require.True(t, ok)
	require.Equal(t, cacheTTLTarget1h, target)
}

func TestGatewayCacheTTLGlobalSetting_RequestInjectionScope(t *testing.T) {
	repo := &gatewayTTLSettingRepo{data: map[string]string{
		SettingKeyEnableAnthropicCacheTTL1hInjection: "true",
	}}
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	svc := &GatewayService{
		settingService: NewSettingService(repo, &config.Config{}),
	}

	require.True(t, svc.shouldInjectAnthropicCacheTTL1h(context.Background(), &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}))
	require.True(t, svc.shouldInjectAnthropicCacheTTL1h(context.Background(), &Account{Platform: PlatformAnthropic, Type: AccountTypeSetupToken}))
	require.False(t, svc.shouldInjectAnthropicCacheTTL1h(context.Background(), &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}))
	require.False(t, svc.shouldInjectAnthropicCacheTTL1h(context.Background(), &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}))

	repo.data[SettingKeyEnableAnthropicCacheTTL1hInjection] = "false"
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	require.False(t, svc.shouldInjectAnthropicCacheTTL1h(context.Background(), &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}))
}
