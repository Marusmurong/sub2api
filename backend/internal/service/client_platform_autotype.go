package service

import (
	"context"
	"log/slog"
	"math"
	"time"
)

// 建号时按配额自动定型（把"智能分配"从分池路由里解耦出来）。
//
// 背景：定型原本只发生在 filterAccountsByClientPlatform 里，而那段代码挂在
// gateway.client_platform_pool.enabled 下面。于是"决定号自称什么系统"和"决定谁
// 接谁的流量"被绑在同一个开关上，可这两件事的风险完全不同：
//
//   - 定型：零风险，不影响路由，只决定出站 X-Stainless-OS/Arch
//   - 分池路由：号少时会把某个平台的绝大部分流量压到一两个新号上（2026-09-08
//     账号 216 就是这么被打爆的），必须等每个池 ≥2 个健康号才敢开
//
// 结果是：想要身份对齐，就被迫连路由一起打开。这里把定型拆出来，让它在建号那一刻
// 独立完成，与路由开关无关。
//
// 为什么放在建号而不是选号：选号时定型要走 UpdateExtra 写库，而 hydrateSelectedAccount
// 随后会从调度快照重新读一遍账号——快照还没刷新时，这个号**第一条**上游请求仍然发
// 默认的 MacOS/arm64 头。对新号来说第一条请求恰恰是最要紧的那条。建号时定型则保证
// 它这辈子发出的每一条请求都带正确的平台。

// PickClientPlatformForNewAccount 为即将创建的账号挑一个平台。
//
// 规则与分池里的配额一致：target(p) = round(占比 × 总号数)，总号数**含这个新号**，
// 取缺口最大的平台。缺口相同时占比大的优先，保证结果稳定可测。
//
// 没有占比数据（Redis 观察计数为空 / 读失败）时返回 fallback：观察期还没攒够数据
// 就建号是很正常的，此时宁可给一个确定值，也不要让号留在未定型状态——未定型的号
// 会退回全池默认身份，那正是我们想消除的形态。
func PickClientPlatformForNewAccount(existing []Account, shares map[ClientPlatform]float64, fallback ClientPlatform) (ClientPlatform, string) {
	counts := map[ClientPlatform]int{}
	pooled := 0
	for i := range existing {
		if !existing[i].IsAnthropicOAuthOrSetupToken() {
			continue
		}
		pooled++
		counts[existing[i].ClientPlatform()]++
	}
	total := pooled + 1 // 把待建的这个号算进去，否则第一个号永远拿不到配额

	best := ClientPlatformUnknown
	bestDeficit := 0
	bestShare := 0.0
	for p, share := range shares {
		if p == ClientPlatformUnknown || share <= 0 {
			continue
		}
		deficit := int(math.Round(share*float64(total))) - counts[p]
		if deficit <= 0 {
			continue
		}
		// 三级排序，最后一级按平台名字典序：shares 是 map，遍历顺序随机，
		// 缺口与占比都相同时若不定序，同样的输入会给出不同的结果，既不可测也会
		// 让连续建的两个号随机分到不同平台。
		better := deficit > bestDeficit ||
			(deficit == bestDeficit && share > bestShare) ||
			(deficit == bestDeficit && share == bestShare && (best == ClientPlatformUnknown || p < best))
		if better {
			best, bestDeficit, bestShare = p, deficit, share
		}
	}
	if best != ClientPlatformUnknown {
		return best, "quota"
	}
	// 有占比但所有平台都已达标（round 之后配额刚好填满）：给占比最大的平台。
	// 这里回退到 default_platform 是错的——默认平台可能恰恰是占比最小的那个，
	// 那样每次"无缺口"都会往少数派上加号，越加越偏。
	var top ClientPlatform
	topShare := 0.0
	for p, share := range shares {
		if p == ClientPlatformUnknown || share <= 0 {
			continue
		}
		if share > topShare || (share == topShare && (top == ClientPlatformUnknown || p < top)) {
			top, topShare = p, share
		}
	}
	if top != ClientPlatformUnknown {
		return top, "no_deficit_top_share"
	}
	return fallback, "fallback_no_shares"
}

// autoTypeNewAnthropicAccount 在建号时把 extra.client_platform 填上。
//
// 三种情况原样返回，不做任何事：非 Anthropic OAuth/SetupToken 账号；开关关闭；
// extra 里已经显式写了 client_platform（管理员或 preset 指定的一律优先）。
func (s *adminServiceImpl) autoTypeNewAnthropicAccount(ctx context.Context, platform, accountType string, extra map[string]any) map[string]any {
	if s == nil || s.cfg == nil {
		return extra
	}
	cfg := s.cfg.Gateway.ClientPlatformPool
	if !cfg.AutoTypeEnabled() {
		return extra
	}
	if platform != PlatformAnthropic || (accountType != AccountTypeOAuth && accountType != AccountTypeSetupToken) {
		return extra
	}
	if extra != nil {
		if v, ok := extra[AccountClientPlatformExtraKey].(string); ok && ParseClientPlatform(v) != ClientPlatformUnknown {
			return extra
		}
	}

	fallback := ParseClientPlatform(cfg.EffectiveDefaultPlatform())
	if fallback == ClientPlatformUnknown {
		fallback = ClientPlatformMacOSArm64
	}

	var existing []Account
	if s.accountRepo != nil {
		if list, err := s.accountRepo.ListByPlatform(ctx, PlatformAnthropic); err == nil {
			existing = list
		} else {
			// 读不到存量就只能按 fallback 定型：留成未定型比定错更糟。
			slog.Warn("client_platform.autotype_list_failed", "error", err)
		}
	}
	shares := ReadClientPlatformShares(ctx, s.clientPlatformStats, cfg.EffectiveShareWindowDays(), time.Now())

	picked, reason := PickClientPlatformForNewAccount(existing, shares, fallback)

	out := make(map[string]any, len(extra)+1)
	for k, v := range extra {
		out[k] = v
	}
	out[AccountClientPlatformExtraKey] = string(picked)
	slog.Info("client_platform.autotype",
		"platform", string(picked),
		"reason", reason,
		"existing_anthropic", len(existing),
		"shares_known", len(shares) > 0)
	return out
}
