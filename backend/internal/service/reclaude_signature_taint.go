package service

import "time"

// ReclaudeSignatureTaintWindow 是换号之后「自动剥离历史 thinking 签名」的窗口。
//
// 取 2 小时以对齐 ccPrevReqTTL：会话态本来就只活这么久，窗口比它长没有意义。
//
// 窗口只决定**自动检测能覆盖多久**，不是正确性边界：窗口内出现过的会话会被
// 永久打上污染标记（markSessionSignatureTainted），窗口外才第一次出现的会话
// 退回既有的「一次 400 再学会」路径。缩短窗口的代价是多几次 400，
// 放长窗口的代价是长期无谓剥离、把 prompt 缓存一直打冷。
const ReclaudeSignatureTaintWindow = 2 * time.Hour

// ReclaudeSignatureTaintedAfterSwitch 判断该 reclaude 账号是否刚换过底层账号，
// 以至于客户端手里的历史 thinking 签名必然会被上游拒绝。
//
// 🔴 为什么需要这一条：既有的判据是 `signatureOwnerAccountID != selectedAccountID`，
// 而 reclaude 换号时 **account.ID 不变** ⇒ 判定成「没换号」⇒ 不剥离 ⇒
// 历史签名照发，新账号必拒（代码注释记录的实测量级：约 595 次/天）。
// 身份纪元解决了 prev_request_id 那一半，签名污染标记按 (groupID, sessionHash)
// 存、换号时无法枚举，只能靠账号上的「最近换号时间」反推。
//
// ⚠️ 非 reclaude 账号必须恒为 false：对全池账号开这个开关等于把所有会话的
// prompt 缓存打冷。
func ReclaudeSignatureTaintedAfterSwitch(account *Account, now time.Time) bool {
	if account == nil || !account.IsReclaude() {
		return false
	}

	raw, _ := account.Extra[ExtraKeyReclaudeLastSwitchedAt].(string)
	if raw == "" {
		return false
	}

	switchedAt, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		// 解析不出来就当没换过：宁可多一次 400 重试，也不要让所有请求白白剥离。
		return false
	}

	return now.Sub(switchedAt) < ReclaudeSignatureTaintWindow
}
