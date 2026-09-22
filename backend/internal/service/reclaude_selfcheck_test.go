package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/reclaude"
)

// selfCheckStore 记录激活动作。
type selfCheckStore struct {
	account      *Account
	getErr       error
	clearedError bool
	schedulable  *bool
}

func (s *selfCheckStore) GetAccount(_ context.Context, _ int64) (*Account, error) {
	return s.account, s.getErr
}

func (s *selfCheckStore) ClearAccountError(_ context.Context, _ int64) error {
	s.clearedError = true
	return nil
}

func (s *selfCheckStore) SetAccountSchedulable(_ context.Context, _ int64, schedulable bool) error {
	s.schedulable = &schedulable
	return nil
}

// stepProbe 按端点返回预设结果。
type stepProbe struct {
	readyStatus   int
	accountStatus int
	accountBody   string
	err           error
	calls         []string
}

func (p *stepProbe) ProbeReclaudeEndpoint(
	_ context.Context, _ *Account, endpoint string,
) (*http.Response, error) {
	p.calls = append(p.calls, endpoint)
	if p.err != nil {
		return nil, p.err
	}
	status, body := p.readyStatus, ""
	if endpoint == ReclaudeClientAccountPath {
		status, body = p.accountStatus, p.accountBody
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func passingProbe() *stepProbe {
	return &stepProbe{
		readyStatus:   200,
		accountStatus: 200,
		accountBody:   `{"user_email":"o***@example.com"}`,
	}
}

func selfCheckFixture(t *testing.T, probe ReclaudeEndpointProbe, upstream *ReclaudeUpstream) (*ReclaudeSelfChecker, *selfCheckStore) {
	t.Helper()
	account, _ := reclaudeTestAccount(t)
	account.Proxy = &Proxy{}
	store := &selfCheckStore{account: account}
	return NewReclaudeSelfChecker(store, probe, upstream), store
}

func passingUpstream(t *testing.T) *ReclaudeUpstream {
	t.Helper()
	_, cipher := reclaudeTestAccount(t)
	return NewReclaudeUpstream(&capturingUpstream{response: &http.Response{
		StatusCode: 200, Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(string(envelopeResponse(t, 200, reclaude.GatewayResponseMetadata{}, "{}")))),
	}}, cipher, nil)
}

func TestReclaudeSelfChecker(t *testing.T) {
	ctx := context.Background()

	t.Run("三步全绿才置 active", func(t *testing.T) {
		checker, store := selfCheckFixture(t, passingProbe(), passingUpstream(t))

		result, err := checker.Run(ctx, 9)

		require.NoError(t, err)
		require.True(t, result.Passed)
		require.Len(t, result.Steps, 3)
		require.Equal(t, "o***@example.com", result.BoundEmail)
		require.True(t, result.Activated)
		require.True(t, store.clearedError)
		require.NotNil(t, store.schedulable)
		require.True(t, *store.schedulable)
	})

	t.Run("任一步失败就不置 active", func(t *testing.T) {
		probe := passingProbe()
		probe.accountStatus = http.StatusUnauthorized
		checker, store := selfCheckFixture(t, probe, passingUpstream(t))

		result, err := checker.Run(ctx, 9)

		require.NoError(t, err)
		require.False(t, result.Passed)
		require.False(t, result.Activated)
		require.False(t, store.clearedError)
	})

	t.Run("失败时不改动账号状态", func(t *testing.T) {
		// 🔴 只在成功时动状态。失败就下线的话，一次网络抖动会把正在跑的账号打下线；
		// 真正的凭据失效由网关错误映射（401/403 → SetError）负责停号。
		probe := passingProbe()
		probe.err = errors.New("dial tcp: timeout")
		checker, store := selfCheckFixture(t, probe, passingUpstream(t))

		result, err := checker.Run(ctx, 9)

		require.NoError(t, err)
		require.False(t, result.Passed)
		require.Nil(t, store.schedulable)
	})

	t.Run("非 reclaude 账号拒绝自检", func(t *testing.T) {
		store := &selfCheckStore{account: &Account{ID: 1, Type: AccountTypeOAuth}}
		checker := NewReclaudeSelfChecker(store, passingProbe(), passingUpstream(t))

		_, err := checker.Run(ctx, 1)

		require.Error(t, err)
	})

	t.Run("前一步失败时不再打后面的端点", func(t *testing.T) {
		// 网关都不可达就没必要再拿凭据去试，更没必要消耗一次 /proxy 请求配额。
		probe := passingProbe()
		probe.readyStatus = http.StatusBadGateway
		checker, _ := selfCheckFixture(t, probe, passingUpstream(t))

		result, err := checker.Run(ctx, 9)

		require.NoError(t, err)
		require.False(t, result.Passed)
		require.Equal(t, []string{ReclaudeHealthReadyPath}, probe.calls)
		require.Len(t, result.Steps, 3)
		require.False(t, result.Steps[1].OK)
		require.False(t, result.Steps[2].OK)
	})
}
