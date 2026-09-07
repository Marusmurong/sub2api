//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func oauthAcct(id int64, platform string) Account {
	extra := map[string]any{}
	if platform != "" {
		extra[accountClientPlatformKey] = platform
	}
	return Account{ID: id, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Concurrency: 6, Extra: extra}
}

func ids(accs []Account) []int64 {
	out := make([]int64, 0, len(accs))
	for _, a := range accs {
		out = append(out, a.ID)
	}
	return out
}

func poolCfg(mode string) config.ClientPlatformPoolConfig {
	return config.ClientPlatformPoolConfig{Enabled: true, Mode: mode}
}

// 目标占比 70/25/5，在用 10 个号 → windows 目标 7、macos 3、linux 0（池不开）。
var shares70_25_5 = map[ClientPlatform]float64{ClientPlatformWindowsX64: 0.7, ClientPlatformMacOSArm64: 0.25, ClientPlatformLinuxX64: 0.05}

func TestFilterAccountsByClientPlatform_TypedPoolServesItsPlatform(t *testing.T) {
	accs := []Account{oauthAcct(1, "macos-arm64"), oauthAcct(2, "windows-x64"), oauthAcct(3, "windows-x64"), oauthAcct(4, "")}
	d := filterAccountsByClientPlatform(clientPlatformPoolInput{Accounts: accs, Requested: ClientPlatformWindowsX64, Shares: shares70_25_5, Cfg: poolCfg("fallback")})
	require.Equal(t, ClientPlatformWindowsX64, d.Platform)
	require.ElementsMatch(t, []int64{2, 3}, ids(d.Candidates), "只给 windows 池的号，未定型的不碰")
	require.Zero(t, d.ToTypeID)
	require.False(t, d.Exhausted)
}

func TestFilterAccountsByClientPlatform_UnknownRequestGoesToDefaultPool(t *testing.T) {
	accs := []Account{oauthAcct(1, "macos-arm64"), oauthAcct(2, "windows-x64")}
	d := filterAccountsByClientPlatform(clientPlatformPoolInput{Accounts: accs, Requested: ClientPlatformUnknown, Shares: shares70_25_5, Cfg: poolCfg("fallback")})
	require.Equal(t, ClientPlatformMacOSArm64, d.Platform)
	require.Equal(t, []int64{1}, ids(d.Candidates))
}

func TestFilterAccountsByClientPlatform_ClosedPoolFallsBackToDefault(t *testing.T) {
	// 4 个号 × 5% linux = 0 < 2 → linux 池不开，linux 请求走默认池，不定型。
	accs := []Account{oauthAcct(1, "macos-arm64"), oauthAcct(2, "windows-x64"), oauthAcct(3, ""), oauthAcct(4, "")}
	d := filterAccountsByClientPlatform(clientPlatformPoolInput{Accounts: accs, Requested: ClientPlatformLinuxX64, Shares: shares70_25_5, Cfg: poolCfg("fallback")})
	require.Equal(t, ClientPlatformMacOSArm64, d.Platform)
	require.Contains(t, d.Reason, "pool_closed_use_default")
	require.Equal(t, []int64{1}, ids(d.Candidates))
	require.Zero(t, d.ToTypeID, "池没开就不该消耗未定型的号")
}

func TestFilterAccountsByClientPlatform_TypesUntypedWhenPoolBusyAndDeficit(t *testing.T) {
	// windows 目标 = 0.7 × 4 ≈ 3，现有 1 且忙 → 缺口 2 → 定型一个未定型的号。
	accs := []Account{oauthAcct(1, "macos-arm64"), oauthAcct(2, "windows-x64"), oauthAcct(3, ""), oauthAcct(4, "")}
	d := filterAccountsByClientPlatform(clientPlatformPoolInput{Accounts: accs, Requested: ClientPlatformWindowsX64, Shares: shares70_25_5, Busy: map[int64]bool{2: true}, Cfg: poolCfg("fallback"), PickIndex: func(int) int { return 0 }})
	require.Equal(t, int64(3), d.ToTypeID, "空闲的未定型号被定型（测试固定选第一个）")
	require.ElementsMatch(t, []int64{2, 3}, ids(d.Candidates))
	require.Contains(t, d.Reason, "type_untyped")
}

