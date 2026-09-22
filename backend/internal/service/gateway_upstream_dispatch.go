package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// ErrReclaudeUpstreamNotWired 表示请求落到了 reclaude 账号，但 reclaude 转发能力
// 尚未接入（批次 3 才实现）。
//
// 这是一个**故意的失败**而不是降级：宁可这个请求报错，也绝不能退回裸
// DoWithTLS —— 那会带着空 Bearer 把原始 Anthropic 请求直接打到 api.anthropic.com，
// 既泄漏请求内容，又必然 401。
var ErrReclaudeUpstreamNotWired = errors.New("reclaude upstream not wired")

// dispatchUpstream 是 service 层**唯一**允许直接调用 httpUpstream.DoWithTLS 的地方。
//
// 收口的理由：DoWithTLS 在 service 层有近二十个调用点（主路径 + 四条重试分支 +
// count_tokens + 协议转换入站 + 模型发现 + 计费探测 + 账号连通性测试），逐处加
// if 判断必然漏。收口成一个函数后，新增上游类型只需要改这里一处，
// 而 upstream_dispatch_chokepoint_test.go 会保证没有人绕过它。
func dispatchUpstream(
	httpUpstream HTTPUpstream,
	reclaudeUpstream *ReclaudeUpstream,
	req *http.Request,
	proxyURL string,
	account *Account,
	profile *tlsfingerprint.Profile,
) (*http.Response, error) {
	if account == nil {
		return nil, errors.New("dispatch upstream: account is required")
	}
	if account.IsReclaude() {
		// 未接线时**故意失败**，绝不退回裸 DoWithTLS。
		if reclaudeUpstream == nil {
			return nil, fmt.Errorf("%w (account %d)", ErrReclaudeUpstreamNotWired, account.ID)
		}
		// profile 被刻意丢弃：外层对端是 reclaude 网关而不是 Anthropic，
		// 套 Anthropic 的 TLS 指纹没有意义（ResolveTLSProfile 对这类账号本来也返回 nil）。
		return reclaudeUpstream.Do(req, account, proxyURL)
	}
	if httpUpstream == nil {
		return nil, errors.New("dispatch upstream: http upstream not configured")
	}
	return httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, profile)
}

// SetReclaudeUpstream 注入 reclaude 信封转发器。
//
// 走可选注入而不是加构造参数：GatewayService 的构造函数参数列表是跟随上游合并
// 时的高冲突点，而这个依赖本身允许缺席（缺席时 reclaude 账号直接报错）。
func (s *GatewayService) SetReclaudeUpstream(upstream *ReclaudeUpstream) {
	if s == nil {
		return
	}
	s.reclaudeUpstream = upstream
}

// SetReclaudeEnabledFunc 注入 reclaude 通道的运行时开关。
//
// 传一个函数而不是 *SettingService：调度端每次判定都要读最新值（开关的意义
// 就在于立刻生效），而 GatewayService 不该为此多认识一个服务。
func (s *GatewayService) SetReclaudeEnabledFunc(enabled func(context.Context) bool) {
	if s == nil {
		return
	}
	s.reclaudeEnabled = enabled
}

// SetReclaudeQuotaGate 注入 reclaude 日 token 硬闸。
func (s *GatewayService) SetReclaudeQuotaGate(gate *ReclaudeQuotaGate) {
	if s == nil {
		return
	}
	s.reclaudeQuota = gate
}

// doUpstream 是 GatewayService 的转发收口。
func (s *GatewayService) doUpstream(
	req *http.Request, proxyURL string, account *Account, profile *tlsfingerprint.Profile,
) (*http.Response, error) {
	return dispatchUpstream(s.httpUpstream, s.reclaudeUpstream, req, proxyURL, account, profile)
}

// doUpstream 是 AccountTestService 的转发收口（账号连通性测试、模型发现）。
// reclaude 账号在这里同样被拒绝：它的连通性自检走 /client/account，不走本链路。
func (s *AccountTestService) doUpstream(
	req *http.Request, proxyURL string, account *Account, profile *tlsfingerprint.Profile,
) (*http.Response, error) {
	return dispatchUpstream(s.httpUpstream, nil, req, proxyURL, account, profile)
}

// doUpstream 是 UpstreamBillingProbeService 的转发收口。
// reclaude 账号在这里被拒绝：他们不回传成本明细，计费探测对其无意义。
func (s *UpstreamBillingProbeService) doUpstream(
	req *http.Request, proxyURL string, account *Account, profile *tlsfingerprint.Profile,
) (*http.Response, error) {
	if s.accountTestService == nil {
		return nil, errors.New("dispatch upstream: account test service not configured")
	}
	return dispatchUpstream(s.accountTestService.httpUpstream, nil, req, proxyURL, account, profile)
}
