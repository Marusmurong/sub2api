//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Anthropic 撤销授权时返回 401 "OAuth access token has been revoked."。
//
// 此前按「撤销必然连带作废 refresh_token」的假设直接永久禁用。2026-08-08 生产证伪：
//
//	11:23:23  账号 117 收到该 401 → 被永久禁用
//	13:36:26  同一账号在后台点「测试」即恢复 active
//
// 账号 118 / 119 同日同样表现。三个号的 refresh_token 当时都还有效——被撤销的只是
// access token，而 Anthropic 对这两种情况返回同一句话，从文案上分不出来。
//
// 误判的代价是双向的：一个还能用的订阅号立刻停止调度，Supply 侧同步标记 error 后
// 又是单向终态，于是账号池留下永远不会自愈的假封号，还连带停掉该号的日结算。
//
// 现在的判据是刷新结果而不是错误文案：走 OAuth 自愈路径（失效缓存 + 临时不可调度），
// 由 token_refresh_service 带分布式锁刷一次。真被撤销的账号刷新会拿到 non-retryable
// 错误，那里同样 SetError 永久禁用，安全网仍在。

func anthropicRevoked401Body(msg string) []byte {
	return []byte(`{"type":"error","error":{"type":"authentication_error","message":"` + msg + `"}}`)
}

// 核心回归：带 refresh_token 的账号收到 revoked 401 时，不得再被就地永久禁用。
func TestHandleUpstreamError_AnthropicRevokedDefersToRefresh(t *testing.T) {
	repo := &errorPolicyRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 20, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"refresh_token": "rt-still-present"}}

	shouldDisable := svc.HandleUpstreamError(context.Background(), account, 401,
		http.Header{}, anthropicRevoked401Body("OAuth access token has been revoked."))

	// shouldDisable 的语义是「这次别再用这个号」（换号），临时不可调度同样返回 true。
	// 永久与临时的分界在 SetError vs SetTempUnschedulable，断言必须落在这两个上。
	require.True(t, shouldDisable, "本次请求仍应换号")
	require.Zero(t, repo.setErrCalls,
		"revoked 401 不得就地永久禁用 —— 生产上已证实 refresh_token 可能仍然有效")
	require.Equal(t, 1, repo.tempCalls,
		"应临时不可调度，留出刷新窗口，由刷新结果判定生死")
}

// 边界：没有 refresh_token 的账号确实无法自愈，必须仍然直接禁用。
// 否则改动会留下一批永远不死、每轮冷却结束后再发一次无意义 502 的废号。
func TestHandleUpstreamError_AnthropicRevokedWithoutRefreshTokenStillDisables(t *testing.T) {
	repo := &errorPolicyRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 24, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{}} // 无 refresh_token

	svc.HandleUpstreamError(context.Background(), account, 401,
		http.Header{}, anthropicRevoked401Body("OAuth access token has been revoked."))

	require.Equal(t, 1, repo.setErrCalls, "无 refresh_token 无法自愈，应直接禁用")
	require.Zero(t, repo.tempCalls)
}

// 仅凭据过期的普通 401 保持原有自愈路径，不受本次改动影响。
func TestHandleUpstreamError_AnthropicOrdinary401StillSelfHeals(t *testing.T) {
	repo := &errorPolicyRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 21, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"refresh_token": "rt-still-present"}}

	svc.HandleUpstreamError(context.Background(), account, 401,
		http.Header{}, anthropicRevoked401Body("Invalid authentication credentials"))

	require.Zero(t, repo.setErrCalls, "普通 401 不应永久禁用")
	require.Equal(t, 1, repo.tempCalls, "应临时不可调度，留出刷新窗口")
}

// OpenAI 的 token_revoked 语义不同（那边确实是永久作废），其分支不得被本次改动波及。
func TestHandleUpstreamError_OpenAIRevokedStillDisablesPermanently(t *testing.T) {
	repo := &errorPolicyRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 25, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"refresh_token": "rt"}}

	svc.HandleUpstreamError(context.Background(), account, 401, http.Header{},
		[]byte(`{"error":{"code":"token_revoked","message":"revoked"}}`))

	require.Equal(t, 1, repo.setErrCalls, "OpenAI token_revoked 仍应永久禁用")
}

// 撤销判定只认 Anthropic：别的平台有各自语义，不能被这条子串误伤。
func TestHandleUpstreamError_RevokedMarkerScopedToAnthropic(t *testing.T) {
	repo := &errorPolicyRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 22, Platform: PlatformGemini, Type: AccountTypeOAuth,
		Credentials: map[string]any{"refresh_token": "rt"}}

	svc.HandleUpstreamError(context.Background(), account, 401,
		http.Header{}, anthropicRevoked401Body("OAuth access token has been revoked."))

	require.Zero(t, repo.setErrCalls, "非 Anthropic 平台不应命中该分支")
}
