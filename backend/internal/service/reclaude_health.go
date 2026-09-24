package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/reclaude"
)

// reclaude 网关的非推理端点。
const (
	// ReclaudeInterceptDomainsPath 动态拦截清单，原版 daemon 每 60s 打一次（带 ETag）。
	ReclaudeInterceptDomainsPath = "/client/intercept-domains"
	// ReclaudeClientAccountPath 账号/订阅状态，原版 daemon 每 60s 打一次。
	ReclaudeClientAccountPath = "/client/account"
	// ReclaudeHealthReadyPath 探活。
	ReclaudeHealthReadyPath = "/health/ready"
	// ReclaudeTelemetryPath 遥测端点 —— **我们不发**，见下方注释。
	ReclaudeTelemetryPath = "/client/telemetry"
)

// ReclaudeHeartbeatInterval 是心跳周期。
//
// 60 秒，对齐原版 daemon 的两个后台循环（intercept.SyncLoop / daemonAccountSyncLoop）。
// 一台每几十分钟才心跳一次的设备，在他们的时序里是明显稀疏的；
// 而「最近使用」是设备页面上的产品级一等字段。成本可忽略：两个空请求/分钟。
const ReclaudeHeartbeatInterval = time.Minute

// 心跳必须**单副本**执行。
//
// 🔴 sub2api 支持多副本。若每个进程各跑一份 ticker，3 副本 ⇒ 每台「设备」
// 每分钟发 6 个请求、来自 3 个 TCP 会话 —— 在对端看来是一台机器同时开着
// 3 个 daemon，比不心跳还糟。
const (
	ReclaudeHeartbeatLeaderLockKey = "reclaude:heartbeat:leader"
	// TTL 必须长于心跳周期，否则每个周期都会重新选主，多副本会同时发。
	ReclaudeHeartbeatLeaderLockTTL = 3 * ReclaudeHeartbeatInterval
)

// ReclaudeEndpointProbe 向 reclaude 网关的非推理端点发一次请求。
type ReclaudeEndpointProbe interface {
	ProbeReclaudeEndpoint(ctx context.Context, account *Account, endpoint string) (*http.Response, error)
}

// ReclaudeHeartbeatRunner 维持「客户端存在感」。
//
// 遥测**刻意不发**，三条理由：
//  1. RECLAUDE_TELEMETRY_DISABLE 是面向用户的开关 ⇒ 用户可合法关闭并继续使用
//     ⇒ 服务端不可能把遥测当作 /proxy 的必要条件。这是结构性论据，不是推测。
//  2. 它上报的 passthrough_hosts 对我们恒为空，发了只是暴露 TTFT 与流量规模。
//  3. 遥测不含机器指纹（机器配置冻结在 login 那一刻），不发也不会穿帮。
type ReclaudeHeartbeatRunner struct {
	probe    ReclaudeEndpointProbe
	observer ReclaudeAccountActuator
}

// NewReclaudeHeartbeatRunner 构造心跳器。
func NewReclaudeHeartbeatRunner(probe ReclaudeEndpointProbe) *ReclaudeHeartbeatRunner {
	return &ReclaudeHeartbeatRunner{probe: probe}
}

// SetAccountObserver 注入执行器，让心跳顺带做换号检测。
//
// 用 setter 而不是加构造参数：换号检测是心跳的**副产品**，缺席时心跳本身照常工作。
func (r *ReclaudeHeartbeatRunner) SetAccountObserver(observer ReclaudeAccountActuator) {
	if r == nil {
		return
	}
	r.observer = observer
}

// Beat 按当前时刻执行一次心跳。
func (r *ReclaudeHeartbeatRunner) Beat(ctx context.Context, account *Account) error {
	return r.BeatAt(ctx, account, time.Now())
}

