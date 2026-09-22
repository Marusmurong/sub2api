package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

var errFakeNotCipher = errors.New("fake encryptor: not a cipher text")

// fakeEncryptor 用可逆的假变换代替 AES，让测试只验证「加/解密路径是否走到位」，
// 不重复验证 AES-256-GCM 本身。
type fakeEncryptor struct {
	encryptErr error
	decryptErr error
}

func (f *fakeEncryptor) Encrypt(plaintext string) (string, error) {
	if f.encryptErr != nil {
		return "", f.encryptErr
	}
	return "CIPHER(" + plaintext + ")", nil
}

func (f *fakeEncryptor) Decrypt(ciphertext string) (string, error) {
	if f.decryptErr != nil {
		return "", f.decryptErr
	}
	if !strings.HasPrefix(ciphertext, "CIPHER(") || !strings.HasSuffix(ciphertext, ")") {
		return "", errFakeNotCipher
	}
	return strings.TrimSuffix(strings.TrimPrefix(ciphertext, "CIPHER("), ")"), nil
}

// V-8：密钥未显式配置时每次启动随机生成，加密落库的 sk/seed 在下次重启后
// 永久不可解密 = 一份订阅报废，且禁止二次 login ⇒ 无法补救。
// 所以这道闸必须在**第一条真凭据入库之前**就存在。
func TestRequireCredentialEncryptionKey(t *testing.T) {
	t.Run("密钥已显式配置时放行", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Totp.EncryptionKeyConfigured = true

		require.NoError(t, RequireCredentialEncryptionKey(cfg))
	})

	t.Run("密钥未配置时拒绝，且错误信息要说清后果", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Totp.EncryptionKeyConfigured = false

		err := RequireCredentialEncryptionKey(cfg)

		require.Error(t, err)
		require.ErrorIs(t, err, ErrCredentialEncryptionKeyNotConfigured)
		require.Contains(t, err.Error(), "restart", "管理端要能显示明确原因")
	})

	t.Run("配置为空时拒绝而不是放行", func(t *testing.T) {
		require.Error(t, RequireCredentialEncryptionKey(nil))
	})
}

func TestReclaudeCredentialCipher_EncryptForStorage(t *testing.T) {
	newCredentials := func() map[string]any {
		return map[string]any{
			CredKeyReclaudeSK:       "sk-rec-abc123",
			CredKeyReclaudeSeed:     "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
			CredKeyReclaudeDeviceID: int64(43448),
			CredKeyReclaudeGateway:  "https://la.route.reclaude.ai",
		}
	}

	t.Run("只加密两个秘密字段，其余原样保留", func(t *testing.T) {
		// Arrange
		cipher := NewReclaudeCredentialCipher(&fakeEncryptor{})
		in := newCredentials()

		// Act
		out, err := cipher.EncryptForStorage(in)

		// Assert
		require.NoError(t, err)
		require.Equal(t, encryptedCredentialPrefix+"CIPHER(sk-rec-abc123)", out[CredKeyReclaudeSK])
		require.True(t, strings.HasPrefix(out[CredKeyReclaudeSeed].(string), encryptedCredentialPrefix))
		require.Equal(t, int64(43448), out[CredKeyReclaudeDeviceID])
		require.Equal(t, "https://la.route.reclaude.ai", out[CredKeyReclaudeGateway])
	})

	// 不可变：绝不就地修改调用方的 map。
	t.Run("不修改入参", func(t *testing.T) {
		cipher := NewReclaudeCredentialCipher(&fakeEncryptor{})
		in := newCredentials()

		_, err := cipher.EncryptForStorage(in)

		require.NoError(t, err)
		require.Equal(t, "sk-rec-abc123", in[CredKeyReclaudeSK], "入参被就地修改了")
	})

	// 账号编辑是全对象 PUT，同一份 credentials 可能被反复写回；
	// 二次加密会让密文永远解不出原文。
	t.Run("已加密的值不会被二次加密", func(t *testing.T) {
		cipher := NewReclaudeCredentialCipher(&fakeEncryptor{})
		once, err := cipher.EncryptForStorage(newCredentials())
		require.NoError(t, err)

		twice, err := cipher.EncryptForStorage(once)

		require.NoError(t, err)
		require.Equal(t, once[CredKeyReclaudeSK], twice[CredKeyReclaudeSK])
	})

	t.Run("加密器缺失时报错而不是明文落库", func(t *testing.T) {
		cipher := NewReclaudeCredentialCipher(nil)

		_, err := cipher.EncryptForStorage(newCredentials())

		require.Error(t, err)
	})

	t.Run("加密失败时整体报错，不落半份", func(t *testing.T) {
		cipher := NewReclaudeCredentialCipher(&fakeEncryptor{encryptErr: errFakeNotCipher})

		_, err := cipher.EncryptForStorage(newCredentials())

		require.Error(t, err)
	})

	t.Run("秘密字段为空串时跳过而不是加密空值", func(t *testing.T) {
		cipher := NewReclaudeCredentialCipher(&fakeEncryptor{})

		out, err := cipher.EncryptForStorage(map[string]any{CredKeyReclaudeSK: ""})

		require.NoError(t, err)
		require.Equal(t, "", out[CredKeyReclaudeSK])
	})
}

