package service

import "bytes"

// stripThinkingForAccountSwitch 在会话被调度到**另一个账号**时，前置剥离历史
// thinking 块。
//
// 问题（生产 2026-07-28 实测每天约 595 次）：
//
//	messages.N.content.M: Invalid `signature` in `thinking` block
//
// thinking 块的 signature 是**账号/组织级签发**的。粘性会话因账号 429 冷却、
// 窗口费用驱逐等原因失效后，本轮会落到另一个账号，上一轮的签名必然被拒。
//
// 与 gateway_forward.go 中已有的 FilterThinkingBlocks 互补：
//
//	FilterThinkingBlocks            → 处理 "signature 字段缺失"（结构性问题）
//	stripThinkingForAccountSwitch   → 处理 "signature 有效但属于另一个账号"
//
// 为什么只在账号切换时剥离，而不是无条件前置剥离：
//
//   - prompt 缓存是账号/组织级的。换账号时缓存**本就是冷的**，此时剥离零损失；
//     而同账号路径若也剥离，会打掉前缀命中，推高 token 消耗，反过来加剧 429。
//   - 现状是"发出 → 400 → 剥离 → 重试"，等于把 595 次变成 1190 次上游请求，
//     并且每天主动发出 595 次**已知非法**的请求 —— 这本身就是账号异常信号，
//     在关联封禁的语境下代价很高。
//
// 剥离动作复用 FilterThinkingBlocksForRetry（400 重试路径用的同一套变换），
// 其内部按 ShouldApplyRetryFilters 把关：passback-required 上游
// （DeepSeek/Kimi/GLM 等要求历史 thinking 原样回传）不会被剥离。
//
// 返回 (body, applied)：applied 为 false 时 body 原样返回。
func stripThinkingForAccountSwitch(body []byte, mappedModel string, boundAccountID, selectedAccountID int64) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}
	if !isCrossAccountSessionReuse(boundAccountID, selectedAccountID) {
		return body, false
	}
	filtered := FilterThinkingBlocksForRetry(body, mappedModel)
	if bytes.Equal(filtered, body) {
		return body, false
	}
	return filtered, true
}

// isCrossAccountSessionReuse 判断本轮是否把一个已绑定账号的会话调度到了别的账号。
// boundAccountID <= 0 表示没有历史绑定（首次请求或粘性已过期），此时无跨账号签名问题。
func isCrossAccountSessionReuse(boundAccountID, selectedAccountID int64) bool {
	return boundAccountID > 0 && selectedAccountID > 0 && boundAccountID != selectedAccountID
}

// resolveSignatureOwnerAccountID 回答"本轮请求里的历史 thinking 签名是谁签发的"。
//
// 有两个来源，粘性绑定优先：
//
//	sticky   —— 仍然活着的粘性绑定，是当前事实
//	recorded —— 签名归属记录（sig_owner），绑定被清理后仍然保留
//
// 为什么必须有第二个来源：粘性绑定恰恰是在账号 429、被驱逐、变为不可调度时由
// shouldClearStickySession → DeleteSessionAccountID 删掉的，而这正是下一轮必然换号、
// 也就是最需要知道"上一轮是谁签的"的时刻。只看粘性绑定，等于在唯一用得上它的场景里
// 把这条信息扔了。
//
// 生产数据（2026-07-31 单日）：前置剥离命中 166 次，另有 175 次仍然发到上游才拿到
// 400，其中 16 次连重试都没救回来直接漏给客户端——那 175 次只可能是绑定已不存在。
//
// 两个来源都不采信负值：它们来自 Redis，解析异常时不应被当作账号 ID 参与剥离判定。
func resolveSignatureOwnerAccountID(sticky, recorded int64) int64 {
	if sticky > 0 {
		return sticky
	}
	if recorded > 0 {
		return recorded
	}
	return 0
}
