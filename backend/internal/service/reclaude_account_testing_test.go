package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fakeReclaudeSelfCheckRunner struct {
	result *ReclaudeSelfCheckResult
	err    error
	called int64
}

func (f *fakeReclaudeSelfCheckRunner) Run(_ context.Context, accountID int64) (*ReclaudeSelfCheckResult, error) {
	f.called = accountID
	return f.result, f.err
}

// reclaude 账号点「测试」时，之前返回的是 "Unsupported account type: reclaude" ——
// 号建好了却没有任何办法验证它能不能用。
//
// 不走通用 Claude 测试路径：那条路径直接打 api.anthropic.com/v1/messages，
// 而 reclaude 的请求必须封成信封、带 ed25519 设备签名、打对方的 route 节点。
// 自检器已经把这三步做好了，这里只负责把它的结果渲染成 SSE。
func TestTestAccountConnection_Reclaude(t *testing.T) {
	gin.SetMode(gin.TestMode)

	runTest := func(runner ReclaudeSelfCheckRunner) string {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest("GET", "/test", nil)

		svc := &AccountTestService{reclaudeSelfChecker: runner}
		account := &Account{ID: 7, Type: AccountTypeReclaude, Platform: PlatformAnthropic}

		_ = svc.testReclaudeAccountConnection(c, account)
		return recorder.Body.String()
	}

	t.Run("三步全绿时报成功，并逐步汇报", func(t *testing.T) {
		body := runTest(&fakeReclaudeSelfCheckRunner{result: &ReclaudeSelfCheckResult{
			Steps: []ReclaudeSelfCheckStep{
				{Name: ReclaudeCheckGatewayReachable, OK: true},
				{Name: ReclaudeCheckCredentialValid, OK: true},
				{Name: ReclaudeCheckEnvelopeSignable, OK: true},
			},
			Passed:     true,
			BoundEmail: "owner@example.com",
		}})

		require.Contains(t, body, ReclaudeCheckGatewayReachable)
		require.Contains(t, body, ReclaudeCheckEnvelopeSignable)
		require.Contains(t, body, "owner@example.com")
		require.Contains(t, body, `"success":true`)
	})

	t.Run("某步失败时要说清卡在哪一环", func(t *testing.T) {
		// 只说「测试失败」等于没说：三步的排查方向完全不同 ——
		// 网关不可达查代理，凭据无效查设备是否被删，信封失败查签名。
		body := runTest(&fakeReclaudeSelfCheckRunner{result: &ReclaudeSelfCheckResult{
			Steps: []ReclaudeSelfCheckStep{
				{Name: ReclaudeCheckGatewayReachable, OK: true},
				{Name: ReclaudeCheckCredentialValid, Detail: "device revoked"},
				{Name: ReclaudeCheckEnvelopeSignable},
			},
		}})

		require.Contains(t, body, ReclaudeCheckCredentialValid)
		require.Contains(t, body, "device revoked")
		require.NotContains(t, body, `"success":true`)
	})

	t.Run("自检器未注入时如实报错，不冒充成功", func(t *testing.T) {
		body := runTest(nil)

		require.NotContains(t, body, `"success":true`)
		require.Contains(t, strings.ToLower(body), "self-check")
	})

	t.Run("路由：reclaude 账号不再落到通用 Claude 路径", func(t *testing.T) {
		// 通用路径会打 api.anthropic.com 明文，对 reclaude 必然 401，
		// 且白白消耗一次对方配额。
		runner := &fakeReclaudeSelfCheckRunner{result: &ReclaudeSelfCheckResult{Passed: true}}
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest("GET", "/test", nil)

		svc := &AccountTestService{
			accountRepo:         &fakeReclaudeTestAccountRepo{account: &Account{ID: 7, Type: AccountTypeReclaude, Platform: PlatformAnthropic}},
			reclaudeSelfChecker: runner,
		}

		_ = svc.TestAccountConnection(c, 7, "", "", "")

		require.Equal(t, int64(7), runner.called)
		require.NotContains(t, recorder.Body.String(), "Unsupported account type")
	})

	t.Run("SSE 事件是合法 JSON", func(t *testing.T) {
		body := runTest(&fakeReclaudeSelfCheckRunner{result: &ReclaudeSelfCheckResult{Passed: true}})

		for _, line := range strings.Split(body, "\n") {
			payload, found := strings.CutPrefix(line, "data: ")
			if !found {
				continue
			}
			var decoded map[string]any
			require.NoErrorf(t, json.Unmarshal([]byte(payload), &decoded), "坏事件: %s", payload)
		}
	})
}

// 只需要 GetByID：嵌入接口让其余方法保持未实现（被调到就 panic，
// 而那正说明路由走岔了）。
type fakeReclaudeTestAccountRepo struct {
	AccountRepository
	account *Account
}

func (f *fakeReclaudeTestAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	return f.account, nil
}
