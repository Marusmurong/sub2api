package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// ReclaudeLifecycleForwarder 把一条合成请求按信封协议发给网关。
//
// 就是 ReclaudeUpstream.Do —— 定义成小接口只为让测试不必搭一整个转发器。
type ReclaudeLifecycleForwarder interface {
	Do(inner *http.Request, account *Account, proxyURL string) (*http.Response, error)
}

// ReclaudeLifecycleSender 发送合成的生命周期流量。
//
// 两种发法：
//   - SendAsync：遥测等旁路流量，永不阻塞下游；
//   - SendSync：新会话的引导批，**发完才返回**（E2-L3）—— 真客户端是引导发完再发
//     第一条推理，09-26 sub 实发里推理先出门是每次新会话都能看出来的时序矛盾。
//
// 🔴 无论哪种，合成流量失败（网关 429、代理抖动）绝不能把用户的请求也带失败：
// 失败只记日志，不传播。
type ReclaudeLifecycleSender struct {
	forwarder ReclaudeLifecycleForwarder
	cipher    *ReclaudeCredentialCipher
	// sleep 可注入，测试里换成立即返回，避免真的等三秒。
	sleep func(time.Duration)
}

// NewReclaudeLifecycleSender 构造发送器。
func NewReclaudeLifecycleSender(
	forwarder ReclaudeLifecycleForwarder, cipher *ReclaudeCredentialCipher,
) *ReclaudeLifecycleSender {
	return &ReclaudeLifecycleSender{
		forwarder: forwarder,
		cipher:    cipher,
		sleep:     func(d time.Duration) { time.Sleep(d) },
	}
}

// reclaudeLifecycleTimeout 是整批合成流量的总时限。
//
// 引导批最后一条在 +3.2s，留足余量后封顶 —— 没有上限的话，
// 一批卡住的合成请求会一直占着代理连接，而那条代理是推理流量共用的。
const reclaudeLifecycleTimeout = 45 * time.Second

// reclaudeBootstrapSyncTimeout 是同步引导批的整批上限。
//
// 🔴 2026-09-27 从 8s 上调到 30s:并发发送后,整批墙钟 = 末条 Delay(+2.4s) +
// 单条经代理 RTT,正常 ~7s 就 wg.Wait() 返回;只有代理全卡才会等满。8s 太短会
// 在慢代理下又截断引导(撤销根因)。这个上限只是"全卡住"的兜底,不是常态耗时。
// 并发 + 每条独立超时(reclaudePerRequestTimeout)后,单条慢不再拖垮整批。
const reclaudeBootstrapSyncTimeout = 30 * time.Second

// SendAsync 起一个后台任务按时序发完这批请求。
//
// 🔴 ctx 不能用请求的 context：下游的 ctx 在响应返回时就被取消了，
// 而引导流量的最后一条在 +3.2 秒 —— 用它会让这批请求发一半就全部被掐断，
// 在对端看来是「每次启动都中途崩溃」，比不发更可疑。
func (s *ReclaudeLifecycleSender) SendAsync(
	account *Account, proxyURL string, requests []ReclaudeLifecycleRequest,
) {
	if s == nil || s.forwarder == nil || account == nil || len(requests) == 0 {
		return
	}
	go s.send(account, proxyURL, requests, reclaudeLifecycleTimeout)
}

// SendSync 同步发完这批请求后才返回（引导批专用，见 reclaudeBootstrapSyncTimeout）。
func (s *ReclaudeLifecycleSender) SendSync(
	account *Account, proxyURL string, requests []ReclaudeLifecycleRequest,
) {
	if s == nil || s.forwarder == nil || account == nil || len(requests) == 0 {
		return
	}
	s.send(account, proxyURL, requests, reclaudeBootstrapSyncTimeout)
}

// reclaudePerRequestTimeout 是**单条**合成信封的超时。
//
// 🔴 2026-09-27 根因:此前整批共用一个 8s ctx，串行发。经住宅代理打 /proxy 每条
// RTT 高，发到第 5 条(penguin)就耗尽 8s → context deadline exceeded → 整批 return，
// 后 7 条引导(registry/settings/grove/model_selector)全丢，会话形态残缺 → 撤销。
// 改为每条独立超时:一条慢/失败不拖垮、不中断其余。
const reclaudePerRequestTimeout = 20 * time.Second

