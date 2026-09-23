package reclaude

import (
	"crypto/ed25519"
	"crypto/sha256"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// 固定 seed，仅用于测试向量。
func testSeed() []byte {
	seed := make([]byte, DeviceSeedBytes)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	return seed
}

func TestNewDeviceSigner(t *testing.T) {
	t.Run("合法 seed 与 device_id", func(t *testing.T) {
		signer, err := NewDeviceSigner(testSeed(), 43448)

		require.NoError(t, err)
		require.Equal(t, int64(43448), signer.DeviceID())
	})

	t.Run("seed 长度不是 32 字节直接拒绝", func(t *testing.T) {
		for _, size := range []int{0, 16, 31, 33, 64} {
			_, err := NewDeviceSigner(make([]byte, size), 1)
			require.Errorf(t, err, "size=%d 应当被拒绝", size)
		}
	})

	t.Run("device_id 非正数直接拒绝", func(t *testing.T) {
		for _, deviceID := range []int64{0, -1} {
			_, err := NewDeviceSigner(testSeed(), deviceID)
			require.Errorf(t, err, "device_id=%d 应当被拒绝", deviceID)
		}
	})
}

func TestCanonicalString(t *testing.T) {
	// canonical = "v1\n" + ts + "\n" + base64(nonce) + "\n" + base64(sha256(信封字节))
	// 注意：哈希的输入是**整个信封**，不是原始请求体。
	t.Run("按 v1 格式逐段拼接", func(t *testing.T) {
		// Arrange
		nonce := make([]byte, SignatureNonceBytes)
		for i := range nonce {
			nonce[i] = 0xAB
		}
		envelope := []byte("envelope-bytes")
		sum := sha256.Sum256(envelope)

		// Act
		canonical := canonicalString(1758412800123, nonce, envelope)

		// Assert
		expected := SignatureVersion + "\n" +
			"1758412800123" + "\n" +
			signatureEncoding.EncodeToString(nonce) + "\n" +
			signatureEncoding.EncodeToString(sum[:])
		require.Equal(t, expected, canonical)
	})

	t.Run("哈希的是信封而不是请求体", func(t *testing.T) {
		nonce := make([]byte, SignatureNonceBytes)
		body := []byte(`{"model":"claude"}`)
		meta := ClientRequestMetadata{URL: "https://x", Method: "POST", Headers: map[string]string{}}
		envelope, err := EncodeEnvelope(meta, body)
		require.NoError(t, err)

		fromEnvelope := canonicalString(1, nonce, envelope)
		fromBody := canonicalString(1, nonce, body)

		require.NotEqual(t, fromBody, fromEnvelope)
	})
}

func TestAddSignatureHeaders(t *testing.T) {
	t.Run("七个头齐全且长度符合规格", func(t *testing.T) {
		// Arrange
		signer, err := NewDeviceSigner(testSeed(), 43448)
		require.NoError(t, err)
		envelope := []byte("envelope-bytes")
		header := http.Header{}

		// Act
		require.NoError(t, signer.AddSignatureHeaders(header, envelope))

		// Assert：base64 长度是 K-4 的直接体现，变体改了这里会先红
		require.NotEmpty(t, header.Get(HeaderTimestamp))
		require.Len(t, header.Get(HeaderNonce), 22, "base64url 无填充 16B 应为 22 字符")
		require.Len(t, header.Get(HeaderBodySHA256), 43, "base64url 无填充 32B 应为 43 字符")
		require.Len(t, header.Get(HeaderSignature), 86, "base64url 无填充 64B 应为 86 字符")
		require.Equal(t, "43448", header.Get(HeaderDeviceID))
	})

	// 真正的正确性验证：用 seed 推出的公钥去验签。
	// 这不是自我一致性检查 —— ed25519.Verify 是标准库的独立实现。
	t.Run("签名可被对应公钥验证通过", func(t *testing.T) {
		signer, err := NewDeviceSigner(testSeed(), 1)
		require.NoError(t, err)
		envelope := []byte("hello envelope")
		header := http.Header{}
		require.NoError(t, signer.AddSignatureHeaders(header, envelope))

		sig, err := signatureEncoding.DecodeString(header.Get(HeaderSignature))
		require.NoError(t, err)
		nonce, err := signatureEncoding.DecodeString(header.Get(HeaderNonce))
		require.NoError(t, err)

		ts, err := parseTimestampHeader(header.Get(HeaderTimestamp))
		require.NoError(t, err)

		pub := ed25519.NewKeyFromSeed(testSeed()).Public().(ed25519.PublicKey)
		require.True(t, ed25519.Verify(pub, []byte(canonicalString(ts, nonce, envelope)), sig))
	})

	t.Run("每次调用的 nonce 不同", func(t *testing.T) {
		signer, err := NewDeviceSigner(testSeed(), 1)
		require.NoError(t, err)

		first, second := http.Header{}, http.Header{}
		require.NoError(t, signer.AddSignatureHeaders(first, []byte("x")))
		require.NoError(t, signer.AddSignatureHeaders(second, []byte("x")))

		require.NotEqual(t, first.Get(HeaderNonce), second.Get(HeaderNonce))
	})

	t.Run("空信封拒绝签名", func(t *testing.T) {
		signer, err := NewDeviceSigner(testSeed(), 1)
		require.NoError(t, err)

		require.Error(t, signer.AddSignatureHeaders(http.Header{}, nil))
	})
}

// 黄金向量：锁死 canonical 串格式与 base64 变体不被无意改动。
//
// 下面三个字面量**不是从本实现的输出抄来的**，是用独立实现算的：
//
//	sha256: printf 'golden-vector-envelope' | shasum -a 256 | xxd -r -p | base64
//	ed25519: python3 cryptography.hazmat…Ed25519PrivateKey.from_private_bytes(bytes(range(1,33)))
//
// 所以它同时是一次跨实现验证，而不只是自我一致性回归。
//
// 2026-09-23：base64 变体已由 sink 抓到的真实出站头定案为 **base64url 无填充**
// （K-4 结案，见 signature.go 的 signatureEncoding 注释）。本向量随之重算 ——
// nonce/sha/签名三项都用上面那条 python 命令独立生成，仍是跨实现验证。
func TestSignatureGoldenVector(t *testing.T) {
	const (
		fixedTsMillis   = int64(1758412800123)
		wantNonceB64    = "q6urq6urq6urq6urq6urqw"
		wantBodyHashB64 = "kV9_G4SfENqgTgDUoY6rBkM61HpaOOSsfqnt1Gt--_0"
		wantSignature   = "J3ONOeUbmcZmP_r-pSkk8VPqB1Slg34cOHgR_iAD8JMCLZyyf-MtMzWB4LTFirQoyjuQa0XlPdw_68sPU8WACg"
	)

	signer, err := NewDeviceSigner(testSeed(), 43448)
	require.NoError(t, err)

	nonce := make([]byte, SignatureNonceBytes)
	for i := range nonce {
		nonce[i] = 0xAB
	}
	envelope := []byte("golden-vector-envelope")

	header := http.Header{}
	require.NoError(t, signer.addSignatureHeadersAt(header, envelope, fixedTsMillis, nonce))

	require.Equal(t, "1758412800123", header.Get(HeaderTimestamp))
	require.Equal(t, wantNonceB64, header.Get(HeaderNonce))
	require.Equal(t, wantBodyHashB64, header.Get(HeaderBodySHA256))
	require.Equal(t, wantSignature, header.Get(HeaderSignature))
}
