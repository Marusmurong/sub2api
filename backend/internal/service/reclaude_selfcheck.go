package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/reclaude"
)

// 自检三步的名字。前端按名字展示，失败时运维要一眼看出卡在哪一环。
const (
	ReclaudeCheckGatewayReachable = "gateway_reachable"
	ReclaudeCheckCredentialValid  = "credential_valid"
	ReclaudeCheckEnvelopeSignable = "envelope_signable"
)

// reclaudeSelfCheckBody 是第三步用的最小请求体。
//
// 打 count_tokens 而不是 /v1/messages：两者都实打实消耗对方**一次请求配额**，
// 但 count_tokens 不产生 token。自检是个会被反复点的按钮，选便宜的那个。
var reclaudeSelfCheckBody = []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"ping"}]}`)

const reclaudeSelfCheckURL = "https://api.anthropic.com/v1/messages/count_tokens"

// ReclaudeSelfCheckStep 是一步自检的结果。
type ReclaudeSelfCheckStep struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// ReclaudeSelfCheckResult 是一次自检的完整结果。
type ReclaudeSelfCheckResult struct {
	Steps      []ReclaudeSelfCheckStep `json:"steps"`
	Passed     bool                    `json:"passed"`
	BoundEmail string                  `json:"bound_email,omitempty"`
	Activated  bool                    `json:"activated"`
}

// ReclaudeSelfCheckStore 是自检需要的最小仓储面。
type ReclaudeSelfCheckStore interface {
	GetAccount(ctx context.Context, accountID int64) (*Account, error)
	ClearAccountError(ctx context.Context, accountID int64) error
	SetAccountSchedulable(ctx context.Context, accountID int64, schedulable bool) error
}

// ReclaudeSelfChecker 执行建号后的连通性自检。
//
// 三步按依赖顺序短路：网关都不可达就没必要再拿凭据去试，更没必要白白消耗
// 一次 /proxy 请求配额。
type ReclaudeSelfChecker struct {
	store    ReclaudeSelfCheckStore
	probe    ReclaudeEndpointProbe
	upstream *ReclaudeUpstream
}

// NewReclaudeSelfChecker 构造自检器。
func NewReclaudeSelfChecker(
	store ReclaudeSelfCheckStore, probe ReclaudeEndpointProbe, upstream *ReclaudeUpstream,
) *ReclaudeSelfChecker {
	return &ReclaudeSelfChecker{store: store, probe: probe, upstream: upstream}
}

// Run 跑一次自检；三步全绿才把账号置为可调度。
//
// 🔴 **只在成功时动状态**。失败就下线的话，一次网络抖动会把正在跑的账号打下线；
// 而真正的凭据失效有专门的路径负责停号（外层 401/403 → SetError，§6.7）。
// 自检是个可以随便点的按钮，它不该有破坏力。
func (c *ReclaudeSelfChecker) Run(ctx context.Context, accountID int64) (*ReclaudeSelfCheckResult, error) {
	if c == nil || c.store == nil {
		return nil, errors.New("reclaude self-check: not configured")
	}

	account, err := c.store.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil || !account.IsReclaude() {
		return nil, fmt.Errorf("reclaude self-check: account %d is not a reclaude account", accountID)
	}

	result := &ReclaudeSelfCheckResult{
		Steps: []ReclaudeSelfCheckStep{
			{Name: ReclaudeCheckGatewayReachable},
			{Name: ReclaudeCheckCredentialValid},
			{Name: ReclaudeCheckEnvelopeSignable},
		},
	}

	if detail := c.checkEndpoint(ctx, account, ReclaudeHealthReadyPath, nil); detail != "" {
		result.Steps[0].Detail = detail
		return result, nil
	}
	result.Steps[0].OK = true

	var boundEmail string
	if detail := c.checkEndpoint(ctx, account, ReclaudeClientAccountPath, &boundEmail); detail != "" {
		result.Steps[1].Detail = detail
		return result, nil
	}
	result.Steps[1].OK = true
	result.BoundEmail = boundEmail

	if detail := c.checkEnvelope(ctx, account); detail != "" {
		result.Steps[2].Detail = detail
		return result, nil
	}
	result.Steps[2].OK = true

	result.Passed = true
	result.Activated = c.activate(ctx, accountID)
	return result, nil
}

// checkEndpoint 打一个控制面端点；返回空串表示通过。
func (c *ReclaudeSelfChecker) checkEndpoint(
	ctx context.Context, account *Account, endpoint string, boundEmail *string,
) string {
	if c.probe == nil {
		return "probe not configured"
	}

	resp, err := c.probe.ProbeReclaudeEndpoint(ctx, account, endpoint)
	if err != nil {
		return err.Error()
	}
	if resp == nil {
		return "empty response"
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Sprintf("status %d", resp.StatusCode)
	}

	if boundEmail != nil && resp.Body != nil {
		var state reclaude.ClientAccountResponse
		// 解析失败不算这一步失败：状态码已经证明 SK 有效，邮箱只是附带信息。
		if json.NewDecoder(io.LimitReader(resp.Body, reclaudeAccountStateMaxBytes)).Decode(&state) == nil {
			*boundEmail = state.UserEmail
		}
	}
	return ""
}

// checkEnvelope 走一次真实的信封 + 签名链路；返回空串表示通过。
//
// 判据是「能解出信封」，**不看内层状态码**：内层 4xx 来自 Anthropic，说明隧道
// 和签名都已经通了；而网关自己拒绝会是 ReclaudeGatewayError。把内层错误算作
// 失败，会让一个模型名写错卡住整个建号流程。
func (c *ReclaudeSelfChecker) checkEnvelope(ctx context.Context, account *Account) string {
	if c.upstream == nil {
		return "upstream not configured"
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, reclaudeSelfCheckURL, bytes.NewReader(reclaudeSelfCheckBody))
	if err != nil {
		return err.Error()
	}
	req.Header.Set("Content-Type", "application/json")

	proxyURL, err := reclaudeProxyURLFor(account)
	if err != nil {
		return err.Error()
	}

	resp, err := c.upstream.Do(req, account, proxyURL)
	if err != nil {
		return err.Error()
	}
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	return ""
}

// activate 把账号置为可调度；任一步写库失败都算未激活。
func (c *ReclaudeSelfChecker) activate(ctx context.Context, accountID int64) bool {
	if err := c.store.ClearAccountError(ctx, accountID); err != nil {
		return false
	}
	return c.store.SetAccountSchedulable(ctx, accountID, true) == nil
}
