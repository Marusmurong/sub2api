package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// fingerprintSalt 是计算 cc_version 后缀指纹的盐值。
//
// 来源：与 Parrot src/transform/cc_mimicry.py 的 FINGERPRINT_SALT 完全一致；
// 这是真实 Claude Code CLI 抓包推导出的常量，改动会导致 fp 与 CLI 不一致，
// 进一步触发 Anthropic 的第三方检测。
const fingerprintSalt = "59cf53e54c78"

// fingerprintCharIndices 是真实 CLI 的取样位置。语义是 JS 字符串下标，
// 即 UTF-16 code unit，见 fingerprintSampleChars。
var fingerprintCharIndices = []int{4, 7, 20}

// fingerprintSampleChars 按真实 CLI 的语义在 text 上取样。
//
// 真实 CLI 是 JS，text[i] 索引的是 **UTF-16 code unit**——既不是字节，也不是码点：
//
//	ASCII        三者一致
//	中文         一个字符 3 字节 / 1 个 code unit；按字节取会落在 UTF-8 续字节上，
//	             取到的是半个字的碎片而非字符
//	星外字符     emoji 等占 1 个码点 / 2 个 code unit；按码点取又会与 CLI 错位
//
// 因此只有 UTF-16 与 CLI 对齐。越界位置补 '0'，与 CLI 一致。
func fingerprintSampleChars(text string) string {
	units := utf16.Encode([]rune(text))
	var b strings.Builder
	for _, i := range fingerprintCharIndices {
		if i >= len(units) {
			b.WriteByte('0')
			continue
		}
		b.WriteRune(decodeUTF16Unit(units[i]))
	}
	return b.String()
}

// decodeUTF16Unit 还原单个 UTF-16 code unit 对应的字符。
//
// 取样点落在代理对的任一半时返回 U+FFFD：JS 的 text[i] 拿到的正是一个孤立代理项，
// 而孤立代理项在 UTF-8 编码时就是替换字符——哈希的输入因此是 U+FFFD 而非原字符。
func decodeUTF16Unit(u uint16) rune {
	if u >= 0xD800 && u <= 0xDFFF {
		return utf8.RuneError
	}
	return rune(u)
}

// computeClaudeCodeFingerprint 复刻真实 Claude Code CLI 的 cc_version 指纹算法：
//
//  1. 取 messages 中第一条 role=user 的纯文本（首块 text）
//  2. 取该文本的第 4、7、20 字符（不足以 '0' 补齐，见 fingerprintSampleChars）
//  3. SHA256(SALT + chars + cc_version) 取 hex 前 3 字符
//
// 算法来自 Parrot src/transform/cc_mimicry.py:compute_fingerprint，与官方 CLI 字节对齐。
// 任何偏差都会导致 cc_version=X.Y.Z.{fp} 在上游侧与真实 CLI 不一致——而上游持有
// messages 与 cc_version，可以自行复算比对，这是一条确定性校验而非概率特征。
func computeClaudeCodeFingerprint(body []byte, version string) string {
	chars := fingerprintSampleChars(extractFirstUserText(body))
	sum := sha256.Sum256([]byte(fingerprintSalt + chars + version))
	return hex.EncodeToString(sum[:])[:3]
}

// metaUserTextPrefixes 是 CLI 内部 isMeta=true 的 user 消息在线上形态里的开头标记。
//
// 2.1.263 二进制里 isMeta:!0 出现的上下文只有这几种标签：CLAUDE.md / hooks /
// 记忆等上下文注入（system-reminder）、斜杠命令展开（command-message /
// command-name）、本地命令输出（local-command-stdout）。真实用户输入不会以
// 这些标签开头，所以按前缀判定不会误伤。
var metaUserTextPrefixes = []string{
	"<system-reminder>",
	"<command-message>",
	"<command-name>",
	"<local-command-stdout>",
	"<local-command-caveat>",
}

