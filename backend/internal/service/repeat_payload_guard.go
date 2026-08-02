package service

import (
	"strconv"

	"github.com/cespare/xxhash/v2"
)

// 重复 payload 拦截的指纹计算。
//
// 背景（2026-08-01）：账号 claude-e9b4a11a 在 17:04–17:59 收到 22 次连续请求，
// input_tokens 恒为 266715（一个 token 都不差），output_tokens 却在 12702–19442
// 之间各不相同。真实对话每轮在尾部追加内容、input 必然单调增长（同账号前一小时
// 那段就是 262461→266235 一路爬）；输入冻结而输出各异，只能是同一份固定 payload
// 被反复提交采样。
//
// 该账号未缓存输入占比 87.8%，全站其它账号（含同一个下游 key 碰过的另外 14 个）
// 都在 0.0%–2.1%——固定 payload 每次原价重算，52 次请求吃掉 5 小时窗口内 22.2%
// 的金额。额度封顶拦不住这个动作，只能按行为识别。

// RepeatPayloadScope 区分入口，让不同路径的计数互不污染。
type RepeatPayloadScope string

const (
	// RepeatPayloadScopeMessages 对应 POST /v1/messages。
	RepeatPayloadScopeMessages RepeatPayloadScope = "msg"
	// RepeatPayloadScopeCountTokens 对应 POST /v1/messages/count_tokens。
	//
	// 单独一个命名空间是必须的：Claude Code 会为同一份会话状态合法地重复调用
	// count_tokens，若与 messages 共用计数，正常客户端会把 messages 的额度耗光。
	RepeatPayloadScopeCountTokens RepeatPayloadScope = "ct"
)

// RepeatPayloadFingerprint 由请求的 messages 数组算出指纹，第二个返回值表示是否可用。
//
// 只取 messages，刻意**不含** system：system 里的 `x-anthropic-billing-header:` 块
// 带 cc_prev_req（上游请求 id），每次请求都变，还会被 normalizeBillingHeaderBlock
// 重写——纳入指纹会导致永远不命中，等于没做。
//
// messages 内唯一的易变内容是 <system-reminder> 里的 dateline，按天变而不按请求变，
// 在分钟级窗口内稳定。
//
// 不做 JSON 规范化（不走 canonicalAnthropicDigestJSON 的 Unmarshal→Marshal）：
// 同一个脚本重复发送时字节本来就一致，而规范化要为每个 MB 级请求付一次完整反序列化。
// 代价是「客户端每次以不同 key 顺序重新序列化」可以绕过——Python / Go 的 JSON
// 序列化顺序是确定的，实际中不会自发出现，接受这个缺口。
//
// MessagesRaw 返回的是 body 的零拷贝子切片，这里只读不持有。
func RepeatPayloadFingerprint(parsed *ParsedRequest) (string, bool) {
	if parsed == nil {
		return "", false
	}
	raw := parsed.MessagesRaw()
	if len(raw) == 0 {
		return "", false
	}
	return strconv.FormatUint(xxhash.Sum64(raw), 36), true
}