func TestFilterAccountsByClientPlatform_NoTypingWhenPoolHasIdleAccount(t *testing.T) {
	accs := []Account{oauthAcct(1, "macos-arm64"), oauthAcct(2, "windows-x64"), oauthAcct(3, "")}
	d := filterAccountsByClientPlatform(clientPlatformPoolInput{Accounts: accs, Requested: ClientPlatformWindowsX64, Shares: shares70_25_5, Cfg: poolCfg("fallback")})
	require.Zero(t, d.ToTypeID, "池里有空闲号就不动缓冲")
	require.Equal(t, []int64{2}, ids(d.Candidates))
}

func TestFilterAccountsByClientPlatform_NoTypingBeyondTarget(t *testing.T) {
	// windows 目标 = 0.7 × 3 ≈ 2，已有 2 个（都忙）→ 缺口 0 → 不定型，回退到任意号。
	accs := []Account{oauthAcct(1, "windows-x64"), oauthAcct(2, "windows-x64"), oauthAcct(3, "")}
	d := filterAccountsByClientPlatform(clientPlatformPoolInput{Accounts: accs, Requested: ClientPlatformWindowsX64, Shares: shares70_25_5, Busy: map[int64]bool{1: true, 2: true}, Cfg: poolCfg("fallback")})
	require.Zero(t, d.ToTypeID)
	require.ElementsMatch(t, []int64{1, 2}, ids(d.Candidates), "都忙但池里有号：候选仍是它们，等待/排队由后续层处理")
	require.False(t, d.Exhausted)
}

func TestFilterAccountsByClientPlatform_DefaultPoolAlwaysAbsorbsUntyped(t *testing.T) {
	// 默认池没号、目标也为 0（无观察数据）→ 仍可定型一个未定型号给默认池。
	accs := []Account{oauthAcct(1, ""), oauthAcct(2, "")}
	d := filterAccountsByClientPlatform(clientPlatformPoolInput{Accounts: accs, Requested: ClientPlatformUnknown, Shares: map[ClientPlatform]float64{}, Cfg: poolCfg("fallback"), PickIndex: func(int) int { return 0 }})
	require.Equal(t, ClientPlatformMacOSArm64, d.Platform)
	require.Equal(t, int64(1), d.ToTypeID)
	require.Equal(t, []int64{1}, ids(d.Candidates))
}

func TestFilterAccountsByClientPlatform_StickyAlwaysKept(t *testing.T) {
	// 4 个号 → windows 目标 3，池开；粘性账号 1 是 macos。
	accs := []Account{oauthAcct(1, "macos-arm64"), oauthAcct(2, "windows-x64"), oauthAcct(3, "windows-x64"), oauthAcct(4, "windows-x64")}
	d := filterAccountsByClientPlatform(clientPlatformPoolInput{Accounts: accs, StickyAccountID: 1, Requested: ClientPlatformWindowsX64, Shares: shares70_25_5, Cfg: poolCfg("fallback")})
	require.Equal(t, ClientPlatformWindowsX64, d.Platform)
	require.ElementsMatch(t, []int64{2, 3, 4, 1}, ids(d.Candidates), "粘性账号平台不同也保留：粘性 > 平台")
}

