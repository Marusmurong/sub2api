package service

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
)

// ReclaudeLifecycleDriver 决定每条推理该带哪些生命周期流量。
//
// 它是 D3 的入口：把「会话边界判定」「该发什么」「怎么发」三者接起来，
// 而转发器只需要调一次 OnInference。
type ReclaudeLifecycleDriver struct {
	tracker *ReclaudeSessionTracker
	sender  *ReclaudeLifecycleSender
	now     func() time.Time

	mu sync.Mutex
	// sessions 记每个账号当前会话的 id 与起点，供遥测事件填
	// session_id / process.uptime —— 真值里同一会话的事件共享同一个 id。
	sessions map[int64]reclaudeSessionState
	// pendingEvents 是尚未上报的遥测事件名，按账号累计（见 maybeFlushEvents）。
	pendingEvents map[int64][]string
	// lastEventFlush 记每个账号上次上报的时刻。
	lastEventFlush map[int64]time.Time
}

type reclaudeSessionState struct {
	id      string
	startAt time.Time
}

// NewReclaudeLifecycleDriver 构造驱动器。
func NewReclaudeLifecycleDriver(sender *ReclaudeLifecycleSender) *ReclaudeLifecycleDriver {
	return &ReclaudeLifecycleDriver{
		tracker:        NewReclaudeSessionTracker(),
		sender:         sender,
		now:            time.Now,
		sessions:       map[int64]reclaudeSessionState{},
		pendingEvents:  map[int64][]string{},
		lastEventFlush: map[int64]time.Time{},
	}
}

// OnInference 在每条推理请求出网前调用。
//
// 🔴 三道闸，缺一不可：
//  1. 合成请求自己不触发合成 —— 否则每条合成请求再生成一批，指数爆炸。
//  2. 只对 reclaude 账号生效。
//  3. 无代理时不发 —— 与心跳同一条红线：宁可不发，也不从机房 IP 出去。
//
// model 是**这次请求真实使用的模型**，进遥测事件的 model 字段。
// 传空串时不影响其余流量，只是事件里的 model 为空。
func (d *ReclaudeLifecycleDriver) OnInference(
	ctx context.Context, account *Account, proxyURL, model string,
) {
	if d == nil || d.sender == nil || account == nil || !account.IsReclaude() {
		return
	}
	// 🔴 递归防线。
	if IsReclaudeSynthetic(ctx) {
		return
	}
	if proxyURL == "" {
		return
	}
	// 🔴 daemon 模式不合成（E1）。本机真客户端自己会发引导 / 遥测，再补一份就是
	// 双份；而且我们的合成 GET 会带着自己的缺陷经 daemon 转出去，把「daemon 装箱
	// 是否被撤」这个对照实验搞脏。Do 在 daemon 分支之前调用本函数，闸必须在这里。
	if _, ok := ReclaudeDaemonEndpoint(account); ok {
		return
	}
	if _, ok := ReclaudeDaemonProxy(account); ok {
		return
	}
	// 🔴 忠实中继模式：下游真 claude 自己发 bootstrap / event_logging / mcp_servers,
	// 由中继路由原样转发到 la.route,此处不再合成——否则真+合成双份反而更假。
	// 默认关,只对做真机对照(E1)的账号开。见 reclaude_relay_mode.go。
	if ReclaudeRelayLifecycleEnabled(account) {
		return
	}

	// 🔴 内层 claude-cli/claude-code UA 用 **claude-cli 版本**(machine_env.cli_version,
	// 如 2.1.282),不是 reclaude 包装器版本(client_version=v1.4.0)。2026-09-26 抓包实证
	// 旧代码误用后者,合成流量全发成 claude-cli/1.4.0(不存在的版本)。cli_version 缺失时
	// 由 reclaudeClientVersionOr 兜底到基线,不再退回 v1.4.0。
	cliVersion := parseReclaudeMachineEnv(account.GetCredential(CredKeyReclaudeMachineEnv)).CliVersion
	orgUUID := account.GetCredential(CredKeyReclaudeOrganizationUUID)
	now := d.now()

	// 冷/暖必须在 ObserveAt 之前算：ObserveAt 会覆盖 lastSeen。
	cold := d.isColdStart(account.ID, now)
	if d.tracker.ObserveAt(account.ID, now) {
		d.startSession(account.ID, now)
		// 🔴 引导批**同步**发完再放推理（E2-L3）。09-26 sub 实发信封里推理比 profile
		// 早 4 毫秒签名出门，真客户端是引导发完（约 2.4 秒）再发第一条推理。
		// 代价是每次新会话的首条推理多等约 2.4 秒；上限见 reclaudeBootstrapSyncTimeout。
		d.sender.SendSync(account, proxyURL, BuildReclaudeBootstrapRequests(ReclaudeBootstrapParams{
			CLIVersion: cliVersion,
			OrgUUID:    orgUUID,
			Model:      model,
			Cold:       cold,
		}))
	}
	// 09-26 真值里两次推理之间没有 mcp_servers 复查，会话内的后续推理不再带任何引导。

	// 🔴 遥测**攒批**，不是每条推理发一次。
	//
	// 2026-09-25 复盘：此前每条推理立刻发一批（只含 2 个事件），
	// 于是 3 分钟内发了 11 次。真值是攒批语义 ——
	// ✅ 抓包实测每批 **103~107 个事件**，一次会话共 7 批。
	// 批大小差 50 倍、频次差一个量级，这是我自己引入的可判别特征。
	//
	// 遥测仍异步：真值里它跟在推理之后几秒，不阻塞推理。
	//
	// 🔴 event_logging 与 Datadog 同一个 flush 时机一起发：真客户端一批推理周围
	// 两路遥测并存（Anthropic event_logging + Datadog intake），且共享同一 session_id /
	// uptime / env 快照。分开发或只发一路，都会让「两路遥测对不上」成为可判别信号。
	if event := d.maybeFlushEvents(account, cliVersion, model, now); event != nil {
		logger.LegacyPrintf("service.reclaude",
			"lifecycle dispatch: account=%d event_logging", account.ID)
		reqs := []ReclaudeLifecycleRequest{*event}
		if dd := d.buildDatadogRequest(account, model, now); dd != nil {
			reqs = append(reqs, *dd)
		}
		d.sender.SendAsync(account, proxyURL, reqs)
	}
}

