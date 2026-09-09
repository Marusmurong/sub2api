package service

import (
	"log/slog"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// 新号爬坡（审计 2026-09-08 账号 216 复盘）。
//
// 选号链路是 filterByMinPriority → filterByMinLoadRate：优先级相同时，负载率最低的
// 账号赢。刚入池的号负载为 0，于是它必然赢到所有名额；老号打满时排队请求会在同一
// 秒集中落到它身上。216 的实测是进池 8 秒接下 12 台机器的 12 个独立会话。
//
// 这里不改选号算法，只把"新号在这一刻能承接多少"压下来：把 Concurrency 与
// max_sessions 从 initial_* 线性抬到账号自身的配置值。两者都是既有的限流维度，
// 下游（并发槽获取、负载率、会话准入、RPM sticky buffer）全部读同一份字段，
// 所以压这两个值即可覆盖整条链路，不必逐点改。
//
// 只作用于 Anthropic OAuth / SetupToken：它们才是订阅号，也才有 max_sessions 语义。

// accountRampupExemptExtraKey 让确定不需要爬坡的账号（例如成熟号只是换了 token、
// created_at 被重置）跳过本机制。
const accountRampupExemptExtraKey = "rampup_exempt"

// rampupFactor 返回 [0,1]：账号年龄占爬坡窗口的比例。0 = 刚建号，1 = 已出窗口。
func rampupFactor(createdAt, now time.Time, windowHours int) float64 {
	if windowHours <= 0 {
		return 1
	}
	window := time.Duration(windowHours) * time.Hour
	age := now.Sub(createdAt)
	if age <= 0 {
		return 0
	}
	if age >= window {
		return 1
	}
	return float64(age) / float64(window)
}

// rampupValue 把一个限流维度从 initial 线性抬到 full。
// full <= initial（账号本身就配得比起点还小）时原样返回 full，不做放大。
func rampupValue(initial, full int, factor float64) int {
	if full > 0 && full <= initial {
		return full
	}
	if full <= 0 {
		// full=0 在 max_sessions 语义里是"不限"。新号阶段仍按 initial 收口，
		// 出窗口后（factor=1）恢复不限。
		if factor >= 1 {
			return 0
		}
		return initial
	}
	v := initial + int(float64(full-initial)*factor+0.5)
	if v < initial {
		v = initial
	}
	if v > full {
		v = full
	}
	return v
}

func (s *GatewayService) newAccountRampupConfig() config.NewAccountRampupConfig {
	if s == nil || s.cfg == nil {
		return config.NewAccountRampupConfig{}
	}
	return s.cfg.Gateway.NewAccountRampup
}

// rampupAccount 返回按爬坡收口后的账号副本；不需要爬坡时返回 nil。
//
// 返回副本而不是就地改：候选列表里的 Account 会被复制到多处（快照缓存、选号结果、
// 会话注册），就地改 Extra 会把限流值写回共享的 map。
func rampupAccount(a Account, cfg config.NewAccountRampupConfig, now time.Time) *Account {
	if !cfg.Enabled {
		return nil
	}
	if !a.IsAnthropicOAuthOrSetupToken() {
		return nil
	}
	if a.extraBool(accountRampupExemptExtraKey) {
		return nil
	}
	if a.CreatedAt.IsZero() {
		// 调度投影不带 created_at 时无从判断年龄，宁可不限，也不要把老号误压成 1 并发。
		return nil
	}
	factor := rampupFactor(a.CreatedAt, now, cfg.EffectiveWindowHours())
	if factor >= 1 {
		return nil
	}

	conc := rampupValue(cfg.EffectiveInitialConcurrency(), a.Concurrency, factor)
	sess := rampupValue(cfg.EffectiveInitialMaxSessions(), a.GetMaxSessions(), factor)
	if conc == a.Concurrency && sess == a.GetMaxSessions() {
		return nil
	}

	ramped := a
	ramped.Concurrency = conc
	extra := make(map[string]any, len(a.Extra)+1)
	for k, v := range a.Extra {
		extra[k] = v
	}
	extra["max_sessions"] = sess
	ramped.Extra = extra
	return &ramped
}

// applyNewAccountRampup 对候选列表逐个套爬坡。无账号需要收口时原样返回入参。
func (s *GatewayService) applyNewAccountRampup(accounts []Account) []Account {
	cfg := s.newAccountRampupConfig()
	if !cfg.Enabled || len(accounts) == 0 {
		return accounts
	}
	now := time.Now()
	var out []Account
	for i := range accounts {
		ramped := rampupAccount(accounts[i], cfg, now)
		if ramped == nil {
			if out != nil {
				out = append(out, accounts[i])
			}
			continue
		}
		if out == nil {
			out = make([]Account, 0, len(accounts))
			out = append(out, accounts[:i]...)
		}
		logAccountRampup(ramped, accounts[i])
		out = append(out, *ramped)
	}
	if out == nil {
		return accounts
	}
	return out
}

// applyNewAccountRampupOne 是单账号版本（粘性选号与选中后 hydrate 走这条）。
func (s *GatewayService) applyNewAccountRampupOne(account *Account) *Account {
	if account == nil {
		return account
	}
	cfg := s.newAccountRampupConfig()
	if !cfg.Enabled {
		return account
	}
	ramped := rampupAccount(*account, cfg, time.Now())
	if ramped == nil {
		return account
	}
	logAccountRampup(ramped, *account)
	return ramped
}

func logAccountRampup(ramped *Account, original Account) {
	slog.Debug("account_rampup.applied",
		"account_id", ramped.ID,
		"age_hours", time.Since(original.CreatedAt).Hours(),
		"concurrency", ramped.Concurrency,
		"concurrency_full", original.Concurrency,
		"max_sessions", ramped.GetMaxSessions(),
		"max_sessions_full", original.GetMaxSessions())
}
