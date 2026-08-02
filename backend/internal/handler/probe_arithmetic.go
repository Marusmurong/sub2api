package handler

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// 算术降智探针的本地拦截。
//
// 生产实测（2026-08-02）：下游中转以约每分钟一次的频率发这类请求，24 小时 826 次
// （api_key 84，claude-haiku），另有 1000+ 次同族形态分散在 key 83 的多个模型上。
// 它们全都走完整条链路——抢用户并发槽、选账号、占账号并发槽、计一次 RPM、真发上游——
// 只为换回十来个 token。累计每天约 1900 次、74 分钟的账号并发槽占用。
//
// 请求形态固定，只有最后一道题随机：
//
//	Calculate and respond with ONLY the number, nothing else.
//
//	Q: 3 + 5 = ?
//	A: 8
//
//	Q: 12 - 7 = ?
//	A: 5
//
//	Q: 48 - 2 = ?     ← 随机，实测还有 12-7 / 24-5 / 43-22
//	A:
//
// 现有三层拦截全都漏掉它：max_tokens 是 50 不是 1；正文没有 SUGGESTION/Warmup
// 关键字；带了标准 Claude Code 的 system 块，被 isTrivialGreetingRequest 的
// 「无实质 system」条件排除；metadata.user_id 含 _session_ 使测活判定走了严格档，
// 而严格档只认纯问候文案。
//
// **必须算出正确答案再返回，不能回固定文案。**
// 对方是在验我们的上游有没有降智：固定的两道例题给格式，最后一道随机题验算力。
// 回问候语或写死的数字会被判定为「这家供应商的模型坏了」，代价远大于那两秒槽位——
// 本来只是浪费资源，那样会变成丢掉整个下游的流量。

// probeArithmeticPrefix 是该探针的固定开场白。
//
// 判定锚在这句话上而不是靠形状（模型/长度/无 tools），是因为形状判定必然误伤：
// 真实用户问一句短问题在形状上与它无法区分。这句话真人不会发。
const probeArithmeticPrefix = "Calculate and respond with ONLY the number, nothing else."

// probeArithmeticMaxTextLen 限制参与正则匹配的文本长度，避免拿正则去扫大 body。
// 实测该探针正文约 120 字节，给到 1KB 已是数倍余量。
const probeArithmeticMaxTextLen = 1024

// arithmeticQuestionPattern 提取形如 `Q: 48 - 2 = ?` 的算式。
// 取最后一个匹配——前面几道是给格式用的例题，已经带了答案。
var arithmeticQuestionPattern = regexp.MustCompile(`Q:\s*(-?\d+)\s*([+\-*])\s*(-?\d+)\s*=\s*\?`)

// detectArithmeticProbe 判断请求是否为算术降智探针，并返回应当回复的答案。
//
// ok 为 false 时调用方必须放行到上游：算不出确定答案（未知运算符、溢出风险、
// 格式不符）时宁可让它真发上游，也不能返回一个可能错误的数字——错误答案与
// 「降智」在对方看来是同一件事。
func detectArithmeticProbe(body []byte) (answer string, ok bool) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return "", false
	}
	// 带工具的是真实会话，探针不会带。
	if tools := gjson.GetBytes(body, "tools"); tools.IsArray() && len(tools.Array()) > 0 {
		return "", false
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return "", false
	}
	arr := messages.Array()
	if len(arr) != 1 || arr[0].Get("role").String() != "user" {
		return "", false
	}

	text, isPlain := plainTextOfMessageContent(arr[0].Get("content"))
	if !isPlain {
		return "", false
	}
	text = strings.TrimSpace(text)
	if len(text) > probeArithmeticMaxTextLen || !strings.HasPrefix(text, probeArithmeticPrefix) {
		return "", false
	}

	matches := arithmeticQuestionPattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return "", false
	}
	last := matches[len(matches)-1]

	left, err := strconv.Atoi(last[1])
	if err != nil {
		return "", false
	}
	right, err := strconv.Atoi(last[3])
	if err != nil {
		return "", false
	}

	var result int
	switch last[2] {
	case "+":
		result = left + right
	case "-":
		result = left - right
	case "*":
		result = left * right
	default:
		// 正则已限定运算符，走到这里说明模式被改过而分支没跟上——放行而不是猜。
		return "", false
	}
	return strconv.Itoa(result), true
}
