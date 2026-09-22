package service

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const (
	// CredKeyReclaudeDeviceHostname 是授权那台机器的 hostname。
	// 它不参与任何逻辑判断，只用于生成账号名，好让号池列表能与 reclaude
	// 设备页面直接对账。
	CredKeyReclaudeDeviceHostname = "reclaude_device_hostname"

	// CredKeyReclaudeTimezone 是授权那台机器的人设时区。
	// 它在 login 那一刻已被对端落库并永久展示，这里记下来是为了让「模拟关机」
	// 的作息与它自洽 —— 一台时区在上海的设备却按洛杉矶作息合盖，本身就是破绽。
	CredKeyReclaudeTimezone = "reclaude_timezone"

	// ExtraKeyReclaudeDailyTokenCap 是该设备的日 token 硬闸（已弃用）。
	//
	// 🔴 保留仅为读取历史账号：限额已改为按档位换算的**美元**日限额
	// （ExtraKeyReclaudePlanTier → quota_daily_limit），走既有配额子系统。
	// 新建账号不再写这个键。
	ExtraKeyReclaudeDailyTokenCap = "reclaude_daily_token_cap"
)

// PreparedReclaudeAccount 是建号前处理的产物。调用方负责把它写回 input。
type PreparedReclaudeAccount struct {
	Name        string
	Credentials map[string]any
	Extra       map[string]any
	Warnings    []string
}

// PrepareReclaudeAccountCreate 执行 reclaude 建号的全部前置处理：
// V-8 闸 → 硬校验 → 生成账号名 → 生成合成 uuid → 加密秘密字段。
//
// 纯函数：不碰 DB、不修改入参。分组隔离（V-6）需要读库，由调用方另行执行。
//
// extra 要传**已规范化的** account extra（CreateAccount 在调用前会跑一轮平台相关的
// 规范化），不是 input.Extra —— 否则那轮规范化的结果会被这里的返回值覆盖掉。
func PrepareReclaudeAccountCreate(
	cfg *config.Config,
	cipher *ReclaudeCredentialCipher,
	input *CreateAccountInput,
	extra map[string]any,
) (PreparedReclaudeAccount, error) {
	var prepared PreparedReclaudeAccount

	if input == nil {
		return prepared, errors.New("prepare reclaude account: input is required")
	}
	if input.Type != AccountTypeReclaude {
		return prepared, fmt.Errorf("prepare reclaude account: unexpected account type %q", input.Type)
	}
	// platform 固定为 anthropic，整条 Anthropic 链路（伪装、调度、用量解析）才能复用。
	if input.Platform != PlatformAnthropic {
		return prepared, fmt.Errorf("prepare reclaude account: platform must be %q, got %q",
			PlatformAnthropic, input.Platform)
	}

	// V-8 先于一切凭据处理：密钥没配好就不该有任何一条真凭据进入内存路径。
	if err := RequireCredentialEncryptionKey(cfg); err != nil {
		return prepared, err
	}

	validationInput := ReclaudeAccountInput{
		ProxyID:        input.ProxyID,
		SK:             credentialString(input.Credentials, CredKeyReclaudeSK),
		SeedEncoded:    credentialString(input.Credentials, CredKeyReclaudeSeed),
		DeviceID:       credentialInt64(input.Credentials, CredKeyReclaudeDeviceID),
		Fingerprint:    credentialString(input.Credentials, CredKeyReclaudeFingerprint),
		GatewayURL:     credentialString(input.Credentials, CredKeyReclaudeGateway),
		ClientVersion:  credentialString(input.Credentials, CredKeyReclaudeClientVersion),
		ClientPlatform: credentialString(input.Credentials, CredKeyReclaudeClientPlatform),
		UserEmail:      credentialString(input.Credentials, CredKeyReclaudeUserEmail),
		PlanTier:       credentialString(extra, ExtraKeyReclaudePlanTier),
		DeviceHostname: credentialString(input.Credentials, CredKeyReclaudeDeviceHostname),
	}

	validated, err := ValidateReclaudeAccountInput(validationInput)
	if err != nil {
		return prepared, err
	}

	credentials := make(map[string]any, len(input.Credentials)+1)
	for key, value := range input.Credentials {
		credentials[key] = value
	}
	credentials[CredKeyReclaudeGateway] = validated.NormalizedGateway

	// 合成 account_uuid：身份统一用的稳定值，**一旦生成终身不变**。
	// reclaude 不给我们 Anthropic 的 account_uuid，且底层账号会被静默换掉，
	// 根本不存在一个稳定的真实值；换号时更换它，上游会看到「一台设备突然换了人」。
	if credentialString(credentials, CredKeyReclaudeSyntheticAccountUUID) == "" {
		credentials[CredKeyReclaudeSyntheticAccountUUID] = uuid.NewString()
	}

	encrypted, err := cipher.EncryptForStorage(credentials)
	if err != nil {
		return prepared, err
	}

	prepared.Name = BuildReclaudeAccountName(validationInput.DeviceHostname, validationInput.DeviceID)
	prepared.Credentials = encrypted
	prepared.Extra = prepareReclaudeExtra(extra, credentialString(credentials, CredKeyReclaudeTimezone), validated.DailyLimitUSD)
	prepared.Warnings = validated.Warnings
	return prepared, nil
}

// prepareReclaudeExtra 补齐账号级的运行态配置，返回新 map，不修改入参。
//
// 目前只有「模拟关机」作息：不在建号时生成的话，这台设备就是 24h 不休 ——
// 而「从不关机」正是这套机制要消除的特征。已配置则保留，不覆盖运维的手工调整。
func prepareReclaudeExtra(extra map[string]any, timezone string, dailyLimitUSD float64) map[string]any {
	out := make(map[string]any, len(extra)+2)
	for key, value := range extra {
		out[key] = value
	}
	// 档位换算出的美元日限额直接落库，由既有的 IsQuotaExceeded 执行。
	// 不保留运维手改的值：档位是限额的唯一入口。
	out["quota_daily_limit"] = dailyLimitUSD
	if _, ok := out[ExtraKeyReclaudeOfflineWindow]; !ok {
		out[ExtraKeyReclaudeOfflineWindow] = GenerateReclaudeOfflineWindow(timezone)
	}
	return out
}

func credentialString(source map[string]any, key string) string {
	if source == nil {
		return ""
	}
	value, _ := source[key].(string)
	return value
}

// credentialInt64 兼容 JSON 反序列化出来的 float64 与直接写入的整型。
func credentialInt64(source map[string]any, key string) int64 {
	if source == nil {
		return 0
	}
	switch value := source[key].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}
