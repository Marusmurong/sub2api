package service

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/reclaude"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
)

// 建号硬校验的错误。全部是**拒绝创建**，不是警告 —— 这类账号有一堆建号后
// 不可逆的约束（人设只有一次机会、禁止二次 login），录错了只能重新买。
var (
	ErrReclaudeProxyRequired    = errors.New("reclaude account requires a bound proxy")
	ErrReclaudeSeedInvalid      = errors.New("reclaude ed25519 seed is invalid")
	ErrReclaudeSKInvalid        = errors.New("reclaude sk is invalid")
	ErrReclaudeDeviceIDInvalid  = errors.New("reclaude device id is invalid")
	ErrReclaudePlanTierRequired = errors.New("reclaude plan tier must be selected")
	// ErrReclaudeDeviceIdentityMissing：metadata.user_id 的两个身份字段缺失或形态不对。
	// rec 服务端按它们识别设备，错了会被拒为 bad_envelope / state mismatch。
	ErrReclaudeDeviceIdentityMissing = errors.New("reclaude device identity (claude user id / account uuid) is required")
	ErrReclaudeGatewayNotAllowed     = errors.New("reclaude gateway url is not an allowed route node")
	ErrReclaudeClientIdentity        = errors.New("reclaude client version and platform are required")
	ErrReclaudeGroupMixed            = errors.New("reclaude accounts must not share a group with self-hosted accounts")
	ErrReclaudeGroupNotExclusive     = errors.New("each reclaude device must have its own group")
)

// ReclaudeSKPrefix 是 SK 的固定前缀（设备页面上可见）。
const ReclaudeSKPrefix = "sk-rec-"

// ReclaudeAllowedGatewayHosts 是节点硬白名单。
//
// 默认配置下 SSRF 校验不生效，这是唯一的防线：gateway_url 决定我们把**全量
// 明文 prompt** 发到哪台机器上。不做 auto-pick，固定一个节点。
var ReclaudeAllowedGatewayHosts = []string{
	// 🔴 这四个就是真客户端的**候选表全集**，缺一不可、多一个都不行。
	//
	// ✅ 真值（2026-09-24 对客户端二进制做 strings 只读分析）：
	//   - 源文件名 `client/internal/config/gateway_candidates.go`
	//   - 符号 `config.PickFastestGateway` / `config.ProbeGateway`
	//   - 四个候选常量均以 `https://` 前缀连续存放
	//
	// daemon 启动时对它们并发做 RTT 探测（`gatewayProbeURL` = base + "/proxy"，
	// 无凭据空 POST），取最快的一个写进 state.json。
	"asia.route.reclaude.ai",
	"la.route.reclaude.ai",
	"misaka.route.reclaude.ai",
	// 真实节点名无 .route 段 —— `reclaude config gateway` 输出实测。
	"cloudfront.reclaude.ai",

	// 🔴 **www.reclaude.ai 已于 2026-09-24 移出白名单，不要加回来。**
	//
	// 它不是候选节点，是**「所有候选都探测失败」时的兜底值**。决定性证据是
	// 客户端自己的 i18n 字符串：
	//
	//	"gateway: auto-picked %s (RTT %s, %d candidates probed)"
	//	"gateway: auto-pick failed (no reachable candidates), falling back to %s"
	//	"(daemon will auto-pick; url is fallback)"
	//
	// 正常工作的真客户端**永远不会**停在 www 上；停在上面意味着它那一刻
	// 一个节点都连不通。而我们此前把全部账号固定在这里 —— 等于整个设备群
	// 长期呈现一种真实世界里不该批量出现的形态。
	// 对端判别它零成本：看哪些设备的 /proxy 落在 apex 而不是 route 节点即可。
	//
	// ⚠️ 此处原注释称「按 route 节点建的号被拒为 device_signature_required，
	// 所以只能用主域」——**那个归因是错的**。那次失败发生在 K-4 签名 bug
	// （base64 变体用了 Std(padded)、控制面 GET 未签名）修复**之前**；
	// 真客户端每天都在打 route 节点且签名正常，而 device_signature_required
	// 是签名错误码，与端点无关。一条错记的注释把我们锁在兜底域上近一周。
	//
	// 佐证（D8）：信封 edge 是对候选表的一次查表，命中回 host、未命中回
	// "unknown"。26/26 条真实信封 edge = "unknown"（那批 daemon 指向 www/sink），
	// 而真客户端遥测 rollup 的 edge = "asia.route.reclaude.ai"。
	// 两处设计自洽地指向同一件事：真客户端不该打 apex。
}

var reclaudeFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// ReclaudeAccountInput 是建号表单里与 reclaude 相关的字段。
type ReclaudeAccountInput struct {
	ProxyID        *int64
	SK             string
	SeedEncoded    string
	DeviceID       int64
	Fingerprint    string
	GatewayURL     string
	ClientVersion  string
	ClientPlatform string
	UserEmail      string
	PlanTier       string
	ClaudeUserID   string
	AccountUUID    string
	DeviceHostname string
}

// ReclaudeValidationResult 是校验产物。
//
// Warnings 与 error 分开：V-3 的指纹自校验依赖一个**算法未知**的推导函数，
// 结案前只能警告，否则会卡在一个今天测不出来的校验上。
type ReclaudeValidationResult struct {
	Seed              []byte
	NormalizedGateway string
	// DailyLimitUSD 由所选档位换算而来，建号时直接写进 extra.quota_daily_limit。
	DailyLimitUSD float64
	Warnings      []string
}

// ValidateReclaudeAccountInput 执行建号硬校验。
func ValidateReclaudeAccountInput(input ReclaudeAccountInput) (ReclaudeValidationResult, error) {
	var result ReclaudeValidationResult

	// V-1：无代理 = 直接用机房 IP，出口 IP 稳定性是隐蔽性参数之一。
	if input.ProxyID == nil || *input.ProxyID <= 0 {
		return result, ErrReclaudeProxyRequired
	}

	if !strings.HasPrefix(strings.TrimSpace(input.SK), ReclaudeSKPrefix) {
		return result, fmt.Errorf("%w: expected %q prefix", ErrReclaudeSKInvalid, ReclaudeSKPrefix)
	}

	// V-2：长度不对说明从 keychain / device.key 里抠错了东西，签名必然全挂。
	seed, err := decodeDeviceSeed(input.SeedEncoded)
	if err != nil {
		return result, fmt.Errorf("%w: %s", ErrReclaudeSeedInvalid, err.Error())
	}
	result.Seed = seed

	// V-4 的应用层部分。全局唯一性靠 DB partial unique index 兜底 ——
	// 只靠应用层 check 并发建号会漏。
	if input.DeviceID <= 0 {
		return result, ErrReclaudeDeviceIDInvalid
	}

	// V-5：包络未标定就售卖 = 超卖。
	//
	// 档位换算出美元日限额，走既有的配额子系统（日/周/总 + 固定/滚动重置）。
	// 不再单独维护 token 计的日闸 —— 两套闸并存会让「哪个在生效」变成
	// 需要查代码才能回答的问题。
	tier, ok := FindReclaudePlanTier(strings.TrimSpace(input.PlanTier))
	if !ok {
		return result, fmt.Errorf("%w: got %q", ErrReclaudePlanTierRequired, input.PlanTier)
	}
	result.DailyLimitUSD = tier.DailyLimitUSD

	// 🔴 设备身份：rec 按 metadata.user_id 里的 device_id / account_uuid 识别设备。
	// 缺失或形态不对时账号建得出来但一定跑不通，而失败信息是对方那句
	// 「请重启 reclaude」，完全看不出是建号时填漏了（2026-09-24 定位）。
	if !claudeUserIDPattern.MatchString(strings.TrimSpace(input.ClaudeUserID)) {
		return result, fmt.Errorf("%w: claude user id must be 64 hex chars (from ~/.claude.json userID), got %q",
			ErrReclaudeDeviceIdentityMissing, input.ClaudeUserID)
	}
	if strings.TrimSpace(input.AccountUUID) == "" {
		return result, fmt.Errorf("%w: account uuid is empty", ErrReclaudeDeviceIdentityMissing)
	}

	if strings.TrimSpace(input.ClientVersion) == "" || strings.TrimSpace(input.ClientPlatform) == "" {
		return result, ErrReclaudeClientIdentity
	}

	// V-9：https + 四节点硬白名单。
	normalized, err := urlvalidator.ValidateHTTPSURL(input.GatewayURL, urlvalidator.ValidationOptions{
		AllowedHosts:     ReclaudeAllowedGatewayHosts,
		RequireAllowlist: true,
	})
	if err != nil {
		return result, fmt.Errorf("%w: %s", ErrReclaudeGatewayNotAllowed, err.Error())
	}
	result.NormalizedGateway = normalized

	// V-3：降级为警告。推导算法未知（逆向只拿到「8 字节 hex」与符号名），
	// 结案后这里改成由 seed 推公钥指纹并比对，再升级为拒绝。
	if !reclaudeFingerprintPattern.MatchString(strings.ToLower(strings.TrimSpace(input.Fingerprint))) {
		result.Warnings = append(result.Warnings,
			"reclaude fingerprint is not 8-byte hex; cannot self-verify it against the seed "+
				"until the derivation algorithm is confirmed")
	}

	return result, nil
}

