package service

import (
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// reclaude 账号的 credentials 子键。
const (
	CredKeyReclaudeSK             = "reclaude_sk"
	CredKeyReclaudeSeed           = "reclaude_ed25519_seed"
	CredKeyReclaudeDeviceID       = "reclaude_device_id"
	CredKeyReclaudeFingerprint    = "reclaude_fingerprint"
	CredKeyReclaudeGateway        = "reclaude_gateway_url"
	CredKeyReclaudeClientVersion  = "reclaude_client_version"
	CredKeyReclaudeClientPlatform = "reclaude_client_platform"
	CredKeyReclaudeUserEmail      = "reclaude_user_email"

	// CredKeyReclaudeSyntheticAccountUUID 是我们自己生成的稳定 UUID，用于身份统一。
	// 不是上游真值 —— reclaude 不给 Anthropic 的 account_uuid，且底层账号会被静默
	// 换掉，根本不存在一个稳定的真实值。静默换号时**不得更换**它，否则上游会看到
	// 「一台设备突然换了人」。
	CredKeyReclaudeSyntheticAccountUUID = "reclaude_synthetic_account_uuid"
)

// encryptedCredentialPrefix 标记该字段已加密。
//
// 带版本号是为了将来换算法时能区分存量；没有这个标记就无法分辨
// 「明文」与「密文」，而静默把明文当密文用会在上游表现为 401，极难排查。
const encryptedCredentialPrefix = "enc:v1:"

// reclaudeEncryptedCredentialKeys 是必须加密落库的子键。
//
// 只加密这两个，而不是给整张 credentials 表上加密：账号凭据今天是明文 JSONB，
// 全量加密会牵动所有账号类型的读写路径，风险远大于收益。
var reclaudeEncryptedCredentialKeys = []string{CredKeyReclaudeSK, CredKeyReclaudeSeed}

func isReclaudeEncryptedCredentialKey(key string) bool {
	for _, k := range reclaudeEncryptedCredentialKeys {
		if k == key {
			return true
		}
	}
	return false
}

// ErrCredentialEncryptionKeyNotConfigured 表示加密密钥未显式配置。
var ErrCredentialEncryptionKeyNotConfigured = errors.New("credential encryption key is not configured")

// RequireCredentialEncryptionKey 是建号前的 V-8 硬闸。
//
// 密钥未显式配置时每次启动随机生成，加密落库的 sk/seed 在**下次重启后永久不可
// 解密** —— 一份订阅报废，且禁止二次 login（takeover 会迁移设备）⇒ 无法补救。
// 所以这不是警告，是拒绝创建。
func RequireCredentialEncryptionKey(cfg *config.Config) error {
	if cfg == nil || !cfg.Totp.EncryptionKeyConfigured {
		return fmt.Errorf(
			"%w: set a persistent encryption key before creating reclaude accounts, "+
				"otherwise stored credentials become permanently undecryptable after the next restart",
			ErrCredentialEncryptionKeyNotConfigured,
		)
	}
	return nil
}

// ReclaudeCredentialCipher 负责 reclaude 秘密字段的加解密。
type ReclaudeCredentialCipher struct {
	encryptor SecretEncryptor
}

// NewReclaudeCredentialCipher 构造加解密器。
func NewReclaudeCredentialCipher(encryptor SecretEncryptor) *ReclaudeCredentialCipher {
	return &ReclaudeCredentialCipher{encryptor: encryptor}
}

// EncryptForStorage 返回一份新的 credentials，其中登记在册的秘密字段已加密。
//
// 不修改入参。已带加密标记的值原样保留 —— 账号编辑是全对象 PUT，同一份
// credentials 会被反复写回，二次加密会让密文永远解不出原文。
func (c *ReclaudeCredentialCipher) EncryptForStorage(credentials map[string]any) (map[string]any, error) {
	if c == nil || c.encryptor == nil {
		return nil, errors.New("encrypt reclaude credentials: encryptor not configured")
	}

	out := make(map[string]any, len(credentials))
	for key, value := range credentials {
		out[key] = value
	}

	for _, key := range reclaudeEncryptedCredentialKeys {
		raw, ok := out[key].(string)
		if !ok || raw == "" {
			continue
		}
		if hasEncryptedCredentialPrefix(raw) {
			continue
		}
		ciphertext, err := c.encryptor.Encrypt(raw)
		if err != nil {
			// 不回显 raw：它就是凭据本身。
			return nil, fmt.Errorf("encrypt credential %q: %w", key, err)
		}
		out[key] = encryptedCredentialPrefix + ciphertext
	}
	return out, nil
}

// DecryptSecret 读出并解密一个登记在册的秘密字段。
//
// 遇到未加密的值直接报错而不是当明文返回：静默接受明文会掩盖「密文没写进去」
// 这类故障，而那正是最该炸出来的。
func (c *ReclaudeCredentialCipher) DecryptSecret(account *Account, key string) (string, error) {
	if c == nil || c.encryptor == nil {
		return "", errors.New("decrypt reclaude credential: encryptor not configured")
	}
	if !isReclaudeEncryptedCredentialKey(key) {
		return "", fmt.Errorf("decrypt reclaude credential: %q is not an encrypted credential key", key)
	}
	if account == nil {
		return "", errors.New("decrypt reclaude credential: account is required")
	}

	stored := account.GetCredential(key)
	if stored == "" {
		return "", fmt.Errorf("decrypt reclaude credential: %q is missing", key)
	}
	if !hasEncryptedCredentialPrefix(stored) {
		return "", fmt.Errorf("decrypt reclaude credential: %q is not encrypted", key)
	}

	plaintext, err := c.encryptor.Decrypt(stored[len(encryptedCredentialPrefix):])
	if err != nil {
		// 不包 %w 里的密文，也不回显 stored。
		return "", fmt.Errorf("decrypt reclaude credential %q failed (encryption key rotated or lost?)", key)
	}
	return plaintext, nil
}

func hasEncryptedCredentialPrefix(value string) bool {
	return len(value) > len(encryptedCredentialPrefix) && value[:len(encryptedCredentialPrefix)] == encryptedCredentialPrefix
}
