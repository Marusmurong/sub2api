package service

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/reclaude"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
)

// ReclaudeGatewayProbe 打 reclaude 网关的**非推理**端点
// （心跳、健康检查、账号状态），不走 /proxy 信封。
//
// 与 ReclaudeUpstream 分开：那边转发的是装箱后的 Anthropic 请求，这边是
// 客户端自身的控制面请求。两者共用凭据与代理，但请求形态完全不同 ——
// 硬塞进一个类型会让「什么时候装箱」变成一个隐藏分支。
type ReclaudeGatewayProbe struct {
	httpUpstream HTTPUpstream
	cipher       *ReclaudeCredentialCipher
}

// NewReclaudeGatewayProbe 构造探测器。
func NewReclaudeGatewayProbe(httpUpstream HTTPUpstream, cipher *ReclaudeCredentialCipher) *ReclaudeGatewayProbe {
	return &ReclaudeGatewayProbe{httpUpstream: httpUpstream, cipher: cipher}
}

// ProbeReclaudeEndpoint 实现 ReclaudeEndpointProbe。
//
// 出站必须与推理流量**走同一条代理**：心跳从另一个出口发出去，
// 在对端眼里就是「这台设备同时出现在两个地方」。
func (p *ReclaudeGatewayProbe) ProbeReclaudeEndpoint(
	ctx context.Context, account *Account, endpoint string,
) (*http.Response, error) {
	if p == nil || p.httpUpstream == nil {
		return nil, fmt.Errorf("reclaude probe: http upstream not configured")
	}
	if account == nil || !account.IsReclaude() {
		return nil, fmt.Errorf("reclaude probe: reclaude account is required")
	}

	sk, err := p.cipher.DecryptSecret(account, CredKeyReclaudeSK)
	if err != nil {
		return nil, err
	}

	gatewayURL, err := urlvalidator.ValidateHTTPSURL(
		account.GetCredential(CredKeyReclaudeGateway),
		urlvalidator.ValidationOptions{AllowedHosts: ReclaudeAllowedGatewayHosts, RequireAllowlist: true},
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrReclaudeGatewayNotAllowed, err.Error())
	}

	// 与推理流量一致：long_stream profile（否则外层退成 HTTP/1.1，而真客户端走 H2）
	// + public-hosts-only（默认配置下 SSRF 校验不生效，这个标记强制开启）。
	reqCtx := WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileLongStream)
	reqCtx = WithHTTPUpstreamPublicHostsOnly(reqCtx)

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, gatewayURL+endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("reclaude probe: build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+sk)
	req.Header.Set(reclaude.HeaderClientVersion, account.GetCredential(CredKeyReclaudeClientVersion))
	req.Header.Set(reclaude.HeaderClientPlatform, account.GetCredential(CredKeyReclaudeClientPlatform))
	req.Header.Set(reclaude.HeaderDeviceID, account.GetCredential(CredKeyReclaudeDeviceID))

	// 🔴 代理取不到就**拒绝发送**，不能退回直连。
	//
	// 建号硬校验保证了 ProxyID 非空（V-1），取不到只可能是数据被改坏或 Proxy 未预加载。
	// 此时传空 proxyURL 会让心跳走机房 IP —— 设备页面的「最近 IP」立刻从住宅跳到
	// 机房，而那个字段是持续更新、直接可见的。宁可这台设备暂时没有心跳。
	proxyURL, err := reclaudeProxyURLFor(account)
	if err != nil {
		return nil, err
	}

	// 这些端点不带信封，因此**不签名** —— 签名覆盖的是信封字节，这里没有信封。
	return p.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, nil)
}

// reclaudeProxyURLFor 取账号绑定的固定住宅代理；取不到即报错。
func reclaudeProxyURLFor(account *Account) (string, error) {
	if account == nil || account.Proxy == nil {
		return "", fmt.Errorf("reclaude probe: account %d has no proxy loaded; refusing to send from the datacenter IP",
			accountIDOf(account))
	}
	url := account.Proxy.URL()
	if url == "" {
		return "", fmt.Errorf("reclaude probe: account %d proxy url is empty; refusing to send from the datacenter IP",
			account.ID)
	}
	return url, nil
}

func accountIDOf(account *Account) int64 {
	if account == nil {
		return 0
	}
	return account.ID
}
