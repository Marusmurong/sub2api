// Package reclaude 实现 reclaude 中转网关的线协议：信封编解码、设备签名与
// 带外事件分类。
//
// 本包是**纯函数**层：不做 IO、不认识 sub2api 的 Account/Service，因此可以被
// 独立的验证脚本（cmd/reclaude-probe）与生产链路共用同一份实现。
package reclaude

const (
	// EnvelopeLengthPrefixBytes 信封的长度前缀宽度：4 字节大端。
	EnvelopeLengthPrefixBytes = 4

	// EnvelopeMaxMetadataBytes 元数据解码上限 16 MiB，超出即报错。
	EnvelopeMaxMetadataBytes = 16 << 20

	// SignatureVersion canonical 串的版本前缀。
	SignatureVersion = "v1"

	// SignatureNonceBytes nonce 长度：16 字节 crypto/rand。
	SignatureNonceBytes = 16

	// DeviceSeedBytes 设备私钥种子长度：32 字节裸 ed25519 seed。
	DeviceSeedBytes = 32
)

// 出站请求头名（§4.3）。Authorization / Content-Type 由调用方按账号凭据补齐。
const (
	HeaderClientVersion  = "X-Reclaude-Client-Version"
	HeaderClientPlatform = "X-Reclaude-Client-Platform"
	HeaderDeviceID       = "X-Reclaude-Device-Id"
	HeaderTimestamp      = "X-Reclaude-Ts"
	HeaderNonce          = "X-Reclaude-Nonce"
	HeaderBodySHA256     = "X-Reclaude-Body-Sha256"
	HeaderSignature      = "X-Reclaude-Signature"
)

// ClientRequestMetadata 是请求信封的元数据段。
//
// Headers 必须全小写 —— 这是对端的硬约束，EncodeEnvelope 会校验。
type ClientRequestMetadata struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	TraceID   string            `json:"traceId,omitempty"`
	Edge      string            `json:"edge,omitempty"`
	Keepalive bool              `json:"keepalive,omitempty"`
}

// GatewayResponseMetadata 是响应信封的元数据段。
//
// Events 是**带外控制信道**：它既可能出现在错误响应里，也可能出现在 status=200
// 的正常响应里，且绝不能透传给下游（原版会把它伪装成 Anthropic 错误吐给用户，
// 内容含别人的掩码邮箱）。
type GatewayResponseMetadata struct {
	Status     int               `json:"status"`
	StatusText string            `json:"statusText,omitempty"`
	Headers    map[string]string `json:"headers"`
	Events     []ReclaudeEvent   `json:"events,omitempty"`
	Keepalive  bool              `json:"keepalive,omitempty"`
}

// ReclaudeEvent 单条带外事件。
type ReclaudeEvent struct {
	Kind                  string `json:"kind"`
	Reason                string `json:"reason,omitempty"`
	RetryAfterSec         int    `json:"retryAfterSec,omitempty"`
	DaysLeft              int    `json:"daysLeft,omitempty"`
	NewAccountMaskedEmail string `json:"newAccountMaskedEmail,omitempty"`
}

// ClientAccountResponse 是 GET /client/account 的响应（login.currentAccountResponse）。
//
// 🔴 这个结构里**没有**任何 token 数、额度、余量字段 —— 他们不提供用量接口，
// 这是确定的。别指望从这里对账。它唯一的运维价值是 user_email：
// 底层 Claude 账号被静默换掉时，这个字段会变，而带外事件不保证每次都到。
type ClientAccountResponse struct {
	UserEmail        string `json:"user_email"`
	AccountUUID      string `json:"account_uuid,omitempty"`
	OrganizationUUID string `json:"organization_uuid,omitempty"`
	BillingType      string `json:"billing_type,omitempty"`
	DisplayName      string `json:"display_name,omitempty"`
}
