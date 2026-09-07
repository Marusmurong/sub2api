package service

import (
	"context"
	"log/slog"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// 按客户端平台分池（审计 C-1），方案见报告 C-1 节「落地方案」：
//
//   - 请求平台来自 ctx（handler 观察钩子写入；会话内以首次判定为准）；判不出的归默认池。
//   - 账号 extra.client_platform 是定型结果，空 = 未定型。定型只发生在这里：
//     某平台池里的号都忙、且该平台缺口 > 0 时，拿一个未定型的号定给它。
//   - 一个平台只有在 目标占比 × 在用总数 ≥ min_pool_target 时才开独立池，否则其请求
//     走默认池；默认池永远开放。
//   - 优先级：会话粘性 > 平台 > 负载。粘性账号永远保留在候选里，绝不因平台换号。
//   - 缺货：fallback 退到任意号并告警；strict 返回空候选 → 无可用账号。
//
// 只作用于 Anthropic OAuth / SetupToken 账号；其它账号原样透传。

// clientPlatformSharesTTL 是目标占比的缓存时长；占比来自观察计数，几分钟一刷足够。
const clientPlatformSharesTTL = 5 * time.Minute

// SetClientPlatformStatsCache 注入观察计数存储（handler 构造时传入，免改 wire）。
func (s *GatewayService) SetClientPlatformStatsCache(c ClientPlatformStatsCache) {
	if s == nil {
		return
	}
	s.clientPlatformStats = c
}

func (s *GatewayService) clientPlatformPoolConfig() config.ClientPlatformPoolConfig {
	if s == nil || s.cfg == nil {
		return config.ClientPlatformPoolConfig{}
	}
	return s.cfg.Gateway.ClientPlatformPool
}

// clientPlatformShares 返回各平台在最近 N 天真 CC 请求里的占比（只统计判出平台的）。
// 读不到数据时返回空 map：此时只有默认池开放。
func (s *GatewayService) clientPlatformShares(ctx context.Context) map[ClientPlatform]float64 {
	if s == nil {
		return map[ClientPlatform]float64{}
	}
	s.clientPlatformSharesMu.Lock()
	defer s.clientPlatformSharesMu.Unlock()
	now := time.Now()
	if s.clientPlatformSharesCached != nil && now.Before(s.clientPlatformSharesExpiry) {
		return s.clientPlatformSharesCached
	}
	if s.clientPlatformStats == nil {
		return map[ClientPlatform]float64{}
	}
	days := s.clientPlatformPoolConfig().EffectiveShareWindowDays()
	counts := map[ClientPlatform]int64{}
	var total int64
	for i := 0; i < days; i++ {
		day := now.UTC().AddDate(0, 0, -i).Format("2006-01-02")
		stats, err := s.clientPlatformStats.ReadClientPlatformStats(ctx, day)
		if err != nil {
			slog.Debug("client_platform_pool.read_stats_failed", "day", day, "error", err)
			continue
		}
		for field, n := range stats {
			p, ok := parseClientPlatformStatField(field)
			if !ok {
				continue
			}
			counts[p] += n
			total += n
		}
	}
	shares := make(map[ClientPlatform]float64, len(counts))
	if total > 0 {
		for p, n := range counts {
			shares[p] = float64(n) / float64(total)
		}
	}
	s.clientPlatformSharesCached = shares
	s.clientPlatformSharesExpiry = now.Add(clientPlatformSharesTTL)
	return shares
}

// parseClientPlatformStatField 只认「已判出平台 + 真 CC」的计数字段：{platform}|{source}|cc。
func parseClientPlatformStatField(field string) (ClientPlatform, bool) {
	parts := strings.Split(field, "|")
	if len(parts) != 3 || parts[2] != "cc" {
		return ClientPlatformUnknown, false
	}
	p := ParseClientPlatform(parts[0])
	if p == ClientPlatformUnknown {
		return ClientPlatformUnknown, false
	}
	return p, true
}

// clientPlatformPoolDecision 是纯函数 filterAccountsByClientPlatform 的输出。
type clientPlatformPoolDecision struct {
	Platform   ClientPlatform // 实际使用的池（可能已从请求平台回退到默认池）
	Candidates []Account      // 交给后续调度层的候选
	ToTypeID   int64          // 需要定型的未定型账号 ID（0 = 无）
	Reason     string         // 日志用
	Exhausted  bool           // 该平台池没号也没未定型可用（fallback 时已退到任意号；strict 时 Candidates 为空）
}

type clientPlatformPoolInput struct {
	Accounts        []Account
	StickyAccountID int64
	Requested       ClientPlatform
	Shares          map[ClientPlatform]float64
	Busy            map[int64]bool // 账号当前并发已满
	Cfg             config.ClientPlatformPoolConfig
	PickIndex       func(n int) int // 定型时从 n 个空闲未定型号里选哪个；nil = 随机
}

func (in clientPlatformPoolInput) pickIndex(n int) int {
	if in.PickIndex != nil {
		if i := in.PickIndex(n); i >= 0 && i < n {
			return i
		}
		return 0
	}
	return rand.IntN(n)
}

// filterAccountsByClientPlatform 是分池的核心判定，纯函数便于测试。
func filterAccountsByClientPlatform(in clientPlatformPoolInput) clientPlatformPoolDecision {
	defaultPlatform := ParseClientPlatform(in.Cfg.EffectiveDefaultPlatform())
	if defaultPlatform == ClientPlatformUnknown {
		defaultPlatform = ClientPlatformMacOSArm64
	}
	requested := in.Requested
	if requested == ClientPlatformUnknown {
		requested = defaultPlatform
	}

	// 只对 Anthropic OAuth 号分池；其它账号原样保留在候选里。
	var pooled, passthrough []Account
	for _, a := range in.Accounts {
		if a.IsAnthropicOAuthOrSetupToken() {
			pooled = append(pooled, a)
		} else {
			passthrough = append(passthrough, a)
		}
	}
	total := len(pooled)
	countByPlatform := map[ClientPlatform]int{}
	for _, a := range pooled {
		countByPlatform[a.ClientPlatform()]++
	}
	target := func(p ClientPlatform) int {
		return int(math.Round(in.Shares[p] * float64(total)))
	}
	poolOpen := func(p ClientPlatform) bool {
		return p == defaultPlatform || target(p) >= in.Cfg.EffectiveMinPoolTarget()
	}

	platform := requested
	reason := "pool"
	if !poolOpen(platform) {
		platform = defaultPlatform
		reason = "pool_closed_use_default"
	}

	// 已定型到该平台的号；粘性账号无论平台永远保留。
	candidates := make([]Account, 0, len(pooled))
	allBusy := true
	for _, a := range pooled {
		if a.ClientPlatform() == platform {
			candidates = append(candidates, a)
			if !in.Busy[a.ID] {
				allBusy = false
			}
		}
	}
	var toType int64
	if len(candidates) == 0 || allBusy {
		// 池里没号或都忙：按缺口定型一个未定型的号。默认池的缺口按「未定型都算它的」放宽，
		// 保证默认池永远能吸收未定型号。
		deficit := target(platform) - countByPlatform[platform]
		if platform == defaultPlatform && deficit <= 0 {
			deficit = 1
		}
		if deficit > 0 {
			// 从空闲的未定型号里随机挑一个：并发的两个不同平台请求若都在此刻定型，随机
			// 能把「挑到同一个号」的概率压到接近零。真撞上时 UpdateExtra 是后写覆盖，
			// 库里以最后一次为准、下次读到即自愈；定型日志（account_typed）能看出来。
			var idleUntyped []Account
			for _, a := range pooled {
				if a.ClientPlatform() == ClientPlatformUnknown && !in.Busy[a.ID] {
					idleUntyped = append(idleUntyped, a)
				}
			}
			if len(idleUntyped) > 0 {
				pick := idleUntyped[in.pickIndex(len(idleUntyped))]
				candidates = append(candidates, pick)
				toType = pick.ID
				reason += "+type_untyped"
			}
		}
	}
	// 粘性账号永远在候选里（粘性 > 平台）。
	if in.StickyAccountID > 0 {
		found := false
		for _, c := range candidates {
			if c.ID == in.StickyAccountID {
				found = true
				break
			}
		}
		if !found {
			for _, a := range pooled {
				if a.ID == in.StickyAccountID {
					candidates = append(candidates, a)
					break
				}
			}
		}
	}

	exhausted := false
	if len(candidates) == 0 {
		exhausted = true
		if in.Cfg.NormalizedMode() == config.ClientPlatformPoolModeStrict {
			reason += "+exhausted_strict"
			return clientPlatformPoolDecision{Platform: platform, Candidates: passthrough, Reason: reason, Exhausted: true}
		}
		reason += "+exhausted_fallback_any"
		candidates = pooled
	}
	return clientPlatformPoolDecision{
		Platform:   platform,
		Candidates: append(candidates, passthrough...),
		ToTypeID:   toType,
		Reason:     reason,
		Exhausted:  exhausted,
	}
}

// applyClientPlatformPool 在调度层加载完可调度账号之后、任何选号逻辑之前调用。
// 开关关闭时原样返回。
func (s *GatewayService) applyClientPlatformPool(ctx context.Context, accounts []Account, stickyAccountID int64) []Account {
	cfg := s.clientPlatformPoolConfig()
	if !cfg.Enabled || len(accounts) == 0 {
		return accounts
	}
	requested := ClientPlatformFromContext(ctx)

	busy := map[int64]bool{}
	if s.concurrencyService != nil {
		ids := make([]int64, 0, len(accounts))
		for _, a := range accounts {
			ids = append(ids, a.ID)
		}
		if current, err := s.concurrencyService.GetAccountConcurrencyBatch(ctx, ids); err == nil {
			for _, a := range accounts {
				if a.Concurrency > 0 && current[a.ID] >= a.Concurrency {
					busy[a.ID] = true
				}
			}
		}
	}

	decision := filterAccountsByClientPlatform(clientPlatformPoolInput{
		Accounts:        accounts,
		StickyAccountID: stickyAccountID,
		Requested:       requested,
		Shares:          s.clientPlatformShares(ctx),
		Busy:            busy,
		Cfg:             cfg,
	})

	if decision.ToTypeID > 0 {
		s.typeAccountClientPlatform(ctx, decision.Candidates, decision.ToTypeID, decision.Platform)
	}
	if decision.Exhausted {
		slog.Warn("client_platform_pool.exhausted",
			"requested", string(requested),
			"platform", string(decision.Platform),
			"mode", cfg.NormalizedMode(),
			"accounts", len(accounts))
	} else {
		slog.Debug("client_platform_pool.applied",
			"requested", string(requested),
			"platform", string(decision.Platform),
			"candidates", len(decision.Candidates),
			"typed", decision.ToTypeID,
			"reason", decision.Reason)
	}
	return decision.Candidates
}

// typeAccountClientPlatform 把未定型账号定给 platform：写库 + 更新本次候选里的副本。
// 定型是一次性的，之后不再改（一个账号 = 一台机器）。写库失败只记日志，本次仍按该
// 平台使用它——下一次会再试。
func (s *GatewayService) typeAccountClientPlatform(ctx context.Context, candidates []Account, accountID int64, platform ClientPlatform) {
	for i := range candidates {
		if candidates[i].ID != accountID {
			continue
		}
		if candidates[i].Extra == nil {
			candidates[i].Extra = map[string]any{}
		}
		candidates[i].Extra[accountClientPlatformKey] = string(platform)
		break
	}
	if s.accountRepo == nil {
		return
	}
	if err := s.accountRepo.UpdateExtra(ctx, accountID, map[string]any{accountClientPlatformKey: string(platform)}); err != nil {
		slog.Warn("client_platform_pool.type_account_failed", "account_id", accountID, "platform", string(platform), "error", err)
		return
	}
	slog.Info("client_platform_pool.account_typed", "account_id", accountID, "platform", string(platform))
}
