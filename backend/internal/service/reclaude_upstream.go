package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/reclaude"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
)

// reclaudeProxyPath 是推理流量的转发端点。
const reclaudeProxyPath = "/proxy"

// ReclaudeGatewayError 表示 **reclaude 网关自身**拒绝了请求
// （SK 失效 / 签名不过 / 设备被撤销 / 他们侧限流 / 节点 5xx），
// 而不是「隧道成功但 Anthropic 报错」。
//
// 两者必须分开：后者外层是 200、body 是信封；前者外层是 4xx/5xx、
// body 是他们自己的错误结构。把前者当信封解会读到 `{"er` ≈ 2.06 GB 的长度前缀，
// 报成「超 16MiB」，把真实原因盖掉。
type ReclaudeGatewayError struct {
	StatusCode int
	AccountID  int64
}

func (e *ReclaudeGatewayError) Error() string {
	return fmt.Sprintf("reclaude gateway rejected the request with status %d (account %d)",
		e.StatusCode, e.AccountID)
}

// ReclaudeEventHandler 接收带外事件。
//
// events 绝不能进返回给下游的 body：原版会把它伪装成 Anthropic 错误吐给用户，
// 内容里带着**别人的**掩码邮箱。
type ReclaudeEventHandler interface {
	HandleReclaudeEvents(account *Account, events []reclaude.ReclaudeEvent)
	// HandleReclaudeGatewayError 承接外层非 200 —— 那是 reclaude 网关**自己**
	// 拒绝了请求，与「隧道通了、Anthropic 报错」是两件事（后者外层恒为 200）。
	HandleReclaudeGatewayError(account *Account, statusCode int)
}

// ReclaudeEventHandlerFunc 让普通函数满足 ReclaudeEventHandler。
type ReclaudeEventHandlerFunc func(account *Account, events []reclaude.ReclaudeEvent)

// HandleReclaudeEvents 实现 ReclaudeEventHandler。
func (f ReclaudeEventHandlerFunc) HandleReclaudeEvents(account *Account, events []reclaude.ReclaudeEvent) {
	f(account, events)
}

// HandleReclaudeGatewayError 实现 ReclaudeEventHandler；函数形式不处理网关错误。
func (f ReclaudeEventHandlerFunc) HandleReclaudeGatewayError(*Account, int) {}

// ReclaudeUpstream 把一个标准的 Anthropic 请求装箱、签名，发给 reclaude 网关，
// 再把信封响应还原成一个合成的 *http.Response。
//
// 还原成 *http.Response 是整个设计的支点：DoWithTLS 之后的错误处理、SSE 解析、
// usage 统计与计费因此全部零改动。
type ReclaudeUpstream struct {
	httpUpstream HTTPUpstream
	cipher       *ReclaudeCredentialCipher
	events       ReclaudeEventHandler
	quota        ReclaudeUpstreamCallRecorder
}

// ReclaudeUpstreamCallRecorder 记录一次上游调用的水位。
//
// 只取 ReclaudeQuotaGate 的一个方法：转发器不需要知道闸门怎么判，
// 也不该有能力去判 —— 它只负责如实上报「这一次确实发出去了」。
type ReclaudeUpstreamCallRecorder interface {
	RecordUpstreamCall(ctx context.Context, account *Account, tokens int64, succeeded bool) error
}

// NewReclaudeUpstream 构造转发器。events 可为 nil（此时事件只被丢弃，不影响请求）。
func NewReclaudeUpstream(
	httpUpstream HTTPUpstream, cipher *ReclaudeCredentialCipher, events ReclaudeEventHandler,
) *ReclaudeUpstream {
	return &ReclaudeUpstream{httpUpstream: httpUpstream, cipher: cipher, events: events}
}

// SetQuotaRecorder 注入日水位记账器。
//
// 用 setter 而不是加构造参数：闸门与转发器是相互独立的两件事，
// 而 NewReclaudeUpstream 的签名已经被协议层测试大量引用。
func (u *ReclaudeUpstream) SetQuotaRecorder(recorder ReclaudeUpstreamCallRecorder) {
	if u == nil {
		return
	}
	u.quota = recorder
}

// recordFailedCall 记一次**已经发出去**的失败调用。
//
// 🔴 只在请求真的出网之后才调用。建包/校验阶段失败时请求根本没发出去，
// 对方的配额没被扣，记了就是系统性高估 —— 高估的方向是白白闲置买来的配额包。
//
// ctx 用 Background：这是一次计数器写入，不带请求态；而流式请求的 ctx
// 在这一刻很可能已经随客户端断开被取消，用它会让水位静默漏记。
func (u *ReclaudeUpstream) recordFailedCall(account *Account) {
	if u == nil || u.quota == nil {
		return
	}
	if err := u.quota.RecordUpstreamCall(context.Background(), account, 0, false); err != nil {
		logger.LegacyPrintf("service.reclaude",
			"failed to record reclaude upstream failure for account %d: %v", account.ID, err)
	}
}

