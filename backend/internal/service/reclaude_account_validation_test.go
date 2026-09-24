package service

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func validSeedHex() string {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	return hex.EncodeToString(seed)
}

func validReclaudeInput() ReclaudeAccountInput {
	proxyID := int64(7)
	return ReclaudeAccountInput{
		ProxyID:        &proxyID,
		SK:             "sk-rec-abcdef123456",
		SeedEncoded:    validSeedHex(),
		DeviceID:       43448,
		Fingerprint:    "5c5349acd61f1df2",
		GatewayURL:     "https://la.route.reclaude.ai",
		ClientVersion:  "v1.4.0",
		ClientPlatform: "linux/amd64",
		PlanTier:       "20x",
		ClaudeUserID:   "c8f2a1b09d3e4f5a6b7c8d9e0f1a2b3c4d5e6f708192a3b4c5d6e7f809a1b2c3",
		AccountUUID:    "9c67eb02-4001-4cde-a6e2-e40f1a71649e",
		DeviceHostname: "mbp-dev",
	}
}

func TestValidateReclaudeAccountInput(t *testing.T) {
	t.Run("合法输入通过", func(t *testing.T) {
		result, err := ValidateReclaudeAccountInput(validReclaudeInput())

		require.NoError(t, err)
		require.Empty(t, result.Warnings)
	})

	// V-1：无代理 = 直接用机房 IP。出口 IP 稳定性是隐蔽性参数之一。
	t.Run("V-1 代理为空拒绝创建", func(t *testing.T) {
		input := validReclaudeInput()
		input.ProxyID = nil

		_, err := ValidateReclaudeAccountInput(input)

		require.ErrorIs(t, err, ErrReclaudeProxyRequired)
	})

	// V-2：长度不对 = 从 keychain 抠错了东西，签名必然全挂。
	t.Run("V-2 seed 解码后必须恰好 32 字节", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			seed string
		}{
			{"空", ""},
			{"31 字节", hex.EncodeToString(make([]byte, 31))},
			{"33 字节", hex.EncodeToString(make([]byte, 33))},
			{"64 字节（把私钥当成了 seed）", hex.EncodeToString(make([]byte, 64))},
			{"非法编码", "zzzz"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				input := validReclaudeInput()
				input.SeedEncoded = tc.seed

				_, err := ValidateReclaudeAccountInput(input)

				require.ErrorIs(t, err, ErrReclaudeSeedInvalid)
			})
		}
	})

	t.Run("V-2 seed 也接受 base64 编码", func(t *testing.T) {
		input := validReclaudeInput()
		input.SeedEncoded = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA="

		result, err := ValidateReclaudeAccountInput(input)

		require.NoError(t, err)
		require.Len(t, result.Seed, 32)
	})

	// V-3：推导算法未知（K-13），所以只能**警告**，不能拒绝 ——
	// 否则会卡在一个今天测不出来的校验上。
	t.Run("V-3 指纹格式不对只警告不拒绝", func(t *testing.T) {
		input := validReclaudeInput()
		input.Fingerprint = "not-hex"

		result, err := ValidateReclaudeAccountInput(input)

		require.NoError(t, err, "K-13 结案前本条不得拒绝创建")
		require.NotEmpty(t, result.Warnings)
		require.Contains(t, strings.Join(result.Warnings, " "), "fingerprint")
	})

	t.Run("V-3 指纹为空同样只警告", func(t *testing.T) {
		input := validReclaudeInput()
		input.Fingerprint = ""

		result, err := ValidateReclaudeAccountInput(input)

		require.NoError(t, err)
		require.NotEmpty(t, result.Warnings)
	})

	t.Run("device_id 必须为正", func(t *testing.T) {
		for _, deviceID := range []int64{0, -1} {
			input := validReclaudeInput()
			input.DeviceID = deviceID

			_, err := ValidateReclaudeAccountInput(input)

			require.ErrorIs(t, err, ErrReclaudeDeviceIDInvalid)
		}
	})

	t.Run("sk 必须带 sk-rec- 前缀", func(t *testing.T) {
		input := validReclaudeInput()
		input.SK = "sk-ant-oops"

		_, err := ValidateReclaudeAccountInput(input)

		require.ErrorIs(t, err, ErrReclaudeSKInvalid)
	})

	// V-5：包络未标定就售卖 = 超卖。这个模型里超卖是庞氏，不是运营弹性。
	t.Run("V-5 必须选定套餐档位", func(t *testing.T) {
		input := validReclaudeInput()
		input.PlanTier = ""

		_, err := ValidateReclaudeAccountInput(input)

		require.ErrorIs(t, err, ErrReclaudePlanTierRequired)
	})

	// V-9：默认配置下 SSRF 校验不生效，节点硬白名单是唯一的防线。
	t.Run("V-9 网关地址必须是已知 route 节点", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			url  string
		}{
			{"http 明文", "http://la.route.reclaude.ai"},
			{"任意外部域名", "https://evil.example.com"},
			{"内网地址", "https://127.0.0.1:8080"},
			{"域名后缀伪装", "https://la.route.reclaude.ai.evil.com"},
			{"空", ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				input := validReclaudeInput()
				input.GatewayURL = tc.url

				_, err := ValidateReclaudeAccountInput(input)

				require.ErrorIs(t, err, ErrReclaudeGatewayNotAllowed)
			})
		}
	})

	t.Run("V-9 四个已知节点全部放行", func(t *testing.T) {
		for _, host := range ReclaudeAllowedGatewayHosts {
			input := validReclaudeInput()
			input.GatewayURL = "https://" + host

			_, err := ValidateReclaudeAccountInput(input)

			require.NoErrorf(t, err, "%s 应当被放行", host)
		}
	})

	t.Run("客户端版本与平台必填", func(t *testing.T) {
		input := validReclaudeInput()
		input.ClientVersion = ""
		_, err := ValidateReclaudeAccountInput(input)
		require.Error(t, err)

		input = validReclaudeInput()
		input.ClientPlatform = ""
		_, err = ValidateReclaudeAccountInput(input)
		require.Error(t, err)
	})
}

