package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeReclaudeUsageStore 记录写入的水位增量。
type fakeReclaudeUsageStore struct {
	deltas []ReclaudeDailyUsage
	total  ReclaudeDailyUsage
}

func (f *fakeReclaudeUsageStore) AddReclaudeDailyUsage(
	_ context.Context, _ int64, _ string, delta ReclaudeDailyUsage,
) (ReclaudeDailyUsage, error) {
	f.deltas = append(f.deltas, delta)
	f.total.Tokens += delta.Tokens
	f.total.UpstreamCalls += delta.UpstreamCalls
	f.total.FailedUpstreamCalls += delta.FailedUpstreamCalls
	return f.total, nil
}

func (f *fakeReclaudeUsageStore) GetReclaudeDailyUsage(
	_ context.Context, _ int64, _ string,
) (ReclaudeDailyUsage, error) {
	return f.total, nil
}

func TestReclaudeBillableTokens(t *testing.T) {
	usage := ClaudeUsage{
		InputTokens:              100,
		OutputTokens:             20,
		CacheCreationInputTokens: 7,
		CacheReadInputTokens:     3,
		// 5m / 1h 是 CacheCreationInputTokens 的**拆分**，再加一次就是重复计数。
		CacheCreation5mTokens: 7,
		CacheCreation1hTokens: 0,
	}

	require.Equal(t, int64(130), ReclaudeBillableTokens(usage))
}

func TestReclaudeUpstream_RecordsFailedCalls(t *testing.T) {
	t.Run("传输失败记一次失败调用", func(t *testing.T) {
		account, cipher := reclaudeTestAccount(t)
		store := &fakeReclaudeUsageStore{}
		upstream := NewReclaudeUpstream(
			&capturingUpstream{err: errors.New("dial tcp: timeout")}, cipher, nil)
		upstream.SetQuotaRecorder(NewReclaudeQuotaGate(store))

		_, err := upstream.Do(innerRequest(t, `{"model":"x"}`), account, "")

		require.Error(t, err)
		require.Len(t, store.deltas, 1)
		require.Equal(t, int64(1), store.deltas[0].FailedUpstreamCalls)
		require.Zero(t, store.deltas[0].Tokens)
		// UpstreamCalls 是**总次数**，失败次数是它的子集（见 RecordUpstreamCall）。
		require.Equal(t, int64(1), store.deltas[0].UpstreamCalls)
	})

	t.Run("网关返回错误状态也记一次失败调用", func(t *testing.T) {
		account, cipher := reclaudeTestAccount(t)
		store := &fakeReclaudeUsageStore{}
		upstream := NewReclaudeUpstream(
			&capturingUpstream{response: &http.Response{
				StatusCode: http.StatusBadGateway,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader("bad gateway")),
			}}, cipher, nil)
		upstream.SetQuotaRecorder(NewReclaudeQuotaGate(store))

		_, err := upstream.Do(innerRequest(t, `{"model":"x"}`), account, "")

		require.Error(t, err)
		require.Len(t, store.deltas, 1)
		require.Equal(t, int64(1), store.deltas[0].FailedUpstreamCalls)
	})

	t.Run("建包阶段失败不记账", func(t *testing.T) {
		// 🔴 请求根本没发出去，对方的配额没被扣。记了就是系统性高估，
		// 高估的方向是白白闲置买来的配额包。
		account, cipher := reclaudeTestAccount(t)
		account.Credentials[CredKeyReclaudeGateway] = "https://evil.example.com"
		store := &fakeReclaudeUsageStore{}
		upstream := NewReclaudeUpstream(&capturingUpstream{}, cipher, nil)
		upstream.SetQuotaRecorder(NewReclaudeQuotaGate(store))

		_, err := upstream.Do(innerRequest(t, `{"model":"x"}`), account, "")

		require.ErrorIs(t, err, ErrReclaudeGatewayNotAllowed)
		require.Empty(t, store.deltas)
	})

	t.Run("没有记账器时照常转发", func(t *testing.T) {
		account, cipher := reclaudeTestAccount(t)
		upstream := NewReclaudeUpstream(
			&capturingUpstream{err: errors.New("dial tcp: timeout")}, cipher, nil)

		_, err := upstream.Do(innerRequest(t, `{"model":"x"}`), account, "")

		require.Error(t, err)
	})
}

func TestGatewayService_RecordsReclaudeTokens(t *testing.T) {
	ctx := context.Background()
	account, _ := reclaudeTestAccount(t)
	usage := ClaudeUsage{InputTokens: 1000, OutputTokens: 234}

	t.Run("成功请求按 token 记一次成功调用", func(t *testing.T) {
		store := &fakeReclaudeUsageStore{}
		gateway := &GatewayService{reclaudeQuota: NewReclaudeQuotaGate(store)}

		gateway.recordReclaudeQuota(ctx, account, &ForwardResult{Usage: usage})

		require.Len(t, store.deltas, 1)
		require.Equal(t, int64(1234), store.deltas[0].Tokens)
		require.Equal(t, int64(1), store.deltas[0].UpstreamCalls)
	})

	t.Run("非 reclaude 账号不写水位", func(t *testing.T) {
		store := &fakeReclaudeUsageStore{}
		gateway := &GatewayService{reclaudeQuota: NewReclaudeQuotaGate(store)}

		gateway.recordReclaudeQuota(ctx,
			&Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth},
			&ForwardResult{Usage: usage})

		require.Empty(t, store.deltas)
	})

	t.Run("缺件时不 panic", func(t *testing.T) {
		var gateway *GatewayService
		gateway.recordReclaudeQuota(ctx, account, &ForwardResult{Usage: usage})
		(&GatewayService{}).recordReclaudeQuota(ctx, account, nil)
	})
}
