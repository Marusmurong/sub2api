package service

import (
	"regexp"
	"strings"
)

const (
	// CredKeyReclaudeClaudeUserID 是**真实客户端**的 Claude Code userID
	// （login 那台机器上 ~/.claude.json 的 userID，64 位 hex）。
	//
	// 🔴 它决定信封内层 body 的 metadata.user_id.device_id —— rec 服务端按这个
	// 值识别设备。报错值会被判
	// `bad_envelope / reclaude state mismatch, please restart reclaude`
	// （2026-09-24 定位：抓真实客户端报文后发现我们报的是自己生成的 ClientID）。
	//
	// ⚠️ 与 CredKeyReclaudeDeviceID 同名不同物：那个是**设备号**（如 44186），
	// 发在 X-Reclaude-Device-Id 头上；这个是 Claude Code 的 userID，发在 body 里。
	CredKeyReclaudeClaudeUserID = "reclaude_claude_user_id"

	// CredKeyReclaudeAccountUUID 是 login 时 /api/cli/auth/poll 返回的 AccountUUID。
	//
	// 早期实现以为「reclaude 不给我们这个值」而一律用合成 UUID，那是错的 ——
	// 逆向报告 §9 的 pollResponse 里就有 AccountUUID，真实信封里也是这个真值。
	CredKeyReclaudeAccountUUID = "reclaude_account_uuid"

	// CredKeyReclaudeOrganizationUUID 是 Claude 组织 UUID。
	//
	// ✅ 真值：event_logging 每个事件的 `auth.organization_uuid`，
	// 以及 mcp-registry 请求的 `x-organization-uuid` 头。
	//
	// 🔴 它**只出现在客户端自己产生的请求里**，网关从不下发 ——
	// 只能在建号时从真机采集（`~/.claude.json` 的 oauthAccount）。
	// 缺失时**不发遥测事件**，绝不编造：org UUID 与账号的归属关系
	// 对端一查就知道，编一个等于主动提供一条矛盾证据。
	CredKeyReclaudeOrganizationUUID = "reclaude_organization_uuid"

	// CredKeyReclaudeClaudeEmail 是**底层 Claude 账号**的邮箱
	// （真机 ~/.claude.json 的 oauthAccount.emailAddress）。
	//
	// 🔴 与 CredKeyReclaudeUserEmail / ExtraKeyReclaudeBoundEmail 都不同：
	//   - 这个   —— 这台设备当前挂在哪个 Claude 账号上（运营要看的）
	//   - user   —— 建号时填的 reclaude 订阅账号
	//   - bound  —— 心跳从 /client/account 读到的值。✅ 2026-09-25 实测它是
	//               **订阅账户邮箱**，一个订阅下所有设备相同，零区分度。
	CredKeyReclaudeClaudeEmail = "reclaude_claude_email"

	// CredKeyReclaudeMachineEnv 是**登录机器的真机指纹快照**（JSON）。
	//
	// 🔴 2026-09-25 撤销复盘定位的根因：event_logging 的 env 块与内层
	// x-stainless-* 头都带机器指纹，而登录 /api/cli/auth/start 已把真机指纹
	// 上报落库。两者必须**同源一致**，否则同一 device_id 自报两副矛盾硬件，
	// 服务端对账即撤销。
	//
	// 字段来源分三类，**不要混**（各有正确来源，混了就是新的不一致）：
	//   - arch / linux_distro_id / linux_distro_version / linux_kernel / shell
	//     → 跟**登录机器真机**走（uname -m / os-release / uname -r）。
	//   - node_version → 🔴 是 **claude-cli 二进制内嵌的运行时版本**
	//     （strings ~/.reclaude/claude-cli/claude 抠出的 node/vXX，实测 v26.3.0），
	//     **不是系统 node -v**（服务器系统 node 是 v18.19.1，绝不能填它）。
	//     x-stainless-runtime-version 与 event_logging env.node_version 都用它。
	//   - cli_version → claude-cli 版本（metadata.json，实测 2.1.280），
	//     进 env.version / UA 版本段。
	//
	// event_logging 与内层 stainless 头都按账号读它，绝不再硬编码。
	CredKeyReclaudeMachineEnv = "reclaude_machine_env"
)

// claudeUserIDPattern 是 Claude Code userID 的形态：sha256 的 64 位小写 hex。
//
// 形态校验不是洁癖：填错字段（比如误把设备号 44186 填进来）会稳定失败，
// 而失败信息是对方那句「请重启 reclaude」，完全看不出是填错了。
var claudeUserIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ReclaudeMetadataDeviceID 返回该账号在 metadata.user_id 里该用的 device_id。
//
// 未配置或形态不对时返回空串 —— 调用方据此回落到既有的 ClientID 逻辑。
// 🔴 不能瞎编一个：编出来的值必然与服务端记录不符，只会稳定失败。
func ReclaudeMetadataDeviceID(account *Account) string {
	if account == nil || !account.IsReclaude() {
		return ""
	}
	value := strings.TrimSpace(account.GetCredential(CredKeyReclaudeClaudeUserID))
	if !claudeUserIDPattern.MatchString(value) {
		return ""
	}
	return value
}
