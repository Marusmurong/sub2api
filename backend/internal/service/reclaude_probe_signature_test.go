//go:build unit

package service

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/reclaude"
)

// 控制面端点（/client/account）必须带签名头。
// 不带的话网关回 400 device_signature_required，而自检会把它显示成
// 「凭据无效」—— 一个缺签名的问题伪装成凭据问题，2026-09-23 踩过。
func TestProbeControlPlaneRequestIsSigned(t *testing.T) {
	seed := make([]byte, reclaude.DeviceSeedBytes)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	cipher := NewReclaudeCredentialCipher(&fakeEncryptor{})
	account := &Account{
		ID: 1,
		Credentials: map[string]any{
			CredKeyReclaudeSeed:     "enc:v1:CIPHER(" + hex.EncodeToString(seed) + ")",
			CredKeyReclaudeDeviceID: "44145",
		},
	}
	probe := &ReclaudeGatewayProbe{cipher: cipher}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"https://asia.route.reclaude.ai/client/account", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.signControlPlaneRequest(req, account, nil); err != nil {
		t.Fatalf("签名失败: %v", err)
	}

	for _, h := range []string{
		reclaude.HeaderDeviceID, reclaude.HeaderTimestamp,
		reclaude.HeaderNonce, reclaude.HeaderBodySHA256, reclaude.HeaderSignature,
	} {
		if req.Header.Get(h) == "" {
			t.Fatalf("缺少签名头 %s", h)
		}
	}

	// 空 body 的摘要必须是 sha256("")，且签名可被设备公钥验证。
	// 这个常量与 2026-09-23 抓到的真实客户端出站头逐字符相同（base64url 无填充），
	// 是「我们的编码变体与真值一致」的最小锁。
	const realClientEmptyBodySHA = "47DEQpj8HBSa-_TImW-5JCeuQeRkm5NMpJWZG3hSuFU"
	emptySum := sha256.Sum256(nil)
	if base64.RawURLEncoding.EncodeToString(emptySum[:]) != realClientEmptyBodySHA {
		t.Fatal("空 body 摘要的编码变体与真实客户端不一致")
	}
	if got := req.Header.Get(reclaude.HeaderBodySHA256); got != base64.RawURLEncoding.EncodeToString(emptySum[:]) {
		t.Fatalf("空 body 摘要不对: %s", got)
	}
	ts, err := strconv.ParseInt(req.Header.Get(reclaude.HeaderTimestamp), 10, 64)
	if err != nil {
		t.Fatalf("时间戳不是整数: %v", err)
	}
	nonce := req.Header.Get(reclaude.HeaderNonce)
	canonical := "v1\n" + strconv.FormatInt(ts, 10) + "\n" + nonce + "\n" +
		base64.RawURLEncoding.EncodeToString(emptySum[:])
	sig, err := base64.RawURLEncoding.DecodeString(req.Header.Get(reclaude.HeaderSignature))
	if err != nil {
		t.Fatalf("签名不是 base64: %v", err)
	}
	if !ed25519.Verify(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey), []byte(canonical), sig) {
		t.Fatal("签名验不过：canonical 串与签名器实现不一致")
	}
}
