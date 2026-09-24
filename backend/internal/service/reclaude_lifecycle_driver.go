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
	// turns 记每个账号在**当前会话内**已经发过几次推理，
	// 用来决定要不要带 mcp 复查（真值里只有会话早期带）。
	turns map[int64]int
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
		turns:          map[int64]int{},
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

	clientVersion := account.GetCredential(CredKeyReclaudeClientVersion)
	now := d.now()

	var requests []ReclaudeLifecycleRequest
	if d.tracker.ObserveAt(account.ID, now) {
		d.startSession(account.ID, now)
		requests = BuildReclaudeBootstrapRequests(clientVersion)
	} else {
		// 非新会话：只有会话早期的几次推理带 MCP 复查。
		requests = BuildReclaudeInferenceFollowups(d.nextTurn(account.ID))
	}

	// 🔴 遥测**攒批**，不是每条推理发一次。
	//
	// 2026-09-25 复盘：此前每条推理立刻发一批（只含 2 个事件），
	// 于是 3 分钟内发了 11 次。真值是攒批语义 ——
	// ✅ 抓包实测每批 **103~107 个事件**，一次会话共 7 批。
	// 批大小差 50 倍、频次差一个量级，这是我自己引入的可判别特征。
	if event := d.maybeFlushEvents(account, clientVersion, model, now); event != nil {
		requests = append(requests, *event)
	}

	logger.LegacyPrintf("service.reclaude",
		"lifecycle dispatch: account=%d requests=%d", account.ID, len(requests))
	d.sender.SendAsync(account, proxyURL, requests)
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
	d.turns[accountID] = 1
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

func (d *ReclaudeLifecycleDriver) nextTurn(accountID int64) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.turns[accountID]++
	return d.turns[accountID]
}
