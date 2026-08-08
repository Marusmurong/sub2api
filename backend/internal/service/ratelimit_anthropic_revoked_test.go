//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Anthropic 撤销授权后返回 401 "OAuth access token has been revoked."。
// 撤销会连带作废 refresh_token（后台刷新拿到 400 non-retryable），所以 OAuth 那条
// "临时不可调度等刷新自愈" 的路径救不回来 —— 只会让废号在冷却与重试之间空转，
// 并持续把已撤销的凭据暴露给上游。这里必须与 OpenAI 的 token_revoked 分支一样永久禁用。

func anthropicRevoked401Body(msg string) []byte {
	return []byte(`{"type":"error","error":{"type":"authentication_error","message":"` + msg + `"}}`)
}

func TestHandleUpstreamError_AnthropicRevokedTokenDisablesPermanently(t *testing.T) {
	repo := &errorPolicyRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 20, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"refresh_token": "rt-still-present"}}

	shouldDisable := svc.HandleUpstreamError(context.Background(), account, 401,
		http.Header{}, anthropicRevoked401Body("OAuth access token has been revoked."))

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrCalls, "应走永久禁用")
	require.Contains(t, repo.lastErrorMsg, "Token revoked (401)")
	require.Contains(t, repo.lastErrorMsg, "has been revoked", "错误信息应保留上游原文便于排查")
	require.Zero(t, repo.tempCalls, "不得再走临时不可调度：撤销后刷新救不回来")
}

// 仅凭据过期的普通 401 必须保持原有的自愈路径，否则一次网络抖动就会永久废掉活号。
func TestHandleUpstreamError_AnthropicOrdinary401StillSelfHeals(t *testing.T) {
	repo := &errorPolicyRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 21, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"refresh_token": "rt-still-present"}}

	// 注意 shouldDisable 的语义是"这次别再用这个号"（换号），临时不可调度路径同样返回 true。
	// 永久与临时的分界在 SetError vs SetTempUnschedulable，断言必须落在这两个上。
	svc.HandleUpstreamError(context.Background(), account, 401,
		http.Header{}, anthropicRevoked401Body("Invalid authentication credentials"))

	require.Zero(t, repo.setErrCalls, "普通 401 不应永久禁用")
	require.Equal(t, 1, repo.tempCalls, "应临时不可调度，留出刷新窗口")
}

// 撤销判定只认 Anthropic：别的平台有各自的语义，不能被这条子串误伤。
func TestHandleUpstreamError_RevokedMarkerScopedToAnthropic(t *testing.T) {
	repo := &errorPolicyRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 22, Platform: PlatformGemini, Type: AccountTypeOAuth,
		Credentials: map[string]any{"refresh_token": "rt"}}

	svc.HandleUpstreamError(context.Background(), account, 401,
		http.Header{}, anthropicRevoked401Body("OAuth access token has been revoked."))

	require.Zero(t, repo.setErrCalls, "非 Anthropic 平台不应命中该分支")
}

// 上游若微调措辞或大小写，仍应命中（取子串 + ToLower 的理由）。
func TestHandleUpstreamError_RevokedMarkerIsCaseInsensitiveSubstring(t *testing.T) {
	for _, msg := range []string{
		"OAuth access token HAS BEEN REVOKED.",
		"This access token has been revoked",
	} {
		t.Run(msg, func(t *testing.T) {
			repo := &errorPolicyRepoStub{}
			svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			account := &Account{ID: 23, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
				Credentials: map[string]any{"refresh_token": "rt"}}

			require.True(t, svc.HandleUpstreamError(context.Background(), account, 401,
				http.Header{}, anthropicRevoked401Body(msg)))
			require.Equal(t, 1, repo.setErrCalls)
		})
	}
}
