package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	telemetry    *ReclaudeTelemetryCollector
	lifecycle    *ReclaudeLifecycleDriver
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

// SetTelemetryCollector 注入遥测采集器。
//
// 与 SetQuotaRecorder 同样用 setter：遥测是转发器的旁路，缺席时转发照常。
func (u *ReclaudeUpstream) SetTelemetryCollector(collector *ReclaudeTelemetryCollector) {
	if u == nil {
		return
	}
	u.telemetry = collector
}

// SetLifecycleDriver 注入生命周期流量驱动器（D3）。
func (u *ReclaudeUpstream) SetLifecycleDriver(driver *ReclaudeLifecycleDriver) {
	if u == nil {
		return
	}
	u.lifecycle = driver
}

// recordTelemetry 记一次**已经出网**的调用。
//
// 与 recordFailedCall 的口径一致：建包阶段失败的请求没出网，不该出现在遥测里 ——
// 遥测的用途恰恰是与对端的网关记录对账，多记等于自己制造对不上。
func (u *ReclaudeUpstream) recordTelemetry(
	account *Account, sample ReclaudeTelemetrySample, at time.Time,
) {
	if u == nil || u.telemetry == nil || account == nil {
		return
	}
	u.telemetry.RecordAt(account.ID, sample, at)
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

	// 🔴 内层 Authorization 必须是本设备的 SK。
	//
	// GetAccessToken 对 reclaude 账号返回空 token（Anthropic 侧凭据由对方用自己的号
	// 填），于是 buildUpstreamRequest 根本不写这个头。但 2026-09-23 抓到的真实客户端
	// 信封里，内层 headers 的 authorization 恰恰是 `Bearer sk-rec-…`（它自己的 SK）——
	// 对方拿它识别客户端，再在转发给 Anthropic 时换成自己的凭据。缺了这一项，网关回
	// 400「reclaude 客户端状态异常，请重启 reclaude 后重试」，看起来像设备坏了。
	//
	// 只在这里补，不动 GetAccessToken：那个函数的返回值被十余处 CC 伪装门控共用，
	// 改它会外溢到普通 Claude 账号的链路。
	//
	// 🔴 必须用 setHeaderRaw 而不是 Header.Set。伪装路径已经用小写键写过一个
	// `authorization: "Bearer "`（reclaude 的 token 是空串，只剩前缀和一个空格），
	// Header.Set 会另建一个规范大小写的 "Authorization" 键 —— 两个键并存，装箱时
	// 统一转小写互相覆盖，谁赢取决于 map 遍历顺序，于是一半的请求带着空凭据出门，
	// 网关回 400 bad_envelope。setHeaderRaw 会把两种大小写都删掉再写小写键。
	setHeaderRaw(inner.Header, "authorization", "Bearer "+sk)

	// D3：补上真客户端的生命周期流量（启动引导 / MCP 复查）。
	//
	// 🔴 放在这里而不是函数末尾：它只看请求本身，不依赖响应，
	// 而且必须在 daemon 分支**之前** —— daemon 模式下本机真客户端
	// 自己就会发这些，我们再补一份就成了双份。
	//
	// 🔴 合成请求自己不能再触发合成（IsReclaudeSynthetic），否则指数爆炸。
	u.lifecycle.OnInference(inner.Context(), account, proxyURL, reclaudeRequestModel(inner))

	// daemon 模式：本机真客户端负责封信封与签名，我们只做 CC 伪装。
	//
	// 🔴 必须放在 authorization 注入**之后**：daemon 用内层 Authorization 识别
	// 是哪台设备在调用（真客户端的 ~/.claude/.credentials.json 里 accessToken
	// 存的就是 SK 本身）。放在前面会发出一个空凭据的请求，daemon 封装时判定
	// 状态异常，回 400 bad_envelope —— 看起来像信封问题，实际是凭据缺失。
	// transparent 口优先：正向代理口实测会被回 bad_envelope，
	// 而 Claude Code 本体打的就是 transparent 口。
	if endpoint, ok := ReclaudeDaemonEndpoint(account); ok {
		if err := retargetToReclaudeDaemon(inner, endpoint); err != nil {
			return nil, err
		}
		return u.doViaDaemon(inner, account, "")
	}
	if daemonProxy, ok := ReclaudeDaemonProxy(account); ok {
		normalizeInnerRequestURLForDaemon(inner)
		return u.doViaDaemon(inner, account, daemonProxy)
	}

	envelope, traceID, err := buildReclaudeEnvelope(inner, gatewayURL, account)
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
	// 排障用：落盘外层请求的完整头部取值（仅在调试环境变量配置时）。
	dumpOutboundRequest(outer, traceID)
	timer := newReclaudeCallTimer(time.Now())
	resp, err := u.httpUpstream.DoWithTLS(outer, proxyURL, account.ID, account.Concurrency, nil)
	if err != nil {
		// 请求已经出网：对端可能已经扣了配额，必须记。
		u.recordFailedCall(account)
		// 没拿到响应头 ⇒ 没有 TTFT 可言，传 0 让它不计入 ttft_n
		// （真值里 ttft_n 可以小于 n，编一个数会破坏这条关系）。
		u.recordTelemetry(account, ReclaudeTelemetrySample{
			Edge:    reclaudeEdgeLabel(gatewayURL),
			Outcome: ReclaudeOutcomeFailForward,
		}, time.Now())
		return nil, fmt.Errorf("reclaude upstream transport: %w", err)
	}
	timer.markTTFT(time.Now())

	synthesized, err := u.synthesizeResponse(resp, account)

	// 外层非 200 = 网关自己拒绝了我们。映射成账号动作（401/403 停号、429 冷却、
	// 5xx 短冷却），否则 SK 被撤销后这个账号会被一直调度、每次都 401。
	// 🔴 任何失败都把**我们实际发出去的信封形状**记下来。
	//
	// 对方的错误文案是给终端用户看的（"请重启 reclaude"），对我们零信息量；
	// 2026-09-23 排查 bad_envelope 时，缺这条日志只能靠一遍遍改代码重部署去猜。
	// 条件必须覆盖两条路径：网关自己的错误结构（ReclaudeGatewayError）与被合成成
	// 普通响应的非 2xx —— 只挂在前者上，那次 400 一条日志都没出。
	// 只记形状不记内容：URL / method / 头名清单 / 各段长度 / 信封头部字节。
	status := 0
	if synthesized != nil {
		status = synthesized.StatusCode
	}
	var gatewayErr *ReclaudeGatewayError
	isGatewayErr := errors.As(err, &gatewayErr)
	if isGatewayErr {
		status = gatewayErr.StatusCode
	}
	if err != nil || status >= http.StatusBadRequest {
		logEnvelopeShapeOnReject(inner, outer, envelope, traceID, status)
		// 排障用：仅在 SUB2API_DEBUG_RECLAUDE_ENVELOPE_DIR 配置时落盘完整字节。
		dumpRejectedEnvelope(envelope, traceID)
	}
	if isGatewayErr && u.events != nil {
		u.events.HandleReclaudeGatewayError(account, gatewayErr.StatusCode)
	}

	// 成功的调用在计费口径上才知道 token 数，由 GatewayService.recordReclaudeQuota 记；
	// 失败的调用拿不到 usage，只能在这里按次记，否则水位系统性低估 ⇒ 超卖。
	if err != nil || synthesized == nil || synthesized.StatusCode >= http.StatusBadRequest {
		u.recordFailedCall(account)
	}

	u.attachTelemetry(account, synthesized, timer, reclaudeEdgeLabel(gatewayURL), err != nil, status)
	return synthesized, err
}

