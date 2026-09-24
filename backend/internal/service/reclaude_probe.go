package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
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
	// interceptETags 记住每台设备的拦截清单版本，用于条件请求（见 ProbeReclaudeEndpoint）。
	interceptETags *ReclaudeInterceptETagStore
}

// RememberInterceptETag 记下拦截清单的版本，供下次条件请求使用。
func (p *ReclaudeGatewayProbe) RememberInterceptETag(accountID int64, etag string) {
	if p == nil || p.interceptETags == nil {
		return
	}
	p.interceptETags.Remember(accountID, etag)
}

// NewReclaudeGatewayProbe 构造探测器。
func NewReclaudeGatewayProbe(
	httpUpstream HTTPUpstream, cipher *ReclaudeCredentialCipher,
) *ReclaudeGatewayProbe {
	return &ReclaudeGatewayProbe{
		httpUpstream:   httpUpstream,
		cipher:         cipher,
		interceptETags: NewReclaudeInterceptETagStore(),
	}
}

// ProbeReclaudeEndpoint 实现 ReclaudeEndpointProbe。
//
// 出站必须与推理流量**走同一条代理**：心跳从另一个出口发出去，
// 在对端眼里就是「这台设备同时出现在两个地方」。
func (p *ReclaudeGatewayProbe) ProbeReclaudeEndpoint(
	ctx context.Context, account *Account, endpoint string,
) (*http.Response, error) {
	return p.doControlPlane(ctx, account, http.MethodGet, endpoint, nil)
}

// PostReclaudeTelemetry 上报一批 rollup。
//
// 与心跳共用 doControlPlane，因此**必然**走同一条住宅代理、同一套签名头、
// 同一个强制 HTTP/1.1 的 transport。遥测从别的出口发出去，在对端眼里就是
// 「这台设备同时出现在两个地方」—— 比不发遥测更糟。
func (p *ReclaudeGatewayProbe) PostReclaudeTelemetry(
	ctx context.Context, account *Account, payload []byte,
) (*http.Response, error) {
	return p.doControlPlane(ctx, account, http.MethodPost, ReclaudeTelemetryPath, payload)
}

// doControlPlane 组装并发出一次控制面请求。
//
// body 为 nil 即 GET 语义（签 sha256("")）。
func (p *ReclaudeGatewayProbe) doControlPlane(
	ctx context.Context, account *Account, method, endpoint string, body []byte,
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

	// 与推理流量一致：Reclaude profile（强制 HTTP/1.1）
	// + public-hosts-only（默认配置下 SSRF 校验不生效，这个标记强制开启）。
	//
	// 🔴 此处原注释写「否则外层退成 HTTP/1.1，而真客户端走 H2」—— **方向反了**。
	// 反编译（newDirectTransport 设 NextProtos=["http/1.1"]）与抓包（所有请求
	// 首行 HTTP/1.1、UA Go-http-client/1.1）双向证伪：真客户端强制 H1。
	reqCtx := WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileReclaude)
	reqCtx = WithHTTPUpstreamPublicHostsOnly(reqCtx)

	var payload io.Reader
	if body != nil {
		payload = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, gatewayURL+endpoint, payload)
	if err != nil {
		return nil, fmt.Errorf("reclaude probe: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		// 显式设置：signControlPlaneRequest 签的是 body，而 content-length
		// 与实际字节数对不上会让对端先于验签就拒掉。
		req.ContentLength = int64(len(body))
	}

	// 🔴 控制面**只带 Authorization + 五个签名头**。
	//
	// ✅ 真值抓包（reclaude-lab/sink/dump/*client_*.txt）：控制面请求里没有
	// X-Reclaude-Client-Version / -Client-Platform —— 那两个头**只出现在 /proxy**。
	// 在这里带上它们是一个「照着 /proxy 抄的」直接特征，真客户端的两条代码
	// 路径本来就不同（tunnel.Forward vs intercept.fetch）。
	//
	// Device-Id 不在这里写：signControlPlaneRequest 里的签名器会统一写入，
	// 两处都写会出现大小写重复键。
	req.Header.Set("Authorization", "Bearer "+sk)

	// 🔴 拦截清单走条件请求：真客户端持久化版本号（形如 "0:0"）并每次带
	// If-None-Match，第一次 200、此后一整天都是 304（✅ 真值抓包）。
	// 不带的话每 60 秒强制一次全量 200 —— 一条每分钟重复、跨设备一致的信号。
	//
	// 只给这个端点带：ETag 是清单端点专有的，给 /client/account 带上是另一种抄错。
	if endpoint == ReclaudeInterceptDomainsPath {
		if etag, ok := p.interceptETags.Get(account.ID); ok {
			req.Header.Set("If-None-Match", etag)
		}
	}

	// 🔴 代理取不到就**拒绝发送**，不能退回直连。
	//
	// 建号硬校验保证了 ProxyID 非空（V-1），取不到只可能是数据被改坏或 Proxy 未预加载。
	// 此时传空 proxyURL 会让心跳走机房 IP —— 设备页面的「最近 IP」立刻从住宅跳到
	// 机房，而那个字段是持续更新、直接可见的。宁可这台设备暂时没有心跳。
	proxyURL, err := reclaudeProxyURLFor(account)
	if err != nil {
		return nil, err
	}

	// 🔴 控制面端点也要签名。
	//
	// 这里原本的假设是「不带信封就不用签名」，2026-09-23 被实测证伪：
	// `GET /client/account` 无签名头会被网关拒为 400 device_signature_required，
	// 而 /health/ready 不需要 —— 于是建号自检表现为「第一步网关可达 ✓、
	// 第二步凭据无效 ✗」，把一个缺签名的问题伪装成凭据问题。
	// GET 没有 body，签的是 sha256("")，canonical 串格式与信封路径完全一致。
	if err := p.signControlPlaneRequest(req, account, body); err != nil {
		return nil, err
	}

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

// signControlPlaneRequest 给控制面请求补签名头（X-Reclaude-Ts / Nonce /
// Body-Sha256 / Signature，以及由签名器统一写入的 Device-Id）。
//
// body 为 nil 时签的是 sha256("")，与 GET 路径一致；POST 必须把**实际发出去
// 的字节**传进来，签一份、发另一份等于自己给自己制造签名失败。
func (p *ReclaudeGatewayProbe) signControlPlaneRequest(
	req *http.Request, account *Account, body []byte,
) error {
	seedEncoded, err := p.cipher.DecryptSecret(account, CredKeyReclaudeSeed)
	if err != nil {
		return err
	}
	seed, err := decodeDeviceSeed(seedEncoded)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrReclaudeSeedInvalid, err.Error())
	}
	signer, err := reclaude.NewDeviceSigner(seed, account.GetCredentialAsInt64(CredKeyReclaudeDeviceID))
	if err != nil {
		return fmt.Errorf("reclaude probe: %w", err)
	}
	return signer.AddControlPlaneSignatureHeaders(req.Header, body)
}