func TestFilterAccountsByClientPlatform_ExhaustedFallbackVsStrict(t *testing.T) {
	// windows 池开（目标 3）但没有 windows 号、也没有未定型号。
	accs := []Account{oauthAcct(1, "macos-arm64"), oauthAcct(2, "macos-arm64"), oauthAcct(3, "macos-arm64"), oauthAcct(4, "macos-arm64")}
	fb := filterAccountsByClientPlatform(clientPlatformPoolInput{Accounts: accs, Requested: ClientPlatformWindowsX64, Shares: shares70_25_5, Cfg: poolCfg("fallback")})
	require.True(t, fb.Exhausted)
	require.Len(t, fb.Candidates, 4, "fallback：退到任意号")

	st := filterAccountsByClientPlatform(clientPlatformPoolInput{Accounts: accs, Requested: ClientPlatformWindowsX64, Shares: shares70_25_5, Cfg: poolCfg("strict")})
	require.True(t, st.Exhausted)
	require.Empty(t, st.Candidates, "strict：空候选 → 无可用账号")
}

func TestFilterAccountsByClientPlatform_NonOAuthPassthrough(t *testing.T) {
	apiKey := Account{ID: 9, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	// 4 个 OAuth 号全是 macos，windows 池开但没号且无未定型 → strict 下 OAuth 候选为空，
	// 但非 OAuth 账号不参与分池、原样保留。
	accs := []Account{oauthAcct(1, "macos-arm64"), oauthAcct(2, "macos-arm64"), oauthAcct(3, "macos-arm64"), oauthAcct(4, "macos-arm64"), apiKey}
	d := filterAccountsByClientPlatform(clientPlatformPoolInput{Accounts: accs, Requested: ClientPlatformWindowsX64, Shares: shares70_25_5, Cfg: poolCfg("strict")})
	require.True(t, d.Exhausted)
	require.Equal(t, []int64{9}, ids(d.Candidates), "非 OAuth 账号不参与分池，原样保留")
}

func TestApplyClientPlatformPool_DisabledIsNoop(t *testing.T) {
	s := &GatewayService{cfg: &config.Config{}}
	accs := []Account{oauthAcct(1, "macos-arm64"), oauthAcct(2, "windows-x64")}
	out := s.applyClientPlatformPool(WithClientPlatform(context.Background(), ClientPlatformWindowsX64), accs, 0)
	require.Equal(t, ids(accs), ids(out))
}

func TestApplyClientPlatformPool_TypesAndUsesUntyped(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.ClientPlatformPool = config.ClientPlatformPoolConfig{Enabled: true}
	s := &GatewayService{cfg: cfg}
	s.clientPlatformSharesCached = shares70_25_5
	s.clientPlatformSharesExpiry = time.Now().Add(time.Hour)
	accs := []Account{oauthAcct(1, "macos-arm64"), oauthAcct(2, ""), oauthAcct(3, "")}
	out := s.applyClientPlatformPool(WithClientPlatform(context.Background(), ClientPlatformWindowsX64), accs, 0)
	require.Len(t, out, 1)
	require.Contains(t, []int64{2, 3}, out[0].ID, "从空闲未定型号里随机挑一个定型")
	require.Equal(t, "windows-x64", out[0].Extra[accountClientPlatformKey], "本次候选里的副本已定型（accountRepo 为 nil 时不写库）")
}

func TestClientPlatformContextRoundTrip(t *testing.T) {
	ctx := WithClientPlatform(context.Background(), ClientPlatformLinuxX64)
	require.Equal(t, ClientPlatformLinuxX64, ClientPlatformFromContext(ctx))
	require.Equal(t, ClientPlatformUnknown, ClientPlatformFromContext(context.Background()))
	require.Equal(t, ClientPlatformUnknown, ClientPlatformFromContext(WithClientPlatform(context.Background(), ClientPlatformUnknown)))
}

func TestParseClientPlatformStatField(t *testing.T) {
	p, ok := parseClientPlatformStatField("windows-x64|env_block|cc")
	require.True(t, ok)
	require.Equal(t, ClientPlatformWindowsX64, p)
	_, ok = parseClientPlatformStatField("linux-x64|header|other")
	require.False(t, ok, "非 CC 不计入占比")
	_, ok = parseClientPlatformStatField("unknown|none|cc")
	require.False(t, ok)
	_, ok = parseClientPlatformStatField("flip|a->b")
	require.False(t, ok)
}
