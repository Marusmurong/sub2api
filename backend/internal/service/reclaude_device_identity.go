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