func TestReclaudeCredentialCipher_DecryptSecret(t *testing.T) {
	t.Run("往返等价", func(t *testing.T) {
		cipher := NewReclaudeCredentialCipher(&fakeEncryptor{})
		stored, err := cipher.EncryptForStorage(map[string]any{CredKeyReclaudeSK: "sk-rec-abc123"})
		require.NoError(t, err)

		account := &Account{Type: AccountTypeReclaude, Credentials: stored}
		got, err := cipher.DecryptSecret(account, CredKeyReclaudeSK)

		require.NoError(t, err)
		require.Equal(t, "sk-rec-abc123", got)
	})

	// 静默接受明文会掩盖「密文没写进去」这类故障，而那正是最该炸出来的。
	t.Run("值没有加密标记时报错而不是当明文返回", func(t *testing.T) {
		cipher := NewReclaudeCredentialCipher(&fakeEncryptor{})
		account := &Account{Type: AccountTypeReclaude, Credentials: map[string]any{
			CredKeyReclaudeSK: "sk-rec-plaintext",
		}}

		_, err := cipher.DecryptSecret(account, CredKeyReclaudeSK)

		require.Error(t, err)
		require.NotContains(t, err.Error(), "sk-rec-plaintext", "错误信息不得回显凭据")
	})

	t.Run("字段缺失时报错", func(t *testing.T) {
		cipher := NewReclaudeCredentialCipher(&fakeEncryptor{})
		account := &Account{Type: AccountTypeReclaude, Credentials: map[string]any{}}

		_, err := cipher.DecryptSecret(account, CredKeyReclaudeSK)

		require.Error(t, err)
	})

	// 密钥轮换/丢失就是这个场景：必须炸，不能降级成空凭据静默上游 401。
	t.Run("解密失败时报错且不回显密文", func(t *testing.T) {
		cipher := NewReclaudeCredentialCipher(&fakeEncryptor{decryptErr: errFakeNotCipher})
		account := &Account{Type: AccountTypeReclaude, Credentials: map[string]any{
			CredKeyReclaudeSK: encryptedCredentialPrefix + "CIPHER(x)",
		}}

		_, err := cipher.DecryptSecret(account, CredKeyReclaudeSK)

		require.Error(t, err)
		require.NotContains(t, err.Error(), "CIPHER(")
	})

	t.Run("只允许解密登记在册的秘密字段", func(t *testing.T) {
		cipher := NewReclaudeCredentialCipher(&fakeEncryptor{})
		account := &Account{Type: AccountTypeReclaude, Credentials: map[string]any{
			CredKeyReclaudeGateway: encryptedCredentialPrefix + "CIPHER(x)",
		}}

		_, err := cipher.DecryptSecret(account, CredKeyReclaudeGateway)

		require.Error(t, err)
	})
}

// 脱敏与合并两条出口：两个秘密字段必须登记进敏感键清单，
// 否则会从 DTO 响应、审计日志里漏出去，或在全对象 PUT 时被清空。
func TestReclaudeSecretsAreRegisteredAsSensitive(t *testing.T) {
	t.Run("登记进敏感键清单", func(t *testing.T) {
		require.True(t, IsSensitiveCredentialKey(CredKeyReclaudeSK))
		require.True(t, IsSensitiveCredentialKey(CredKeyReclaudeSeed))
	})

	t.Run("非秘密字段不在清单里", func(t *testing.T) {
		for _, key := range []string{
			CredKeyReclaudeDeviceID, CredKeyReclaudeFingerprint, CredKeyReclaudeGateway,
			CredKeyReclaudeClientVersion, CredKeyReclaudeClientPlatform, CredKeyReclaudeUserEmail,
		} {
			require.Falsef(t, IsSensitiveCredentialKey(key), "%s 不应被脱敏，它是运维要看的展示字段", key)
		}
	})

	t.Run("全对象 PUT 未带秘密字段时保留原值", func(t *testing.T) {
		existing := map[string]any{
			CredKeyReclaudeSK:   encryptedCredentialPrefix + "CIPHER(sk-rec-abc)",
			CredKeyReclaudeSeed: encryptedCredentialPrefix + "CIPHER(seed)",
		}
		incoming := map[string]any{CredKeyReclaudeGateway: "https://la.route.reclaude.ai"}

		merged := MergePreservingSensitiveCreds(existing, incoming)

		require.Equal(t, existing[CredKeyReclaudeSK], merged[CredKeyReclaudeSK])
		require.Equal(t, existing[CredKeyReclaudeSeed], merged[CredKeyReclaudeSeed])
	})
}