func isMetaUserText(text string) bool {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	for _, p := range metaUserTextPrefixes {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	return false
}

// extractFirstUserText 提取真实 CLI 用于 cc_version 指纹取样的那段文本。
//
// 真实 CLI（2.1.263 原文）：
//
//	e.find(o => o.type==="user" && !o.isMeta)   // 第一条非 meta 的 user 消息
//	→ content 为 string 直接用；为数组取首个 text 块
//
// CLI 的 meta 消息（system-reminder 等）在发到线上时会与紧随其后的真实用户
// 输入合并成同一条 user 消息、排在前面的 text 块。所以线上等价语义是：
// 按顺序扫 user 消息，跳过 meta 开头的 text 块，取到的第一个非 meta text 块
// 就是取样文本；某条 user 消息里全是 meta 块则继续看下一条。
//
// 但 find 一旦命中一条非 meta 消息就不再往后找，哪怕它没有 text 块（比如
// 只有 tool_result）——那时 CLI 返回空串。线上对应：跳过 meta 块后，若该
// 消息里还有非 meta 的其它块（tool_result / image…）却没有 text，或者内容
// 是空数组，就在这里停下返回空串，不能再去看后面的 user 消息。
//
// 2026-09-07 之前这里取的是「第一条 user 消息的首个 text 块」，凡是带
// CLAUDE.md / rules / hooks 的会话首块都是 <system-reminder>，算出的后缀与
// 真实 CLI 不同；上游持有 messages 与 cc_version 可以确定性复算，这是一条
// 逐请求可查的差异。
func extractFirstUserText(body []byte) string {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return ""
	}
	first := ""
	messages.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "user" {
			return true
		}
		content := msg.Get("content")
		if content.Type == gjson.String {
			if isMetaUserText(content.String()) {
				return true
			}
			first = content.String()
			return false
		}
		if !content.IsArray() {
			return true
		}
		found := false
		sawNonMetaBlock := false
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() != "text" {
				sawNonMetaBlock = true
				return true
			}
			text := block.Get("text").String()
			if isMetaUserText(text) {
				return true
			}
			first = text
			found = true
			return false
		})
		if found {
			return false
		}
		// 非 meta 消息但没有 text 块（tool_result 等）或空数组：find 已命中，停。
		if sawNonMetaBlock || len(content.Array()) == 0 {
			return false
		}
		// 全是 meta 块：继续看下一条 user 消息。
		return true
	})
	return first
}

// buildBillingAttributionText 构造 system 数组的 billing attribution 文本。
//
// 形态对齐真实 Claude Code CLI：
//
//	x-anthropic-billing-header: cc_version=2.1.161.{fp}; cc_entrypoint=cli;
//
// 关于 cch 段：**本函数刻意不注入**，但最终出口是带的。
//
// 它由 gateway_billing_header.go 的 ensureBillingHeaderCCH 在转发前统一补齐，
// 这样 mimic 与透传两条路径共用同一处收口，不会各写一份而漂移。
// 在这里看不到 cch 不等于我们不发——查 cch 是否在位请看
// billingHeaderCCHSegment 与 ensureBillingHeaderCCH，不要 grep 本文件。
//
// （曾有注释断言「新版 CLI 已不再发送 cch（issue #3358）」，那是误读。
//
//	2026-09-02 从 2.1.257 原生二进制抽取的构造函数原文：
//	  C = s==="firstParty" && ii() || s==="vertex" ? " cch=00000;" : ""
//	字段一直都在，只是值固定为常量 00000。）
//
// 此 block 不带 cache_control（与真实 CLI 一致；cache breakpoint 由后续的
// Claude Code prompt block 承担）。
func buildBillingAttributionText(body []byte, cliVersion string) (string, error) {
	if cliVersion == "" {
		return "", fmt.Errorf("cliVersion required")
	}
	fp := computeClaudeCodeFingerprint(body, cliVersion)
	return fmt.Sprintf(
		"x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=cli;",
		cliVersion, fp,
	), nil
}