// attachTelemetry 给响应挂上计量，在流读完时结算这次调用。
//
// 🔴 失败路径**当场结算**，成功路径**等流结束**。真值里 dur 只统计成功请求
// （sum(dur_buckets) == ok_n），而失败请求往往没有可读的流；
// 两条路径用同一个时机结算，必然有一边的数字是编的。
func (u *ReclaudeUpstream) attachTelemetry(
	account *Account, synthesized *http.Response,
	timer *reclaudeCallTimer, edge string, failed bool, status int,
) {
	if u == nil || u.telemetry == nil {
		return
	}

	sample := ReclaudeTelemetrySample{Edge: edge, TTFT: timer.ttft}

	if failed || synthesized == nil || status >= http.StatusBadRequest {
		sample.Outcome = ReclaudeOutcomeHTTPError
		if synthesized != nil && synthesized.ContentLength > 0 {
			sample.Bytes = synthesized.ContentLength
		}
		u.recordTelemetry(account, sample, time.Now())
		return
	}
	if synthesized.Body == nil {
		sample.Outcome = ReclaudeOutcomeOk
		sample.Duration = timer.elapsed(time.Now())
		u.recordTelemetry(account, sample, time.Now())
		return
	}

	synthesized.Body = newReclaudeMeasuredBody(synthesized.Body, func(bytes int64) {
		now := time.Now()
		sample.Outcome = ReclaudeOutcomeOk
		sample.Duration = timer.elapsed(now)
		sample.Bytes = bytes
		u.recordTelemetry(account, sample, now)
	})
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
func buildReclaudeEnvelope(inner *http.Request, gatewayURL string, account *Account) ([]byte, string, error) {
	var body []byte
	if inner.Body != nil {
		read, err := io.ReadAll(inner.Body)
		_ = inner.Body.Close()
		if err != nil {
			return nil, "", fmt.Errorf("reclaude upstream: read inner request body: %w", err)
		}
		body = read
	}

	headers := make(map[string]string, len(inner.Header)+2)
	for name, values := range inner.Header {
		if len(values) == 0 {
			continue
		}
		// 对端要求全小写。内层 header 的**取值**一个字不改 —— 那是 CC 伪装的产物。
		headers[strings.ToLower(name)] = values[0]
	}

	// 🔴 用真机指纹覆盖 x-stainless-os/arch/runtime-version（第六次撤销根因）。
	//
	// CC 伪装（applyClaudeCodeMimicHeaders）给所有 OAuth 账号无差别注入
	// x-stainless-os=MacOS —— 那是**给 Anthropic 上游选的**（23 样本 0 个 Linux，
	// 避免独特化）。但 reclaude 网关不是 Anthropic：真客户端的 x-stainless-os 由
	// claude-cli SDK 运行时读 process.platform 现算，**恒等于真机 OS**
	// （反编译到函数体 + 7/7 抓包印证），且登录 /auth/start 已把真机 os/arch
	// 落库。给 Anthropic 的 MacOS 装进 reclaude 信封内层，就是「同一设备内层报
	// Mac、登录档案报 Linux」的自相矛盾 —— 没有任何合理解释的一枪毙命信号。
	//
	// 只在这里覆盖：Anthropic 直连路径完全不经过这里，其 MacOS 伪装原样保留。
	overrideReclaudeStainlessHeaders(headers, account)

	// 🔴 host / content-length 必须显式补进去。
	//
	// Go 把这两项放在 http.Request 的结构体字段（Host / ContentLength）里，不进
	// Header map，照抄 inner.Header 会把它们漏掉。而 2026-09-23 抓到的真实客户端
	// 信封里两者都在 —— 对端是拿 headers 原样去重建上游请求的。
	if inner.Host != "" {
		headers["host"] = inner.Host
	} else if inner.URL != nil {
		headers["host"] = inner.URL.Host
	}
	headers["content-length"] = strconv.Itoa(len(body))

	// 🔴 这两个头缺一不可 —— 2026-09-24 抓真实客户端信封实测：少了会被网关
	// 拒为 {"code":"bad_envelope"}（而信封二进制结构、六个顶层字段、base64
	// 变体全部一致，差异只在这里）。
	//
	// 只在下游没带时补：下游真是 Claude Code 时，它自己的值才是真的。
	if headers["x-claude-code-request-class"] == "" {
		headers["x-claude-code-request-class"] = "main"
	}
	if headers["x-client-request-id"] == "" {
		// 逐请求随机：固定值等于给所有请求盖同一个戳。
		headers["x-client-request-id"] = uuid.NewString()
	}

	// 🔴 必须用 reclaude.NewTraceID()，不能用 uuid.NewString()：
	// 真客户端的 traceId 是纯随机 hex，不设 version/variant 位（见该函数注释）。
	traceID := reclaude.NewTraceID()
	envelope, err := reclaude.EncodeEnvelope(reclaude.ClientRequestMetadata{
		URL:     inner.URL.String(),
		Method:  inner.Method,
		Headers: headers,
		TraceID: traceID,
		// 真实客户端恒发这两项。结构体上它们是 omitempty，不显式赋值整个字段
		// 就从 JSON 里消失，信封形状与真值对不上。
		//
		// edge 按真实现查表（见 reclaudeEdgeLabel）：打主域时是 "unknown"，
		// 打 route 节点时是 host 本身。**不要写死成任一个输出**。
		Edge:      reclaudeEdgeLabel(gatewayURL),
		Keepalive: true,
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
	//
	// 🔴 必须是 Reclaude profile（强制 HTTP/1.1 + ALPN 只报 http/1.1）。
	// 此前用 LongStream 是错的：那个 profile 强制 H2 并周期性发 h2 PING，
	// 而真客户端（📄 newDirectTransport + ✅ 抓包）只走 HTTP/1.1。
	ctx := WithHTTPUpstreamProfile(inner.Context(), HTTPUpstreamProfileReclaude)
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

	// 12.5：**内层**凭据失效检测。
	//
	// 🔴 2026-09-25 生产事故：设备被解绑时，外层是 **200**（信封本身正常），
	// 400 在**内层**：
	//
	//	{"type":"error","error":{"type":"authentication_error",
	//	 "code":"device_revoked","message":"此设备已被解绑…"}}
	//
	// 于是它既不走 HandleReclaudeGatewayError（那条路只看外层状态码），
	// 也不被 ClassifyReclaudeGatewayStatus 分类 —— 账号照常 active+schedulable，
	// 带着一副已经作废的凭据被持续调度了 30 多分钟，每一条都在对端那里
	// 留下一次「已撤销设备仍在尝试」的记录。
	u.detectInnerCredentialRevoked(synthesized, account)

	return synthesized, nil
}

// reclaudeInnerErrorPeekBytes 是为识别内层错误码而预读的字节数。
//
// 错误体只有几百字节；正常响应**一个字节都不预读**（先看状态码）。
const reclaudeInnerErrorPeekBytes = 2 << 10

// detectInnerCredentialRevoked 检查内层响应是不是「设备已被解绑」，是则停号。
//
// 🔴 只在内层状态码 >= 400 时才碰 body：正常响应（含 SSE 流）绝不能预读 ——
// 预读会打乱流式边界，让下游收到残缺的第一个事件。
func (u *ReclaudeUpstream) detectInnerCredentialRevoked(resp *http.Response, account *Account) {
	if u == nil || u.events == nil || resp == nil || resp.Body == nil {
		return
	}
	if resp.StatusCode < http.StatusBadRequest {
		return
	}

	peeked, err := io.ReadAll(io.LimitReader(resp.Body, reclaudeInnerErrorPeekBytes))
	// 无论成功与否都要把读到的字节还回去，否则下游拿到的是残缺错误体。
	resp.Body = &reclaudeRestoredBody{
		Reader: io.MultiReader(bytes.NewReader(peeked), resp.Body),
		closer: resp.Body,
	}
	if err != nil {
		return
	}
	if !bytes.Contains(peeked, []byte(ReclaudeDeviceRevokedCode)) {
		return
	}

	logger.LegacyPrintf("service.reclaude",
		"inner credential revoked for account %d (status %d): stopping scheduling",
		account.ID, resp.StatusCode)
	u.events.HandleReclaudeGatewayError(account, http.StatusUnauthorized)
}

// reclaudeRestoredBody 把预读的前缀与剩余流拼回，并保留原始 Close。
type reclaudeRestoredBody struct {
	io.Reader
	closer io.Closer
}

func (b *reclaudeRestoredBody) Close() error {
	return b.closer.Close()
}

// reclaudeEnvelopeBody 把信封剩余流包成 ReadCloser，Close 时关闭原始 body。
type reclaudeEnvelopeBody struct {
	reader io.Reader
	closer io.Closer
}

func (b *reclaudeEnvelopeBody) Read(p []byte) (int, error) { return b.reader.Read(p) }
func (b *reclaudeEnvelopeBody) Close() error               { return b.closer.Close() }

// doViaDaemon 把内层请求经本机 reclaude daemon 的正向代理发出去。
//
// 与自研信封路径的区别：这里**不封信封、不签名、不改 URL** —— 请求原样发往
// api.anthropic.com，daemon 拦下后自己完成信封、签名与节点选择。
//
// 🔴 内层 Authorization 必须已经是本设备的 SK（调用方在进入本函数前注入）：
// daemon 靠它识别设备，缺失时封装失败并回 bad_envelope。
func (u *ReclaudeUpstream) doViaDaemon(
	inner *http.Request, account *Account, daemonProxy string,
) (*http.Response, error) {
	// 账号绑定的住宅代理在 daemon 那一侧生效（它自己的出站配置），
	// 这里传 daemon 地址本身作为代理 —— 出站只有回环一跳。
	resp, err := u.httpUpstream.DoWithTLS(inner, daemonProxy, account.ID, account.Concurrency, nil)
	if err != nil {
		// 请求可能已经出网：对端可能已扣配额，必须记。
		u.recordFailedCall(account)
		return nil, fmt.Errorf("reclaude daemon transport: %w", err)
	}
	return resp, nil
}
