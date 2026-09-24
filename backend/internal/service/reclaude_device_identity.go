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
	// 🔴 2026-09-25 撤销复盘定位的根因：event_logging 每个事件的 env 块带
	// kernel / arch / node_version / distro，而登录时 /api/cli/auth/start
	// 已把这台机器的真机指纹上报给服务端并落库。两者必须**同源一致** ——
	// 我们此前硬编码抄样本值（arm64 / v26.3.0），与服务器真机（x86_64 /
	// v18.19.1 / kernel 6.17-aws）对不上：同一 device_id 自报了两副矛盾硬件，
	// 服务端一致性校验一对账就撤销。
	//
	// 建号时从登录机器采集（uname -m / uname -r / os-release / node -v），
	// 存这里；event_logging 按账号读，绝不再硬编码。
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
