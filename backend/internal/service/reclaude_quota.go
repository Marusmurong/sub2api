package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// ReclaudeFailedCallTokenEstimate 是失败上游调用的保守 token 估算。
//
// 为什么不能按 0 计：一个下游请求最多打出约七次上游调用（五次重试 + 400 纠错
// 路径），失败的那几次返回 nil usage、不进计费链路，**但对方的配额是实打实扣
// 了的**。按 0 计就是系统性低估水位，而低估的方向正好是超卖。
//
// ⚠️ 这个数字是占位值，需要用 Phase 0 的限流反推与 Phase 2 的长跑校准。
// 它的意义不在于准，而在于**不为零**。
const ReclaudeFailedCallTokenEstimate int64 = 2000

// reclaudeWeeklySoftLimitFactor 周软闸 = 日闸 × 7 × 0.8。
const reclaudeWeeklySoftLimitFactor = 0.8

// ReclaudeDailyUsage 是一台设备当天的水位。
//
// 三个计数同时维护：token 是计费口径，上游调用次数是**配额口径**，
// 两者对 reclaude 并不等价。
type ReclaudeDailyUsage struct {
	Tokens              int64 `json:"tokens"`
	UpstreamCalls       int64 `json:"upstream_calls"`
	FailedUpstreamCalls int64 `json:"failed_upstream_calls"`
}

// EffectiveTokens 是用于闸门判定的水位：已知 token + 失败调用的保守估算。
func (u ReclaudeDailyUsage) EffectiveTokens() int64 {
	return u.Tokens + u.FailedUpstreamCalls*ReclaudeFailedCallTokenEstimate
}

// ReclaudeDailyUsageStore 是日水位的存储（生产实现走 Redis + DB 快照）。
type ReclaudeDailyUsageStore interface {
	AddReclaudeDailyUsage(ctx context.Context, accountID int64, day string, delta ReclaudeDailyUsage) (ReclaudeDailyUsage, error)
	GetReclaudeDailyUsage(ctx context.Context, accountID int64, day string) (ReclaudeDailyUsage, error)
}

// ReclaudeQuotaGate 是日 token 硬闸。
//
// 🔴 这是**新建子系统**，不是复用既有配额：
//   - 调度端既有检查是 `IsAPIKeyOrBedrock() && IsQuotaExceeded()` ⇒ reclaude 不在其中，闸门永不触发
//   - 写入端只在 apikey/bedrock 时累加 ⇒ 水位永远是 0
//   - 语义上既有配额是**美元计**，与 token 计对不上
type ReclaudeQuotaGate struct {
	store ReclaudeDailyUsageStore
}

// NewReclaudeQuotaGate 构造闸门。store 可为 nil，此时一律判定为超闸
// （宁可不可调度，也不能因为计数器缺席就变成无限放行）。
func NewReclaudeQuotaGate(store ReclaudeDailyUsageStore) *ReclaudeQuotaGate {
	return &ReclaudeQuotaGate{store: store}
}

// IsDailyCapExceeded 判定该账号今日是否已超闸。
//
// 所有不确定路径一律判定为**超闸**：上限未标定、计数器缺席、计数器报错。
// 反方向的失败（放行）在这个模型里没有补救手段 —— 超卖不是运营弹性。
func (g *ReclaudeQuotaGate) IsDailyCapExceeded(ctx context.Context, account *Account) bool {
	if account == nil || !account.IsReclaude() {
		return false
	}

	cap := ReclaudeDailyTokenCap(account)
	if cap <= 0 {
		// 0 = 包络未标定。未标定就调度等于超卖。
		return true
	}
	if g == nil || g.store == nil {
		return true
	}

	usage, err := g.store.GetReclaudeDailyUsage(ctx, account.ID, reclaudeUsageDay(time.Now()))
	if err != nil {
		return true
	}
	return usage.EffectiveTokens() >= cap
}

// RecordUpstreamCall 记一次上游调用的水位。
//
// succeeded=false 的调用同样要记：它不产生 usage，却实打实消耗了对方配额。
func (g *ReclaudeQuotaGate) RecordUpstreamCall(
	ctx context.Context, account *Account, tokens int64, succeeded bool,
) error {
	if g == nil || g.store == nil || account == nil || !account.IsReclaude() {
		return nil
	}

	delta := ReclaudeDailyUsage{Tokens: tokens, UpstreamCalls: 1}
	if !succeeded {
		delta.Tokens = 0
		delta.FailedUpstreamCalls = 1
	}

	_, err := g.store.AddReclaudeDailyUsage(ctx, account.ID, reclaudeUsageDay(time.Now()), delta)
	return err
}

// ReclaudeCountTokensTokenEstimate 是一次 count_tokens 的保守 token 估算。
//
// count_tokens 发的是**完整 prompt**，对方按请求扣配额，但它不产生 usage、
// 不进计费链路 —— 按 0 计就是一条看不见的配额消耗（文档点名的第二个「配额暗漏」，
// 第一个是重试放大）。
//
// ⚠️ 与 ReclaudeFailedCallTokenEstimate 一样是占位值，待 Phase 2 长跑校准。
// 它的意义不在于准，而在于**不为零**。
const ReclaudeCountTokensTokenEstimate int64 = 1000

