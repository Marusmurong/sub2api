package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpdateSessionWindow_SkipsReclaude(t *testing.T) {
	// 🔴 anthropic-ratelimit-unified-* 反映的是**底层那个 Claude 账号**的窗口，
	// 不是我们配额包的。写进来就会把一个陌生账号的 5h 窗口显示在管理端「5h 窗口」栏，
	// 直接违反「上游拿不到的东西不许显示」这条红线。
	resetUnix := time.Now().Add(3 * time.Hour).Unix()
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", resetUnix))

	t.Run("reclaude 账号不写窗口", func(t *testing.T) {
		repo := &sessionWindowMockRepo{}
		svc := newRateLimitServiceForTest(repo)

		svc.UpdateSessionWindow(context.Background(),
			&Account{ID: 42, Platform: PlatformAnthropic, Type: AccountTypeReclaude}, headers)

		require.Empty(t, repo.sessionWindowCalls)
	})

	t.Run("自建号照常写窗口", func(t *testing.T) {
		// 反方向的回归：这条门控一旦写宽，全池账号的 5h 窗口会一起失效。
		repo := &sessionWindowMockRepo{}
		svc := newRateLimitServiceForTest(repo)

		svc.UpdateSessionWindow(context.Background(),
			&Account{ID: 42, Platform: PlatformAnthropic, Type: AccountTypeOAuth}, headers)

		require.Len(t, repo.sessionWindowCalls, 1)
	})
}

func TestCapReclaudeCooldown(t *testing.T) {
	now := time.Now()

	t.Run("超过上限时封顶", func(t *testing.T) {
		got := CapReclaudeCooldown(
			&Account{Type: AccountTypeReclaude}, now.Add(6*time.Hour), now)

		require.Equal(t, now.Add(ReclaudeMaxCooldown), got)
	})

	t.Run("未超上限时原样返回", func(t *testing.T) {
		want := now.Add(3 * time.Minute)

		require.Equal(t, want,
			CapReclaudeCooldown(&Account{Type: AccountTypeReclaude}, want, now))
	})

	t.Run("非 reclaude 账号一律原样返回", func(t *testing.T) {
		// 🔴 给全池账号的冷却加上限，等于让真正被限流的账号提前恢复调度，
		// 换来的是下一轮立刻再吃一发 429。
		want := now.Add(6 * time.Hour)

		require.Equal(t, want, CapReclaudeCooldown(&Account{Type: AccountTypeOAuth}, want, now))
		require.Equal(t, want, CapReclaudeCooldown(nil, want, now))
	})
}

func TestHandle429_CapsReclaudeCooldown(t *testing.T) {
	// 上游给的 reset 在 6 小时后；reclaude 账号必须被压到 30 分钟以内。
	repo := &rateLimitCaptureRepo{}
	svc := newRateLimitServiceForTest(repo)

	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "rejected")
	headers.Set("anthropic-ratelimit-unified-5h-reset",
		fmt.Sprintf("%d", time.Now().Add(6*time.Hour).Unix()))

	svc.handle429(context.Background(),
		&Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeReclaude}, headers, nil)

	require.NotEmpty(t, repo.rateLimitedUntil)
	require.WithinDuration(t,
		time.Now().Add(ReclaudeMaxCooldown), repo.rateLimitedUntil[0], time.Minute)
}

// rateLimitCaptureRepo 在 sessionWindowMockRepo 之上记录 SetRateLimited。
type rateLimitCaptureRepo struct {
	sessionWindowMockRepo
	rateLimitedUntil []time.Time
}

func (r *rateLimitCaptureRepo) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	r.rateLimitedUntil = append(r.rateLimitedUntil, resetAt)
	return nil
}
