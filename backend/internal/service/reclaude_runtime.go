package service

import (
	"context"

	"github.com/google/uuid"
)

// ReclaudeEventDedupWindow 是带外事件的幂等窗口。
//
// 同一个事件会搭在同一账号的多条响应上重复回来（换号事件尤其密集）。
// 窗口太短 ⇒ 身份纪元被连推几次、会话态反复翻页；太长 ⇒ 真的第二次换号被吃掉。
// 取心跳周期的 5 倍：比事件重复的时间尺度大一个量级，比两次换号的间隔小得多。
const ReclaudeEventDedupWindow = 5 * ReclaudeHeartbeatInterval

// ReclaudeRuntime 是 reclaude 的运行时三件套。
//
// 为什么打成一个结构体而不是四五个 wire provider：wire_gen.go 在本仓是**手工维护**
// 的，而 §10.2 已经点名批次 0 会明显抬高下次合并上游的冲突面。装配集中在服务层，
// 接线处就只剩三行，合并时几乎不会撞。
type ReclaudeRuntime struct {
	Upstream  *ReclaudeUpstream
	QuotaGate *ReclaudeQuotaGate
	Scheduler *ReclaudeScheduler
	// SelfChecker 给管理端的「建号后连通性自检」按钮用。
	SelfChecker *ReclaudeSelfChecker
}

// ProvideReclaudeRuntime 装配 reclaude 运行时。
//
// 每个依赖都允许为 nil（单机无 Redis、单测、未启用 reclaude）：缺件时各组件
// 自己退化成安全形态 —— 日闸缺存储一律判超闸、心跳缺代理直接拒发 ——
// 而不是在启动阶段 panic 把整个服务拖下水。
func ProvideReclaudeRuntime(
	accountRepo ReclaudeAccountSource,
	httpUpstream HTTPUpstream,
	encryptor SecretEncryptor,
	usageStore ReclaudeDailyUsageStore,
	lockCache LeaderLockCache,
) *ReclaudeRuntime {
	cipher := NewReclaudeCredentialCipher(encryptor)
	accounts := NewReclaudeAccountRepoAdapter(accountRepo)

	// 事件旁路：信封响应里的带外事件 → 幂等分发 → 落成真实账号状态变更。
	// alerter 暂缺（告警通道尚未接），执行器对 nil alerter 是安全的。
	actuator := NewReclaudeAccountService(accounts, nil)
	events := NewReclaudeEventDispatcher(actuator, ReclaudeEventDedupWindow)

	probe := NewReclaudeGatewayProbe(httpUpstream, cipher)
	heartbeat := NewReclaudeHeartbeatRunner(probe)
	// 心跳顺带做换号检测：带外事件搭在推理响应上，空闲账号可能很久收不到，
	// 而「绑定邮箱变了」是一条独立的可见信号。
	heartbeat.SetAccountObserver(actuator)

	// 遥测：热路径采集 → 300 秒窗口 → 搭心跳的 tick 上报。
	// 🔴 采集器必须与转发器**共用同一个实例**：各持一份的话，转发器记的样本
	// 上报器永远取不到，遥测恒为空 —— 而「有推理、零遥测」正是要消除的矛盾。
	telemetryCollector := NewReclaudeTelemetryCollector()
	telemetry := NewReclaudeTelemetryReporter(probe, telemetryCollector)

	quota := NewReclaudeQuotaGate(usageStore)
	upstream := NewReclaudeUpstream(httpUpstream, cipher, events)
	upstream.SetTelemetryCollector(telemetryCollector)

	// D3：生命周期流量。驱动器把合成请求回送给**同一个** upstream，
	// 因此它们与推理流量共用代理、签名、TLS profile 与配额记账。
	// 递归由 WithReclaudeSynthetic 标记挡住（见 ReclaudeLifecycleDriver.OnInference）。
	upstream.SetLifecycleDriver(
		NewReclaudeLifecycleDriver(NewReclaudeLifecycleSender(upstream, cipher)))
	// 失败调用的按次记账在转发器里发生（那里才知道请求有没有真的出网）。
	upstream.SetQuotaRecorder(quota)

	return &ReclaudeRuntime{
		Upstream:    upstream,
		QuotaGate:   quota,
		SelfChecker: NewReclaudeSelfChecker(accounts, probe, upstream),
		Scheduler: NewReclaudeScheduler(
			accounts, accounts, heartbeat, telemetry, lockCache, uuid.NewString()),
	}
}

// AttachTo 把转发器、日闸与运行时开关注入网关。
//
// 🔴 三者必须**同时**注入：
//   - 只注入转发器 = 拿掉了日闸，账号一直可调度 ⇒ 直接超卖对方的配额包
//   - 不注入开关 = 出事时关不掉（开关缺席时调度端按「关」处理，所以漏注入
//     的表现是这条通道整个不工作 —— 刻意选这个方向）
func (r *ReclaudeRuntime) AttachTo(gateway *GatewayService, settings *SettingService) {
	if r == nil || gateway == nil {
		return
	}
	gateway.SetReclaudeUpstream(r.Upstream)
	gateway.SetReclaudeQuotaGate(r.QuotaGate)
	gateway.SetReclaudeEnabledFunc(settings.IsReclaudeEnabled)
}

// Start 起后台节拍器。
func (r *ReclaudeRuntime) Start() {
	if r == nil {
		return
	}
	r.Scheduler.Start(context.Background())
}

// Stop 停后台节拍器；幂等。
func (r *ReclaudeRuntime) Stop() {
	if r == nil {
		return
	}
	r.Scheduler.Stop()
}
