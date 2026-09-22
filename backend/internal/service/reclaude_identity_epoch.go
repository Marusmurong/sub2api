package service

import "strconv"

// ExtraKeyReclaudeIdentityEpoch 是账号的「身份纪元」。
//
// 每次 reclaude 静默换号 +1。它存在的理由是一个很容易看反的事实：
//
//	sub2api 里几乎所有会话态（prev_request_id、thinking 签名污染标记）的 key
//	都以 account.ID 为轴，而 **reclaude 换号时 account.ID 不变** ——
//	那层「换号即 miss」的保护对这类账号整个失效。
//
// 不这么做的后果是两条都会被上游证伪：
//   - 把【旧上游账号】的 request id 注入 cc_prev_req = 一条假 parent-link，
//     比缺字段更糟
//   - 历史 thinking 签名是旧账号签的，新账号必拒
//
// 选「翻页」而不是「遍历删除」：不需要 SCAN/DEL（在 Redis 上是危险操作），
// 旧键靠自身 TTL 自然过期，且推进纪元是一次原子的字段写。
const ExtraKeyReclaudeIdentityEpoch = "reclaude_identity_epoch"

// ReclaudeIdentityEpoch 读取账号当前的身份纪元；非 reclaude 账号恒为 0。
func ReclaudeIdentityEpoch(account *Account) int64 {
	if account == nil || !account.IsReclaude() {
		return 0
	}
	return credentialInt64(account.Extra, ExtraKeyReclaudeIdentityEpoch)
}

// NextReclaudeIdentityEpoch 返回换号后应当写入的纪元值。
func NextReclaudeIdentityEpoch(account *Account) int64 {
	return ReclaudeIdentityEpoch(account) + 1
}

// NamespaceSessionByIdentityEpoch 把会话标识按身份纪元分命名空间。
//
// ⚠️ 非 reclaude 账号**必须原样返回**：改了会让全池账号的 prev_request_id
// 与签名态在部署那一刻集体失效 —— 那本身就是一个批量特征。
func NamespaceSessionByIdentityEpoch(account *Account, sessionID string) string {
	if sessionID == "" || account == nil || !account.IsReclaude() {
		return sessionID
	}
	return strconv.FormatInt(ReclaudeIdentityEpoch(account), 10) + ":" + sessionID
}
