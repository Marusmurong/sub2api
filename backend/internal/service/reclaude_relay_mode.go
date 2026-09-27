package service

import "strings"

// ExtraKeyReclaudeRelayLifecycle 打开该账号的「忠实中继」模式。
//
// 背景（2026-09-27 定位）：真 reclaude 客户端跑的是一个**真 Claude Code**,
// 由 MITM 拦截它的全部 api.anthropic.com 流量(messages + event_logging +
// bootstrap + mcp_servers)转发给 rec 网关。sub 默认是「处理一条 /v1/messages
// + 自己合成一套薄生命周期」,合成出来的会话形态比真客户端薄得多,是撤销的最强
// 候选根因(见 docs/RECLAUDE_REVOCATION_VERIFY_PLAN_2026-09-26.md §3.5 / §9.2)。
//
// 打开后:OnInference **不再合成**引导/遥测,改由下游真 claude 自己发来的
// bootstrap / event_logging / mcp_servers 经中继路由原样转发到 la.route
// (见 reclaude 中继 handler)。
//
// 🔴 只在「下游确实是真 Claude Code」时开;对普通 API 下游开=既不合成、下游又
// 不发生命周期,会话反而更薄。默认关,不影响存量账号。这是真机对照实验(E1)的开关。
const ExtraKeyReclaudeRelayLifecycle = "reclaude_relay_lifecycle"

// ReclaudeRelayLifecycleEnabled 判断该账号是否开启忠实中继模式。
//
// 取值 "1" / "true" / "yes" / "on"(大小写不敏感)为开,其余(含缺省)为关。
func ReclaudeRelayLifecycleEnabled(account *Account) bool {
	if account == nil || !account.IsReclaude() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(credentialString(account.Extra, ExtraKeyReclaudeRelayLifecycle))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
