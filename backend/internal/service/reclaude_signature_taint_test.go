package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReclaudeSignatureTaintedAfterSwitch(t *testing.T) {
	now := time.Now()
	switchedAt := func(at time.Time) map[string]any {
		return map[string]any{ExtraKeyReclaudeLastSwitchedAt: at.UTC().Format(time.RFC3339)}
	}

	t.Run("刚换号时判定为已污染", func(t *testing.T) {
		// 🔴 换号时 account.ID 不变 ⇒ shouldPreStripThinking 认为「没换号」⇒ 不剥离，
		// 但历史 thinking 签名是旧账号签的，新账号必拒（实测约 595 次/天）。
		account := &Account{ID: 9, Type: AccountTypeReclaude, Extra: switchedAt(now.Add(-time.Minute))}

		require.True(t, ReclaudeSignatureTaintedAfterSwitch(account, now))
	})

	t.Run("窗口过后不再判定", func(t *testing.T) {
		// 窗口只决定「自动检测多久」；落在窗口外的会话退回既有的 400 重试路径学习。
		account := &Account{
			ID: 9, Type: AccountTypeReclaude,
			Extra: switchedAt(now.Add(-ReclaudeSignatureTaintWindow - time.Minute)),
		}

		require.False(t, ReclaudeSignatureTaintedAfterSwitch(account, now))
	})

	t.Run("从未换过号时为 false", func(t *testing.T) {
		require.False(t, ReclaudeSignatureTaintedAfterSwitch(
			&Account{ID: 9, Type: AccountTypeReclaude}, now))
	})

	t.Run("非 reclaude 账号恒为 false", func(t *testing.T) {
		// 反方向的回归：对全池账号开这个开关，等于把所有会话的 prompt 缓存打冷。
		account := &Account{ID: 9, Type: AccountTypeOAuth, Extra: switchedAt(now)}

		require.False(t, ReclaudeSignatureTaintedAfterSwitch(account, now))
		require.False(t, ReclaudeSignatureTaintedAfterSwitch(nil, now))
	})

	t.Run("时间戳坏掉时不判定", func(t *testing.T) {
		// 解析不出来就当没换过：宁可漏一次 400 重试，也不要让所有请求白白剥离。
		account := &Account{
			ID: 9, Type: AccountTypeReclaude,
			Extra: map[string]any{ExtraKeyReclaudeLastSwitchedAt: "not-a-timestamp"},
		}

		require.False(t, ReclaudeSignatureTaintedAfterSwitch(account, now))
	})
}
