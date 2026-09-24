package service

import (
	"bytes"
	"net/http"
	"time"
)

// ReclaudeCredentialRevokedReason 是凭据失效时写进 error_message 的前缀。
//
// 冷却理由的另外两个（模拟关机 / 限流）分别定义在 reclaude_offline_window.go
// 与 reclaude_actuator.go —— 三者必须互相可区分：运维看到一台设备停了，
// 第一个问题永远是「是它自己合盖了，还是被限流了，还是节点挂了」。
const ReclaudeCredentialRevokedReason = "reclaude_credential_revoked"

const (
	// ReclaudeGatewayUnavailableCooldown 是节点 5xx 的短冷却。
	//
	// 取值短：节点抖动是常态，冷却太长等于一次抖动废掉一台设备半小时。
	ReclaudeGatewayUnavailableCooldown = 2 * time.Minute

	// ReclaudeMaxCooldown 是 reclaude 账号任何冷却的上限。
	//
	// 🔴 冷却时长来自**底层那个 Claude 账号**的限流窗口，不是我们配额包的。
	// 换号之后它指向一个已经不相关的时间点 —— 不封顶就是一台设备白白躺几个小时，
	// 而这类账号一共没几台。
	ReclaudeMaxCooldown = 30 * time.Minute
)

// ClassifyReclaudeGatewayStatus 把 /proxy 的**外层**状态码映射成账号动作。
//
// 外层非 200 有两类完全不同的含义，绝不能混：这里处理的是「reclaude 网关自己
// 拒绝了请求」，而不是「隧道通了、Anthropic 报错」—— 后者外层恒为 200，
// 错误码在信封里，走既有的 Anthropic 错误处理。
// ReclaudeDeviceRevokedCode 是设备被解绑时对端返回的错误码。
//
// 🔴 它走的是 **400**，不是 401/403。✅ 2026-09-25 生产实测：
//
//	HTTP 400 {"type":"error","error":{"type":"authentication_error",
//	          "code":"device_revoked","message":"此设备已被解绑…"}}
//
// 只按状态码分类会把它落进 default（只告警不停号），于是账号带着一副已经
// 作废的凭据继续被调度 —— 那天它打了 30 多分钟必然失败的请求，
// 每一条都在对端那里留下一次「已撤销设备仍在尝试」的记录。
const ReclaudeDeviceRevokedCode = "device_revoked"

// ClassifyReclaudeGatewayBody 在状态码之外再看错误码。
//
// 优先于 ClassifyReclaudeGatewayStatus 使用：状态码是粗粒度的，
// 而对端把「凭据彻底失效」和「这次请求不合法」都塞在 400 里。
func ClassifyReclaudeGatewayBody(statusCode int, body []byte) ReclaudeAccountActionKind {
	if len(body) > 0 && bytes.Contains(body, []byte(ReclaudeDeviceRevokedCode)) {
		return ReclaudeActionCredentialRevoked
	}
	return ClassifyReclaudeGatewayStatus(statusCode)
}

func ClassifyReclaudeGatewayStatus(statusCode int) ReclaudeAccountActionKind {
	switch {
	case statusCode == http.StatusUnauthorized, statusCode == http.StatusForbidden:
		// SK 被撤销 / 签名不过 / 设备被顶掉。没有自动降级路径，必须停人工介入。
		return ReclaudeActionCredentialRevoked
	case statusCode == http.StatusTooManyRequests:
		return ReclaudeActionCooldown
	case statusCode >= http.StatusInternalServerError:
		return ReclaudeActionGatewayUnavailable
	default:
		// 网关不该回其它 4xx。回了就说明端点或协议变了 —— 与未知事件同一性质：
		// 不静默丢弃，告警，但不动账号状态（状态改错的代价比漏报大）。
		return ReclaudeActionUnknownEvent
	}
}

// CapReclaudeCooldown 对 reclaude 账号的冷却时刻封顶；其它账号原样返回。
//
// ⚠️ 非 reclaude 账号**必须原样返回**：给全池账号的限流冷却加一个上限，
// 等于让所有被真实限流的账号提前恢复调度，会直接打出一轮 429。
func CapReclaudeCooldown(account *Account, resetAt time.Time, now time.Time) time.Time {
	if account == nil || !account.IsReclaude() {
		return resetAt
	}
	ceiling := now.Add(ReclaudeMaxCooldown)
	if resetAt.After(ceiling) {
		return ceiling
	}
	return resetAt
}