// BeatAt 按指定时刻执行一次心跳。
//
// 模拟关机期间必须**整段**停掉心跳：只停调度不停心跳的话，设备页面的
// 「最近使用」照常刷新，等于关机没生效 —— 而「从不关机」正是要消除的特征。
func (r *ReclaudeHeartbeatRunner) BeatAt(ctx context.Context, account *Account, at time.Time) error {
	if r == nil || r.probe == nil || account == nil || !account.IsReclaude() {
		return nil
	}
	if _, offline := ReclaudeOfflineUntil(account, at); offline {
		return nil
	}

	for _, endpoint := range []string{ReclaudeInterceptDomainsPath, ReclaudeClientAccountPath} {
		resp, err := r.probe.ProbeReclaudeEndpoint(ctx, account, endpoint)
		if err != nil {
			return fmt.Errorf("reclaude heartbeat %s (account %d): %w", endpoint, account.ID, err)
		}
		if resp == nil || resp.Body == nil {
			continue
		}

		// /client/account 的 body 顺带读出来做换号检测；其余端点直接丢弃。
		// 两种情况都**必须读完并关闭**，否则连接不会被复用，
		// 反而给对端制造出额外的 TCP 会话 —— 与「像一台设备」的目标相反。
		if endpoint == ReclaudeClientAccountPath {
			r.observeAccountState(account, resp.Body)
		}
		// 🔴 记下清单版本，下一轮才能发条件请求。只记不发等于没做 ——
		// 第一次 200 拿到 ETag 后，此后应当一直是 304（✅ 真客户端如此）。
		// 304 通常不重复带 ETag，Remember 对空值是 no-op，不会把已知版本清掉。
		if endpoint == ReclaudeInterceptDomainsPath {
			// 类型断言而不是加进 ReclaudeEndpointProbe 接口：ETag 记忆是探测器的
			// 可选能力，塞进接口会逼着所有实现（含测试桩）都实现一个用不到的方法。
			if remember, ok := r.probe.(interface {
				RememberInterceptETag(int64, string)
			}); ok {
				remember.RememberInterceptETag(account.ID, resp.Header.Get("ETag"))
			}
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	return nil
}

// reclaudeAccountStateMaxBytes 是 /client/account 响应的读取上限。
//
// 这个端点的响应只有几个字段。设上限是因为 body 来自对端：不封顶就等于让
// 对端决定我们在心跳里分配多少内存。
const reclaudeAccountStateMaxBytes = 64 << 10

// observeAccountState 从 /client/account 的响应里读「当前绑定的 Claude 账号」。
//
// 🔴 为什么心跳要管这件事：换号的带外事件（ReclaudeEvent）搭在推理响应上，
// 一个空闲账号可能很久都收不到；而「绑定邮箱变了」是一条**独立的**可见信号。
// 漏掉换号的后果是旧 prev_request_id 被当成假 parent-link 发上去、
// 旧 thinking 签名被新账号拒绝。
//
// 任何解析失败都静默跳过：心跳的首要职责是让设备看起来还活着，
// 解析一个附带字段失败不该把它中断。
func (r *ReclaudeHeartbeatRunner) observeAccountState(account *Account, body io.Reader) {
	if r.observer == nil {
		return
	}

	var state reclaude.ClientAccountResponse
	if err := json.NewDecoder(io.LimitReader(body, reclaudeAccountStateMaxBytes)).Decode(&state); err != nil {
		logger.LegacyPrintf("service.reclaude",
			"failed to decode account state for account %d: %v", account.ID, err)
		return
	}

	observed := strings.TrimSpace(state.UserEmail)
	if observed == "" {
		return
	}

	known, _ := account.Extra[ExtraKeyReclaudeBoundEmail].(string)
	if observed == known {
		return
	}

	kind := ReclaudeActionAccountSwitched
	if known == "" {
		// 建号时没有这个字段，首次拿到只是回填 —— 当成换号会白白推进身份纪元。
		kind = ReclaudeActionBoundEmailObserved
	}

	r.observer.ApplyReclaudeAction(ReclaudeAccountAction{
		Kind:                  kind,
		AccountID:             account.ID,
		Reason:                "observed via /client/account",
		NewAccountMaskedEmail: observed,
	})
}
