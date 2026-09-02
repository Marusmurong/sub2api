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

// extractFirstUserText 提取 messages 中第一条 user 消息的首段 text 内容。
// 兼容 string 和 []block 两种 content 格式。
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
			first = content.String()
			return false
		}
		if content.IsArray() {
			content.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "text" {
					first = block.Get("text").String()
					return false
				}
				return true
			})
			return false
		}
		return false
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
//  2026-09-02 从 2.1.257 原生二进制抽取的构造函数原文：
//    C = s==="firstParty" && ii() || s==="vertex" ? " cch=00000;" : ""
//  字段一直都在，只是值固定为常量 00000。）
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
