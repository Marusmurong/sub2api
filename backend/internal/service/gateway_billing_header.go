package service

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// billingHeaderPrefix 是 billing attribution block 的固定前缀。
const billingHeaderPrefix = "x-anthropic-billing-header"

// ccVersionInBillingRe matches the semver part of cc_version (X.Y.Z), preserving
// the trailing message-derived suffix (e.g. ".c02") if present.
var ccVersionInBillingRe = regexp.MustCompile(`cc_version=\d+\.\d+\.\d+`)

// ccVersionFingerprintInBillingRe 匹配 cc_version 的完整 "semver.fp" 形态，
// 用于按最终 body 重算 fp（见 normalizeBillingHeaderBlock）。
var ccVersionFingerprintInBillingRe = regexp.MustCompile(`(cc_version=\d+\.\d+\.\d+)\.[0-9a-f]{3}`)

// ccEntrypointSegmentRe 匹配 "cc_entrypoint=<值>;" 整段，用于在其后插入 cch 段。
var ccEntrypointSegmentRe = regexp.MustCompile(`(cc_entrypoint=[^;]*;)`)

// billingHeaderCCHSegment 是真实 CLI 在 first-party 路径上恒定发送的 cch 段。
//
// 真实 2.1.220 的构造（函数 k7n）：
//
//	s = (provider === "firstParty" && Yd()) || provider === "vertex" ? " cch=00000;" : ""
//
// 其中 Yd() 等价于「ANTHROPIC_BASE_URL 未设置或指向 api.anthropic.com」。于是真订阅
// 用户直连时每条请求都带该段，而任何设了自定义 base URL 的客户端一条都不带——某账号
// 的流量里 cch= 出现率为 0，即可推断其客户端并非直连。
//
// 注意该值是**字面常量** 00000，不是签名。此前仓内注释据「值从 d8726 变为 00000」
// 判断字段已被移除，是误读；字段一直都在。详见 docs/CC_2.1.220_EGRESS_SPEC.md §3。
const billingHeaderCCHSegment = " cch=00000;"

// normalizeBillingHeaderBlock 归一化 system 里的 billing attribution block，
// 是转发前对该 block 的唯一收口点：
//
//  1. cc_version 的 semver 段对齐我们实际发送的 User-Agent 版本
//  2. 补齐 cch 段（真实 first-party 客户端恒发，此前 mimic 与透传两条路径都缺）
//  3. recomputeFingerprint 时按最终 body 重算 cc_version 的 fp 后缀
//
// 第 3 步只在 mimic 路径开启。该路径的 block 由我们构造，而 block 构造完成后 body
// 仍会被 dateline 归一化等步骤改写 messages，fp 会与最终发出的 body 不一致；真实 CLI
// 是按最终 messages 算 fp 的（见 EGRESS_SPEC §4.2）。透传路径的 block 由真实客户端
// 生成，其取样口径含 transcript 的 isMeta 过滤，我们无法从 API body 完全复现，重算
// 反而可能引入偏差，故保持客户端原值。
func normalizeBillingHeaderBlock(body []byte, userAgent string, recomputeFingerprint bool) []byte {
	systemResult := gjson.GetBytes(body, "system")
	if !systemResult.Exists() || !systemResult.IsArray() {
		return body
	}

	version := ExtractCLIVersion(userAgent)

	var fingerprint string
	if recomputeFingerprint {
		fingerprint = computeClaudeCodeFingerprint(body, firstNonEmptyFingerprint(version, claude.CLICurrentVersion))
	}

	idx := 0
	systemResult.ForEach(func(_, item gjson.Result) bool {
		text := item.Get("text")
		if !text.Exists() || text.Type != gjson.String || !strings.HasPrefix(text.String(), billingHeaderPrefix) {
			idx++
			return true
		}

		original := text.String()
		next := original
		if version != "" {
			next = ccVersionInBillingRe.ReplaceAllString(next, "cc_version="+version)
		}
		if fingerprint != "" {
			next = ccVersionFingerprintInBillingRe.ReplaceAllString(next, "${1}."+fingerprint)
		}
		next = ensureBillingHeaderCCH(next)

		if next != original {
			if updated, err := sjson.SetBytes(body, fmt.Sprintf("system.%d.text", idx), next); err == nil {
				body = updated
			}
		}
		idx++
		return true
	})

	return body
}

// ensureBillingHeaderCCH 在 cc_entrypoint 段之后补上 cch 段；已存在则原样返回。
//
// 没有 cc_entrypoint 段时不改写：那说明 block 形态不是我们认识的样子，
// 与其凭空造字段，不如保持原样。
func ensureBillingHeaderCCH(text string) string {
	if strings.Contains(text, "cch=") {
		return text
	}
	if !ccEntrypointSegmentRe.MatchString(text) {
		return text
	}
	return ccEntrypointSegmentRe.ReplaceAllString(text, "${1}"+billingHeaderCCHSegment)
}