// decodeDeviceSeed 接受 hex 或 base64，解码后必须恰好 32 字节。
func decodeDeviceSeed(encoded string) ([]byte, error) {
	trimmed := strings.TrimSpace(encoded)
	if trimmed == "" {
		return nil, errors.New("seed is empty")
	}

	if seed, err := hex.DecodeString(trimmed); err == nil {
		return checkSeedLength(seed)
	}
	if seed, err := base64.StdEncoding.DecodeString(trimmed); err == nil {
		return checkSeedLength(seed)
	}
	return nil, errors.New("seed is neither valid hex nor base64")
}

func checkSeedLength(seed []byte) ([]byte, error) {
	if len(seed) != reclaude.DeviceSeedBytes {
		return nil, fmt.Errorf("seed must decode to %d bytes, got %d", reclaude.DeviceSeedBytes, len(seed))
	}
	return seed, nil
}

var reclaudeHostnameSanitizer = regexp.MustCompile(`[^a-z0-9-]+`)

// BuildReclaudeAccountName 生成账号名：rec/<设备hostname>-<device_id 后 4 位>。
//
// 建号时自动生成、不可编辑：rec/ 前缀让号池列表一眼分得出来，hostname 能与
// reclaude 设备页面直接对账，device_id 后缀避免同一人设下建多个时撞名。
//
// ⚠️ 名字只是**人眼判据**。所有代码分支一律以 AccountType 为准，绝不靠名字前缀判断。
func BuildReclaudeAccountName(hostname string, deviceID int64) string {
	slug := reclaudeHostnameSanitizer.ReplaceAllString(strings.ToLower(strings.TrimSpace(hostname)), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "device"
	}

	suffix := strconv.FormatInt(deviceID, 10)
	if len(suffix) > 4 {
		suffix = suffix[len(suffix)-4:]
	}
	return "rec/" + slug + "-" + suffix
}

// CheckReclaudeGroupIsolation 执行 V-6 的双向分组隔离。
//
// ⚠️ 必须按 Type 判定，不能按 platform：reclaude 账号的 platform 仍是 anthropic，
// 既有的 checkMixedChannelRisk 比的是 platform ⇒ 现状不会触发任何告警。
//
// 分组隔离是最后一道防线：即使名字被人改乱，调度也不会把两类号混在一起 failover。
func CheckReclaudeGroupIsolation(account *Account, groupMembers []*Account) error {
	if account == nil {
		return nil
	}

	for _, member := range groupMembers {
		if member == nil || member.ID == account.ID {
			continue
		}
		if member.IsReclaude() != account.IsReclaude() {
			return fmt.Errorf("%w: account %d and account %d have different sourcing models",
				ErrReclaudeGroupMixed, account.ID, member.ID)
		}
	}
	return nil
}

// CheckReclaudeSingleAccountPerGroup 执行分片约束：**1 台设备 = 1 个 group**。
//
// 为什么是硬校验而不是约定：sub2api 的**组内 failover 是自动的**
// （FailoverContinue 会转给组内下一个账号）。两台 rec 设备放进同一个 group，
// 「片内自动跨设备兜底」就成了默认行为 —— 而那恰恰被禁止：一台设备被封时
// 自动把流量甩到邻片，会把邻片的包络也打穿，形成连环封号。
//
// 设备死了正确的处理是：**该片降级并告警，人工处理**。
//
// 若将来要在一个 group 里放多台设备，需要新增「禁止组内 failover」的账号级
// 开关 —— 那是新功能，不是放宽这条校验。
func CheckReclaudeSingleAccountPerGroup(account *Account, groupMembers []*Account) error {
	if account == nil || !account.IsReclaude() {
		return nil
	}

	for _, member := range groupMembers {
		if member == nil || member.ID == account.ID || !member.IsReclaude() {
			continue
		}
		return fmt.Errorf("%w: group already holds reclaude account %d", ErrReclaudeGroupNotExclusive, member.ID)
	}
	return nil
}