// buildDatadogRequest 在遥测 flush 时产出一条 Datadog 日志请求（与 event_logging 同批）。
//
// 与 maybeFlushEvents 共享 session_id 与会话起点 —— 两路遥测的 env / process /
// session_id 必须严格同源（见 driver 顶部 Datadog 说明）。缺真值身份字段时
// BuildReclaudeDatadogBatch 返回 nil，这里也返回 nil（不发可解释）。
func (d *ReclaudeLifecycleDriver) buildDatadogRequest(
	account *Account, model string, now time.Time,
) *ReclaudeLifecycleRequest {
	session, ok := d.sessionState(account.ID)
	if !ok {
		return nil
	}
	batch := BuildReclaudeDatadogBatch(ReclaudeEventContext{
		Account:   account,
		SessionID: session.id,
		Model:     model,
		Uptime:    now.Sub(session.startAt),
		Now:       now,
	})
	if len(batch) == 0 {
		return nil
	}
	body, err := json.Marshal(batch)
	if err != nil {
		return nil
	}
	// 真值里 Datadog 跟在推理之后几秒（与 event_logging 同批）。
	req := BuildReclaudeDatadogRequest(body, 3*time.Second)
	return &req
}

// ReclaudeColdStartGap 是「静默多久之后再来的启动算冷启动」。
//
// 真值：09-26 同一台机器 35 分钟后第二次启动**没有** claude_cli/bootstrap 与
// penguin_mode —— 它们有缓存，不是每次进程启动都拉。缓存 TTL 抓包推不出来，
// 取 6 小时是工程取舍：短于它会让一天冷启动十几次，长于它又回到「从不冷启动」。
const ReclaudeColdStartGap = 6 * time.Hour

