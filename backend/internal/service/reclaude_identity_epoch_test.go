package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 换号后必须让旧的会话态失效，但 sub2api 里这些态的 key 都以 account.ID 为轴 ——
// 而 reclaude 换号时 **account.ID 不变**，那层保护整个失效：
//
//   - prev_request_id：会取到【旧上游账号】的 request id 注入 cc_prev_req，
//     那是一条可被上游证伪的假 parent-link，比缺字段更糟
//   - thinking 签名：历史签名是旧账号签的，新账号必拒
//
// 解法是给账号加一个「身份纪元」，换号即 +1，把会话态的命名空间整体翻页。
// 比「遍历删除」好：不需要 SCAN/DEL，旧键靠 TTL 自然过期，且翻页是原子的。
func TestReclaudeIdentityEpoch(t *testing.T) {
	t.Run("未换过号时为 0", func(t *testing.T) {
		account := &Account{ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude}

		require.Zero(t, ReclaudeIdentityEpoch(account))
	})

	t.Run("读取已记录的纪元", func(t *testing.T) {
		account := &Account{
			ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude,
			Extra: map[string]any{ExtraKeyReclaudeIdentityEpoch: float64(3)},
		}

		require.Equal(t, int64(3), ReclaudeIdentityEpoch(account))
	})

	t.Run("非 reclaude 账号恒为 0", func(t *testing.T) {
		account := &Account{
			ID: 9, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
			Extra: map[string]any{ExtraKeyReclaudeIdentityEpoch: float64(3)},
		}

		require.Zero(t, ReclaudeIdentityEpoch(account))
	})
}

func TestNamespaceSessionByIdentityEpoch(t *testing.T) {
	t.Run("reclaude 账号的会话标识带上纪元", func(t *testing.T) {
		account := &Account{
			ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude,
			Extra: map[string]any{ExtraKeyReclaudeIdentityEpoch: float64(2)},
		}

		require.Equal(t, "2:sess-abc", NamespaceSessionByIdentityEpoch(account, "sess-abc"))
	})

	// 这是本机制的核心断言：纪元一变，同一个会话就落到不同的命名空间，
	// 旧的 prev_request_id 与签名态再也取不到。
	t.Run("纪元变化后同一会话落到不同命名空间", func(t *testing.T) {
		before := &Account{
			ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude,
			Extra: map[string]any{ExtraKeyReclaudeIdentityEpoch: float64(1)},
		}
		after := &Account{
			ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude,
			Extra: map[string]any{ExtraKeyReclaudeIdentityEpoch: float64(2)},
		}

		require.NotEqual(t,
			NamespaceSessionByIdentityEpoch(before, "sess-abc"),
			NamespaceSessionByIdentityEpoch(after, "sess-abc"))
	})

	// 非 reclaude 账号的会话标识**一个字不能变** —— 那会让全池账号的
	// prev_request_id 与签名态在部署那一刻集体失效。
	t.Run("非 reclaude 账号原样返回", func(t *testing.T) {
		account := &Account{ID: 9, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

		require.Equal(t, "sess-abc", NamespaceSessionByIdentityEpoch(account, "sess-abc"))
	})

	t.Run("纪元为 0 时也带前缀以保持形态一致", func(t *testing.T) {
		account := &Account{ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude}

		require.Equal(t, "0:sess-abc", NamespaceSessionByIdentityEpoch(account, "sess-abc"))
	})

	t.Run("空会话标识原样返回", func(t *testing.T) {
		account := &Account{ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude}

		require.Empty(t, NamespaceSessionByIdentityEpoch(account, ""))
	})
}

// 换号推进纪元；其它事件不得推进。
func TestNextReclaudeIdentityEpoch(t *testing.T) {
	account := &Account{
		ID: 9, Platform: PlatformAnthropic, Type: AccountTypeReclaude,
		Extra: map[string]any{ExtraKeyReclaudeIdentityEpoch: float64(4)},
	}

	require.Equal(t, int64(5), NextReclaudeIdentityEpoch(account))
}