// Do 执行一次信封转发。
//
// ⚠️ 签名里**刻意不带 context 参数**：ctx 必须取自 inner.Context()。
// 流式请求的 context 被 detachStreamUpstreamContext 换成了 WithoutCancel 并挂在
// inner request 上，另取一个 ctx 等于把那层 detach 白做 —— 下游一断就拿不到 usage。
func (u *ReclaudeUpstream) Do(inner *http.Request, account *Account, proxyURL string) (*http.Response, error) {
	if u == nil || u.httpUpstream == nil {
		return nil, errors.New("reclaude upstream: http upstream not configured")
	}
	if inner == nil {
		return nil, errors.New("reclaude upstream: request is required")
	}
	if account == nil || !account.IsReclaude() {
		return nil, errors.New("reclaude upstream: reclaude account is required")
	}

	signer, sk, err := u.deviceSignerFor(account)
	if err != nil {
		return nil, err
	}

	// 出站前再校验一次网关地址。建号时已经校验过（V-9），这里是第二道：
	// credentials 可能被后续的编辑或数据导入改动，而这个字段决定我们把
	// **全量明文 prompt** 发到哪台机器上。
	gatewayURL, err := urlvalidator.ValidateHTTPSURL(
		account.GetCredential(CredKeyReclaudeGateway),
		urlvalidator.ValidationOptions{AllowedHosts: ReclaudeAllowedGatewayHosts, RequireAllowlist: true},
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrReclaudeGatewayNotAllowed, err.Error())
	}

	envelope, traceID, err := buildReclaudeEnvelope(inner)
	if err != nil {
		return nil, err
	}
	if capture := reclaudeTraceCaptureFromContext(inner.Context()); capture != nil {
		capture.set(traceID)
	}

	outer, err := u.buildProxyRequest(inner, account, gatewayURL, sk, envelope, signer)
	if err != nil {
		return nil, err
	}

	// tlsProfile 显式传 nil：外层对端是 reclaude 网关，不是 Anthropic，
	// 套 Anthropic 的 TLS 指纹没有意义。并发参数传 account.Concurrency 而不是 1 ——
	// 它会被直接设成 MaxConnsPerHost，传 1 等于把推导出来的并发值一行作废。
	resp, err := u.httpUpstream.DoWithTLS(outer, proxyURL, account.ID, account.Concurrency, nil)
	if err != nil {
		// 请求已经出网：对端可能已经扣了配额，必须记。
		u.recordFailedCall(account)
		return nil, fmt.Errorf("reclaude upstream transport: %w", err)
	}

	synthesized, err := u.synthesizeResponse(resp, account)

	// 外层非 200 = 网关自己拒绝了我们。映射成账号动作（401/403 停号、429 冷却、
	// 5xx 短冷却），否则 SK 被撤销后这个账号会被一直调度、每次都 401。
	var gatewayErr *ReclaudeGatewayError
	if errors.As(err, &gatewayErr) && u.events != nil {
		u.events.HandleReclaudeGatewayError(account, gatewayErr.StatusCode)
	}

	// 成功的调用在计费口径上才知道 token 数，由 GatewayService.recordReclaudeQuota 记；
	// 失败的调用拿不到 usage，只能在这里按次记，否则水位系统性低估 ⇒ 超卖。
	if err != nil || synthesized == nil || synthesized.StatusCode >= http.StatusBadRequest {
		u.recordFailedCall(account)
	}
	return synthesized, err
}

// deviceSignerFor 解密凭据并构造签名器。
func (u *ReclaudeUpstream) deviceSignerFor(account *Account) (*reclaude.DeviceSigner, string, error) {
	sk, err := u.cipher.DecryptSecret(account, CredKeyReclaudeSK)
	if err != nil {
		return nil, "", err
	}
	seedEncoded, err := u.cipher.DecryptSecret(account, CredKeyReclaudeSeed)
	if err != nil {
		return nil, "", err
	}
	seed, err := decodeDeviceSeed(seedEncoded)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %s", ErrReclaudeSeedInvalid, err.Error())
	}

	signer, err := reclaude.NewDeviceSigner(seed, account.GetCredentialAsInt64(CredKeyReclaudeDeviceID))
	if err != nil {
		return nil, "", fmt.Errorf("reclaude upstream: %w", err)
	}
	return signer, sk, nil
}

