//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func rampupCfg() config.NewAccountRampupConfig {
	return config.NewAccountRampupConfig{
		Enabled: true, WindowHours: 48, InitialConcurrency: 1, InitialMaxSessions: 3,
	}
}

func rampupAccountAged(age time.Duration) Account {
	return Account{
		ID: 216, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Concurrency: 5,
		CreatedAt:   time.Now().Add(-age),
		Extra:       map[string]any{"max_sessions": 15},
	}
}

func TestRampup_NewAccountStartsSmallAndOpensUp(t *testing.T) {
	now := time.Now()
	cases := []struct {
		age                time.Duration
		wantConc, wantSess int
	}{
		{0, 1, 3},
		{12 * time.Hour, 2, 6},
		{24 * time.Hour, 3, 9},
		{36 * time.Hour, 4, 12},
	}
	for _, tc := range cases {
		a := rampupAccountAged(tc.age)
		ramped := rampupAccount(a, rampupCfg(), now)
		require.NotNil(t, ramped, "age=%s 仍在窗口内，应当收口", tc.age)
		require.Equal(t, tc.wantConc, ramped.Concurrency, "age=%s", tc.age)
		require.Equal(t, tc.wantSess, ramped.GetMaxSessions(), "age=%s", tc.age)
	}

	// 窗口末尾已经抬到账号自身配置值，不再需要收口
	require.Nil(t, rampupAccount(rampupAccountAged(47*time.Hour), rampupCfg(), now))
}

func TestRampup_OutOfWindowUntouched(t *testing.T) {
	require.Nil(t, rampupAccount(rampupAccountAged(72*time.Hour), rampupCfg(), time.Now()))
}

// 就地改会把限流值写回共享的 Extra map，候选列表被复制到多处后会互相污染。
func TestRampup_DoesNotMutateOriginal(t *testing.T) {
	a := rampupAccountAged(time.Hour)
	ramped := rampupAccount(a, rampupCfg(), time.Now())
	require.NotNil(t, ramped)
	require.Equal(t, 5, a.Concurrency)
	require.Equal(t, 15, a.GetMaxSessions())
	require.Equal(t, 1, ramped.Concurrency)
}

func TestRampup_SkipConditions(t *testing.T) {
	now := time.Now()

	off := rampupCfg()
	off.Enabled = false
	require.Nil(t, rampupAccount(rampupAccountAged(0), off, now), "开关关闭不生效")

	grok := rampupAccountAged(0)
	grok.Platform = PlatformGrok
	require.Nil(t, rampupAccount(grok, rampupCfg(), now), "只作用于 Anthropic 订阅号")

	exempt := rampupAccountAged(0)
	exempt.Extra["rampup_exempt"] = true
	require.Nil(t, rampupAccount(exempt, rampupCfg(), now), "豁免标记应跳过")

	noCreated := rampupAccountAged(0)
	noCreated.CreatedAt = time.Time{}
	require.Nil(t, rampupAccount(noCreated, rampupCfg(), now), "拿不到 created_at 时不得误压老号")
}

// 账号本身就配得比爬坡起点还小时，不能被"抬高"。
func TestRampup_NeverRaisesAboveAccountConfig(t *testing.T) {
	a := rampupAccountAged(0)
	a.Concurrency = 1
	a.Extra["max_sessions"] = 2
	require.Nil(t, rampupAccount(a, rampupCfg(), time.Now()))
}

// max_sessions 未配置（0 = 不限）的新号仍要在窗口内收口。
func TestRampup_UnlimitedSessionsStillCappedWhileYoung(t *testing.T) {
	a := rampupAccountAged(time.Hour)
	delete(a.Extra, "max_sessions")
	ramped := rampupAccount(a, rampupCfg(), time.Now())
	require.NotNil(t, ramped)
	require.Equal(t, 3, ramped.GetMaxSessions())
}