// recordReclaudeCountTokensCall 把一次成功的 count_tokens 记进日水位。
func (s *GatewayService) recordReclaudeCountTokensCall(ctx context.Context, account *Account) {
	if s == nil || s.reclaudeQuota == nil || account == nil || !account.IsReclaude() {
		return
	}

	if err := s.reclaudeQuota.RecordUpstreamCall(
		ctx, account, ReclaudeCountTokensTokenEstimate, true,
	); err != nil {
		logger.LegacyPrintf("service.reclaude",
			"failed to record reclaude count_tokens for account %d: %v", account.ID, err)
	}
}

// ReclaudeBillableTokens 把一次请求的 usage 折成配额口径的 token 数。
//
// ⚠️ 口径是**保守的**：cache_read 对 Anthropic 的计费远比 input 便宜，但对方
// 配额包怎么算我们看不到。宁可高估一点（少卖），也不能低估（超卖）。
// 真实系数要用 Phase 2 的长跑对账校准。
//
// CacheCreation5m / 1h 是 CacheCreationInputTokens 的**拆分**，不参与求和 ——
// 再加一次就是重复计数。
func ReclaudeBillableTokens(usage ClaudeUsage) int64 {
	return int64(usage.InputTokens) +
		int64(usage.OutputTokens) +
		int64(usage.CacheCreationInputTokens) +
		int64(usage.CacheReadInputTokens)
}

// recordReclaudeQuota 把一次**成功**请求的 token 记进日水位。
//
// 🔴 少了这一步，闸门读到的水位恒为 0 ⇒ 配额包被无限放行。
// 失败的调用不走这里（它们拿不到 usage），由 ReclaudeUpstream 按次记。
func (s *GatewayService) recordReclaudeQuota(ctx context.Context, account *Account, result *ForwardResult) {
	if s == nil || s.reclaudeQuota == nil || result == nil {
		return
	}
	if account == nil || !account.IsReclaude() {
		return
	}

	tokens := ReclaudeBillableTokens(result.Usage)
	if err := s.reclaudeQuota.RecordUpstreamCall(ctx, account, tokens, true); err != nil {
		logger.LegacyPrintf("service.reclaude",
			"failed to record reclaude usage for account %d: %v", account.ID, err)
	}
}

// ReclaudeDailyUsageSnapshot 是管理端要看的日水位快照。
type ReclaudeDailyUsageSnapshot struct {
	Usage           ReclaudeDailyUsage `json:"usage"`
	DailyCap        int64              `json:"daily_cap"`
	EffectiveTokens int64              `json:"effective_tokens"`
	WeeklySoftLimit int64              `json:"weekly_soft_limit"`
}

// TodayUsage 读今日水位。
//
// 🔴 计数器缺席时**报错**，不回零值：回 0 会被读成「今天还没用」，
// 而真相是「我们不知道」—— 而这条链路上「我们不知道」恰恰是最该被看见的状态
// （对方不提供任何用量接口，这个计数是唯一信源）。
func (g *ReclaudeQuotaGate) TodayUsage(ctx context.Context, account *Account) (ReclaudeDailyUsageSnapshot, error) {
	var snapshot ReclaudeDailyUsageSnapshot

	if account == nil || !account.IsReclaude() {
		return snapshot, errors.New("reclaude usage: reclaude account is required")
	}
	if g == nil || g.store == nil {
		return snapshot, errors.New("reclaude usage: daily usage store is not configured")
	}

	usage, err := g.store.GetReclaudeDailyUsage(ctx, account.ID, reclaudeUsageDay(time.Now()))
	if err != nil {
		return snapshot, fmt.Errorf("reclaude usage: %w", err)
	}

	cap := ReclaudeDailyTokenCap(account)
	return ReclaudeDailyUsageSnapshot{
		Usage:           usage,
		DailyCap:        cap,
		EffectiveTokens: usage.EffectiveTokens(),
		WeeklySoftLimit: ReclaudeWeeklySoftLimit(cap),
	}, nil
}

// ReclaudeDailyTokenCap 读取账号的日 token 上限；未配置时返回 0。
func ReclaudeDailyTokenCap(account *Account) int64 {
	if account == nil {
		return 0
	}
	return credentialInt64(account.Extra, ExtraKeyReclaudeDailyTokenCap)
}

// ReclaudeWeeklySoftLimit 由日闸推导周软闸。
//
// 软闸只告警不硬停：周维度的波动本来就大，硬停会把正常业务打断。
func ReclaudeWeeklySoftLimit(dailyCap int64) int64 {
	if dailyCap <= 0 {
		return 0
	}
	return int64(float64(dailyCap) * 7 * reclaudeWeeklySoftLimitFactor)
}

// reclaudeUsageDay 按 UTC 日切分水位。
func reclaudeUsageDay(now time.Time) string {
	return now.UTC().Format("2006-01-02")
}