// 账号名建号时自动生成、不可编辑：rec/ 前缀让号池列表一眼分得出来，
// hostname 能与 rec 设备页面直接对账，device_id 后缀避免同人设下撞名。
func TestBuildReclaudeAccountName(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		deviceID int64
		want     string
	}{
		{"常规", "mbp-dev", 43448, "rec/mbp-dev-3448"},
		{"device_id 不足四位", "mbp-dev", 42, "rec/mbp-dev-42"},
		{"hostname 含大写与空格", "  MBP Dev  ", 43448, "rec/mbp-dev-3448"},
		{"hostname 为空时退化为 device", "", 43448, "rec/device-3448"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, BuildReclaudeAccountName(tt.hostname, tt.deviceID))
		})
	}
}

// V-6：既有的 checkMixedChannelRisk 比的是 platform，而 rec 的 platform 仍是
// anthropic ⇒ 现状不会触发任何告警。必须按 Type 判定。
func TestReclaudeGroupIsolation(t *testing.T) {
	reclaude := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeReclaude}
	selfHosted := &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	t.Run("rec 分组混入自建号被拒绝", func(t *testing.T) {
		err := CheckReclaudeGroupIsolation(reclaude, []*Account{selfHosted})

		require.ErrorIs(t, err, ErrReclaudeGroupMixed)
	})

	t.Run("自建号分组混入 rec 账号被拒绝", func(t *testing.T) {
		err := CheckReclaudeGroupIsolation(selfHosted, []*Account{reclaude})

		require.ErrorIs(t, err, ErrReclaudeGroupMixed)
	})

	t.Run("同类账号放行", func(t *testing.T) {
		require.NoError(t, CheckReclaudeGroupIsolation(reclaude, []*Account{
			{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeReclaude},
		}))
		require.NoError(t, CheckReclaudeGroupIsolation(selfHosted, []*Account{
			{ID: 4, Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
		}))
	})

	t.Run("空分组放行", func(t *testing.T) {
		require.NoError(t, CheckReclaudeGroupIsolation(reclaude, nil))
	})

	// 账号自己已经在组里时不应把自己算成冲突（编辑既有账号的场景）。
	t.Run("忽略自身", func(t *testing.T) {
		require.NoError(t, CheckReclaudeGroupIsolation(reclaude, []*Account{reclaude}))
	})
}

// gateway_url 白名单是把全量明文 prompt 发往何处的唯一防线。
//
// ⚠️ 这个测试此前叫 ...IncludesPrimaryDomain 并断言「主域必须被接受」。
// 那个结论已于 2026-09-24 被推翻：www.reclaude.ai 是真客户端 auto-pick
// 全部失败时的兜底值，不是候选节点。候选表本身由
// TestReclaudeGatewayCandidates 锁定，这里只保留 SSRF 防线一条。
func TestReclaudeGatewayAllowlistRejectsUnknownHosts(t *testing.T) {
	t.Run("白名单之外仍然拒绝", func(t *testing.T) {
		// 这条是防线本身：gateway_url 决定我们把全量明文 prompt 发到哪台机器。
		input := validReclaudeInput()
		input.GatewayURL = "https://evil.example.com"

		_, err := ValidateReclaudeAccountInput(input)

		require.ErrorIs(t, err, ErrReclaudeGatewayNotAllowed)
	})

	t.Run("相似域名不被误放行", func(t *testing.T) {
		for _, bad := range []string{
			"https://www.reclaude.ai.evil.com",
			"https://notwww.reclaude.ai",
			"https://reclaude.ai.attacker.net",
		} {
			input := validReclaudeInput()
			input.GatewayURL = bad

			_, err := ValidateReclaudeAccountInput(input)

			require.Errorf(t, err, "不该放行 %s", bad)
		}
	})
}