// isColdStart 判断这次启动该不该带冷启动才有的引导（bootstrap / penguin）。
func (d *ReclaudeLifecycleDriver) isColdStart(accountID int64, now time.Time) bool {
	last, seen := d.tracker.LastSeen(accountID)
	if !seen {
		return true
	}
	return now.Sub(last) >= ReclaudeColdStartGap
}

// ReclaudeEventFlushInterval 是遥测攒批的最小间隔。
//
// ⚠️ 真值样本的批间隔是 0.5~14 秒，但那是**一次交互式会话内**的分布 ——
// 每批都在重发整个会话的累计事件（103→103→103→107），说明它是
// 「有新事件就把当前全量再发一次」而非「攒够再发」。
// 我们的事件量远小于真客户端（只有三种 api_* 事件），照搬秒级间隔会变成
// 高频发送微批。取 60 秒：既不高频，也不会攒到一个不合理的大批。
const ReclaudeEventFlushInterval = time.Minute

// maybeFlushEvents 在距上次上报足够久时产出一条 event_logging 请求。
//
// 攒批期间的事件累计在 pendingEvents 里，一次性发出 —— 与真值的
// 「一批含整段会话的事件」语义一致。
//
// 只发三种事件：它们都对应一次**真实的上游调用**（见 reclaude_event_logging.go
// 顶部的选择标准）。真客户端一批里有 42 种事件，其余都是 CLI 自身子系统的状态，
// 我们没有那些子系统，填了就是与实际行为矛盾的证词。
func (d *ReclaudeLifecycleDriver) maybeFlushEvents(
	account *Account, clientVersion, model string, now time.Time,
) *ReclaudeLifecycleRequest {
	session, ok := d.sessionState(account.ID)
	if !ok {
		return nil
	}

	// 本次推理产生的事件先入账，再决定这一刻发不发。
	names := d.accumulateEvents(account.ID, now)
	if len(names) == 0 {
		return nil
	}

	batch := BuildReclaudeEventBatch(ReclaudeEventContext{
		Account:   account,
		SessionID: session.id,
		Model:     model,
		Uptime:    now.Sub(session.startAt),
		Now:       now,
	}, names)
	if batch == nil {
		return nil
	}

	body, err := json.Marshal(batch)
	if err != nil {
		return nil
	}
	// 真值里 event_logging 跟在推理之后几秒，不与之同时。
	request := BuildReclaudeEventLoggingRequest(clientVersion, body, 3*time.Second)
	return &request
}

// accumulateEvents 记下本次推理产生的事件，到点时取走全部待发事件。
//
// 未到间隔返回 nil（继续攒）；到点返回累计的全部事件名并清空。
func (d *ReclaudeLifecycleDriver) accumulateEvents(accountID int64, now time.Time) []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	// 每条真实推理产生这两个事件 —— 它们都对应一次真实的上游调用。
	d.pendingEvents[accountID] = append(d.pendingEvents[accountID],
		ReclaudeEventAPIQuery, ReclaudeEventAPICacheBreakpoints)

	last, seen := d.lastEventFlush[accountID]
	if seen && now.Sub(last) < ReclaudeEventFlushInterval {
		return nil
	}
	d.lastEventFlush[accountID] = now

	pending := d.pendingEvents[accountID]
	delete(d.pendingEvents, accountID)
	return pending
}

// startSession 开一个新会话：重置轮次并生成新的 session_id。
//
// 🔴 session_id 必须逐会话变化。固定值 = 一台「永远在同一个会话里」的机器，
// 而真值里它跟随 CLI 进程。
func (d *ReclaudeLifecycleDriver) startSession(accountID int64, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sessions[accountID] = reclaudeSessionState{id: uuid.NewString(), startAt: now}
	// 新会话重置攒批状态：不清的话，上一个会话的事件会带着旧 session_id
	// 被算进新会话的第一批里。同时删掉 lastEventFlush，让新会话立刻上报一次
	// —— 真值是「启动即 flush」。
	delete(d.pendingEvents, accountID)
	delete(d.lastEventFlush, accountID)
}

func (d *ReclaudeLifecycleDriver) sessionState(accountID int64) (reclaudeSessionState, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	session, ok := d.sessions[accountID]
	return session, ok
}