func (s *ReclaudeLifecycleSender) send(
	account *Account, proxyURL string, requests []ReclaudeLifecycleRequest, timeout time.Duration,
) {
	defer func() {
		// 合成流量是旁路，panic 绝不能掀翻进程。
		if r := recover(); r != nil {
			logger.LegacyPrintf("service.reclaude",
				"lifecycle sender panicked for account %d: %v", account.ID, r)
		}
	}()

	// 整批总预算:防止一批卡住的合成请求无限占用代理连接（推理流量共用）。
	// 不再用它做单条超时——单条各自独立(reclaudePerRequestTimeout)。
	batchCtx, cancelBatch := context.WithTimeout(context.Background(), timeout)
	defer cancelBatch()

	sk, err := s.cipher.DecryptSecret(account, CredKeyReclaudeSK)
	if err != nil {
		logger.LegacyPrintf("service.reclaude",
			"lifecycle sender cannot read sk for account %d: %v", account.ID, err)
		return
	}

	// 🔴 并发发送:真客户端的引导批时序密集(末条 +2.4s),串行经代理发 12 条会
	// 累计到几十秒。并发既贴近真值时序,又绕开"串行累计超时"这个撤销根因。
	// 每条按自己的 Delay 起跑(复刻时序),各自独立超时,一条失败不影响其余。
	var wg sync.WaitGroup
	for _, spec := range requests {
		if batchCtx.Err() != nil {
			break // 整批总预算耗尽才停(极端情况),正常不会到这
		}
		wg.Add(1)
		go func(spec ReclaudeLifecycleRequest) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					logger.LegacyPrintf("service.reclaude",
						"lifecycle sendOne panicked for account %d: %v", account.ID, r)
				}
			}()
			// 按 Delay 复刻真值时序(相对会话起点的绝对时刻)。
			// 用 s.sleep 注入点:测试可换成立即返回,不真的等。
			if spec.Delay > 0 {
				s.sleep(spec.Delay)
				if batchCtx.Err() != nil {
					return
				}
			}
			// 🔴 每条独立超时:不共享整批预算,一条慢不拖垮后面。
			reqCtx, cancel := context.WithTimeout(context.Background(), reclaudePerRequestTimeout)
			defer cancel()
			s.sendOne(reqCtx, account, proxyURL, sk, spec)
		}(spec)
	}
	wg.Wait()
}

func (s *ReclaudeLifecycleSender) sendOne(
	ctx context.Context, account *Account, proxyURL, sk string, spec ReclaudeLifecycleRequest,
) {
	// 🔴 打上合成标记：没有它，这条请求进 Do 之后又会触发一批新的合成流量，
	// 指数爆炸。见 WithReclaudeSynthetic 的注释。
	var payload io.Reader = http.NoBody
	if len(spec.Body) > 0 {
		payload = bytes.NewReader(spec.Body)
	}
	req, err := http.NewRequestWithContext(
		WithReclaudeSynthetic(ctx), spec.Method, spec.URL, payload)
	if err != nil {
		logger.LegacyPrintf("service.reclaude",
			"lifecycle request build failed (%s): %v", spec.URL, err)
		return
	}
	for name, value := range spec.Headers {
		// setHeaderRaw 写小写键：装箱时 headers 统一转小写，
		// 用 Header.Set 会另建规范大小写的键，两者在 map 里互相覆盖。
		setHeaderRaw(req.Header, strings.ToLower(name), value)
	}
	if spec.NeedsAuthorization {
		setHeaderRaw(req.Header, "authorization", "Bearer "+sk)
	}
	if len(spec.Body) > 0 {
		// 装箱时 content-length 由 buildReclaudeEnvelope 按实际字节写入，
		// 这里设 ContentLength 是为了让它读得到真实长度。
		req.ContentLength = int64(len(spec.Body))
	}

	resp, err := s.forwarder.Do(req, account, proxyURL)
	if err != nil {
		// 只记不传播：合成流量失败不该影响任何人。
		logger.LegacyPrintf("service.reclaude",
			"lifecycle request failed (%s %s) for account %d: %v",
			spec.Method, spec.URL, account.ID, err)
		return
	}
	// 🔴 成功路径也要留痕。此前这条链路**完全静默** —— 线上查「生命周期流量
	// 有没有在发」只能看到 0 条日志，而 0 条既可能是「全部成功」，也可能是
	// 「压根没触发」。两者的处置完全不同（2026-09-25 为此排查了一轮）。
	// 配额计数器不能用来判断：合成流量成功时不走计费路径，从不计数。
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	logger.LegacyPrintf("service.reclaude",
		"lifecycle sent: account=%d %s %s -> %d", account.ID, spec.Method, spec.URL, status)

	if resp != nil && resp.Body != nil {
		// 必须读完并关闭，否则连接不复用，反而制造额外 TCP 会话。
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}