// buildReclaudeEnvelope 把内层请求装箱。
//
// 请求体必须完整 buffer：签名覆盖的是整个信封的 sha256，没法 chunked 流式上传。
// 对 /v1/messages 无影响（请求体小）。响应侧仍然是流式的。
func buildReclaudeEnvelope(inner *http.Request) ([]byte, string, error) {
	var body []byte
	if inner.Body != nil {
		read, err := io.ReadAll(inner.Body)
		_ = inner.Body.Close()
		if err != nil {
			return nil, "", fmt.Errorf("reclaude upstream: read inner request body: %w", err)
		}
		body = read
	}

	headers := make(map[string]string, len(inner.Header))
	for name, values := range inner.Header {
		if len(values) == 0 {
			continue
		}
		// 对端要求全小写。内层 header 的**取值**一个字不改 —— 那是 CC 伪装的产物。
		headers[strings.ToLower(name)] = values[0]
	}

	traceID := uuid.NewString()
	envelope, err := reclaude.EncodeEnvelope(reclaude.ClientRequestMetadata{
		URL:     inner.URL.String(),
		Method:  inner.Method,
		Headers: headers,
		TraceID: traceID,
	}, body)
	if err != nil {
		return nil, "", fmt.Errorf("reclaude upstream: %w", err)
	}
	return envelope, traceID, nil
}

// buildProxyRequest 组装打给 /proxy 的外层请求。
func (u *ReclaudeUpstream) buildProxyRequest(
	inner *http.Request, account *Account, gatewayURL, sk string,
	envelope []byte, signer *reclaude.DeviceSigner,
) (*http.Request, error) {
	// ctx 取自 inner —— 见 Do 的签名注释。
	ctx := WithHTTPUpstreamProfile(inner.Context(), HTTPUpstreamProfileLongStream)
	// 默认配置下 SSRF 校验不生效（allowlist 关、allow_private_hosts 开），
	// 这个标记强制开启校验，不依赖全局配置。
	ctx = WithHTTPUpstreamPublicHostsOnly(ctx)

	outer, err := http.NewRequestWithContext(
		ctx, http.MethodPost, gatewayURL+reclaudeProxyPath, newBytesBody(envelope))
	if err != nil {
		return nil, fmt.Errorf("reclaude upstream: build proxy request: %w", err)
	}
	outer.ContentLength = int64(len(envelope))

	outer.Header.Set("Authorization", "Bearer "+sk)
	outer.Header.Set("Content-Type", "application/octet-stream")
	outer.Header.Set(reclaude.HeaderClientVersion, account.GetCredential(CredKeyReclaudeClientVersion))
	outer.Header.Set(reclaude.HeaderClientPlatform, account.GetCredential(CredKeyReclaudeClientPlatform))

	if err := signer.AddSignatureHeaders(outer.Header, envelope); err != nil {
		return nil, fmt.Errorf("reclaude upstream: %w", err)
	}
	return outer, nil
}

// synthesizeResponse 执行响应流水线的 7.5 → 12 步。
func (u *ReclaudeUpstream) synthesizeResponse(outer *http.Response, account *Account) (*http.Response, error) {
	// 7.5：外层非 200 绝不尝试解信封。
	if outer.StatusCode != http.StatusOK {
		// 他们的错误体绝不透传下游，也不进日志正文 —— 它可能含别人的信息。
		_, _ = io.Copy(io.Discard, io.LimitReader(outer.Body, 4<<10))
		_ = outer.Body.Close()
		return nil, &ReclaudeGatewayError{StatusCode: outer.StatusCode, AccountID: account.ID}
	}

	// 8–9：ReadFull 读长度前缀与元数据；10：剩余流**不读**，直接当 body。
	meta, bodyReader, err := reclaude.DecodeResponseEnvelope(outer.Body)
	if err != nil {
		// 任何 error 路径都必须先关内层 body：调用方拿到的是 (nil, err)，
		// 它的 `if resp != nil` 清理救不了，而 inFlight > 0 的 client entry
		// 永不被淘汰，最终撞上游连接数上限。
		_ = outer.Body.Close()
		return nil, fmt.Errorf("reclaude upstream: %w", err)
	}

	// 11：canonical 化 header，否则 X-Should-Retry / request-id / ratelimit 全读不到。
	synthesized := &http.Response{
		Status:        fmt.Sprintf("%d %s", meta.Status, meta.StatusText),
		StatusCode:    meta.Status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        reclaude.CanonicalizeHeaders(meta.Headers),
		Body:          &reclaudeEnvelopeBody{reader: bodyReader, closer: outer.Body},
		ContentLength: -1,
	}

	// 11.5：内层解压。外层 Content-Encoding 是信封的，不带内层标记，
	// 没有任何一层会解它 ⇒ 不解就是 SSE 扫到二进制、usage 解析不到、下游乱码。
	if err := reclaude.DecompressInnerBody(synthesized); err != nil {
		_ = synthesized.Body.Close()
		return nil, fmt.Errorf("reclaude upstream: %w", err)
	}

	// 12：events 旁路。
	if len(meta.Events) > 0 && u.events != nil {
		u.events.HandleReclaudeEvents(account, meta.Events)
	}

	return synthesized, nil
}

// reclaudeEnvelopeBody 把信封剩余流包成 ReadCloser，Close 时关闭原始 body。
type reclaudeEnvelopeBody struct {
	reader io.Reader
	closer io.Closer
}

func (b *reclaudeEnvelopeBody) Read(p []byte) (int, error) { return b.reader.Read(p) }
func (b *reclaudeEnvelopeBody) Close() error               { return b.closer.Close() }
