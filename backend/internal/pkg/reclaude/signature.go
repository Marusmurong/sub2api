package reclaude

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// signatureEncoding 是签名相关头的 base64 变体。
//
// ✅ K-4 已结案（2026-09-23，真值抓包）：**base64url 无填充**（RawURLEncoding）。
//
// 抓法：`reclaude config gateway set --force <本地 sink>` 后重启 daemon，它 60s 一次的
// /client/account 与 /client/intercept-domains 会带着完整签名头打到 sink
// （reclaude-lab/sink/dump/*client_account*）。真实样本里 Body-Sha256 是 43 字符、
// Nonce 22 字符、Signature 86 字符，全部无 `=` 填充且出现 `-` `_` —— 标准 base64
// 的 `+` `/` `=` 与真值对不上，网关一律拒为 device_signature_required。
//
// canonical 串本身已用设备私钥反验过，与本文件实现逐字节相同（v1\n ts \n nonce \n sha，
// 拼的是**编码后的串**），所以此前唯一的错误就是这里的变体。
//
// 🔴 改这一处会同时改掉 /proxy 信封签名。要改先重抓，别凭推断。
var signatureEncoding = base64.RawURLEncoding

// DeviceSigner 用设备私钥为出站信封签名。
//
// 一次生成、终身不动：seed 与 device_id 来自 login 产出的 device.key / device.json，
// 绝不二次 login（takeover 会迁移设备）。
type DeviceSigner struct {
	privateKey ed25519.PrivateKey
	deviceID   int64
}

// NewDeviceSigner 校验并构造签名器。
//
// seed 必须是 32 字节裸 ed25519 seed（device.key 的 wc -c 应当正好是 32）。
func NewDeviceSigner(seed []byte, deviceID int64) (*DeviceSigner, error) {
	if len(seed) != DeviceSeedBytes {
		return nil, fmt.Errorf("device seed must be %d bytes, got %d", DeviceSeedBytes, len(seed))
	}
	if deviceID <= 0 {
		return nil, fmt.Errorf("device id must be positive, got %d", deviceID)
	}
	return &DeviceSigner{
		privateKey: ed25519.NewKeyFromSeed(seed),
		deviceID:   deviceID,
	}, nil
}

// DeviceID 返回设备号。
func (s *DeviceSigner) DeviceID() int64 { return s.deviceID }

// PublicKey 返回设备公钥，供建号校验比对 public_key_fingerprint 使用。
func (s *DeviceSigner) PublicKey() ed25519.PublicKey {
	return s.privateKey.Public().(ed25519.PublicKey)
}

// canonicalString 拼出待签名串：
//
//	"v1\n" + ts + "\n" + base64(nonce) + "\n" + base64(sha256(envelope))
//
// 🔴 envelope 是**整个信封字节**，不是原始请求体。
// 签名不覆盖 URL / method / headers ⇒ 防重放与 body 篡改，不防路径替换。
func canonicalString(tsMillis int64, nonce, envelope []byte) string {
	sum := sha256.Sum256(envelope)
	return SignatureVersion + "\n" +
		strconv.FormatInt(tsMillis, 10) + "\n" +
		signatureEncoding.EncodeToString(nonce) + "\n" +
		signatureEncoding.EncodeToString(sum[:])
}

// AddControlPlaneSignatureHeaders 为**控制面**请求（/client/account 一类，
// 不带信封）签名。
//
// 🔴 2026-09-23 实测：`GET /client/account` 不带签名头会被网关拒为
// `{"code":"device_signature_required","status":400}`（而 /health/ready 不需要，
// 所以自检第一步过、第二步挂，看起来像凭据无效，其实是缺签名）。
//
// 与信封签名的唯一区别是**允许空 body**：GET 没有请求体，签的是 sha256("")。
// canonical 串格式完全相同，所以这里复用同一条计算路径，不另起一套。
func (s *DeviceSigner) AddControlPlaneSignatureHeaders(header http.Header, body []byte) error {
	nonce := make([]byte, SignatureNonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("generate signature nonce: %w", err)
	}
	return s.signAt(header, body, time.Now().UnixMilli(), nonce, true)
}

// AddSignatureHeaders 用当前时间与随机 nonce 为信封签名，写入签名相关头。
//
// 不设置 Authorization / Content-Type / Client-Version / Client-Platform ——
// 那几个来自账号凭据，由调用方补齐（版本号还需按账号定版，见客户端版本跟随）。
func (s *DeviceSigner) AddSignatureHeaders(header http.Header, envelope []byte) error {
	nonce := make([]byte, SignatureNonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("generate signature nonce: %w", err)
	}
	return s.addSignatureHeadersAt(header, envelope, time.Now().UnixMilli(), nonce)
}

// addSignatureHeadersAt 是 AddSignatureHeaders 的可注入时间与 nonce 的版本，
// 供黄金向量测试使用。
func (s *DeviceSigner) addSignatureHeadersAt(header http.Header, envelope []byte, tsMillis int64, nonce []byte) error {
	return s.signAt(header, envelope, tsMillis, nonce, false)
}

// signAt 是签名的唯一实现。allowEmpty 只对控制面请求放开空 body ——
// 信封路径保留「空即 bug」的硬校验：一个空信封发出去必然是构造代码写错了。
func (s *DeviceSigner) signAt(
	header http.Header, envelope []byte, tsMillis int64, nonce []byte, allowEmpty bool,
) error {
	if header == nil {
		return fmt.Errorf("sign envelope: header is nil")
	}
	if len(envelope) == 0 && !allowEmpty {
		return fmt.Errorf("sign envelope: envelope is empty")
	}
	if len(nonce) != SignatureNonceBytes {
		return fmt.Errorf("sign envelope: nonce must be %d bytes, got %d", SignatureNonceBytes, len(nonce))
	}

	sum := sha256.Sum256(envelope)
	canonical := canonicalString(tsMillis, nonce, envelope)
	signature := ed25519.Sign(s.privateKey, []byte(canonical))

	header.Set(HeaderDeviceID, strconv.FormatInt(s.deviceID, 10))
	header.Set(HeaderTimestamp, strconv.FormatInt(tsMillis, 10))
	header.Set(HeaderNonce, signatureEncoding.EncodeToString(nonce))
	header.Set(HeaderBodySHA256, signatureEncoding.EncodeToString(sum[:]))
	header.Set(HeaderSignature, signatureEncoding.EncodeToString(signature))
	return nil
}

// parseTimestampHeader 解析 X-Reclaude-Ts。
func parseTimestampHeader(value string) (int64, error) {
	ts, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse timestamp header %q: %w", value, err)
	}
	return ts, nil
}
