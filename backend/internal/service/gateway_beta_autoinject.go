package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

// 本文件补齐 Anthropic 直连路径上"body 用了某能力、但 anthropic-beta header 没带
// 对应 token"导致的 400。Bedrock 路径早有同类实现（autoInjectBedrockBetaTokens），
// 直连路径此前缺失，表现为：
//
//	tools.0: Input tag 'computer_20250124' found using 'type' does not match
//	any of the expected tags: 'bash_20250124', ...
//
// 上游的 schema 里根本不存在 computer_* 这个 tag —— 只有带上 computer-use beta
// 才会出现。这类请求**不该拦截**，补齐 header 之后它就是合法请求。

// computerUseToolTypePrefix 是 Anthropic computer-use 工具类型的前缀，
// 完整形态形如 computer_20250124。
const computerUseToolTypePrefix = "computer_"

// computerUseBetaTokenForToolType 由 computer 工具类型推导出所需的 beta token。
//
//	computer_20250124 → computer-use-2025-01-24
//
// 与 Bedrock 路径写死 computer-use-2025-11-24 不同：直连 API 的 beta token 与
// 工具版本一一对应，写死会让旧版本工具继续 400。非 computer 工具或日期段不合法
// 时返回空串（不猜测）。
func computerUseBetaTokenForToolType(toolType string) string {
	t := strings.ToLower(strings.TrimSpace(toolType))
	date, ok := strings.CutPrefix(t, computerUseToolTypePrefix)
	if !ok {
		return ""
	}
	const dateLen = 8
	if len(date) != dateLen {
		return ""
	}
	for _, r := range date {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return "computer-use-" + date[:4] + "-" + date[4:6] + "-" + date[6:]
}

// requiredBetaTokensForBody 扫描请求体，返回其内容所隐含的必需 beta token。
// 按出现顺序返回且已去重。
func requiredBetaTokensForBody(body []byte) []string {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return nil
	}

	var required []string
	seen := make(map[string]struct{})
	for _, tool := range tools.Array() {
		token := computerUseBetaTokenForToolType(tool.Get("type").String())
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		required = append(required, token)
	}
	return required
}

// injectRequiredBetaTokens 把 body 隐含的必需 beta token 补进 header。
//
// drop 是管理员在「Beta 策略」里配置的过滤集合：被显式过滤掉的 token 不注入，
// 否则会绕过管理员的策略决定。
//
// 返回 (header, changed)：changed 为 false 时 header 原样返回，调用方可据此
// 决定是否要改变 shouldSet 语义。
func injectRequiredBetaTokens(header string, body []byte, drop map[string]struct{}) (string, bool) {
	required := requiredBetaTokensForBody(body)
	if len(required) == 0 {
		return header, false
	}

	existing := make(map[string]struct{})
	for _, part := range strings.Split(header, ",") {
		if p := strings.TrimSpace(part); p != "" {
			existing[p] = struct{}{}
		}
	}

	added := make([]string, 0, len(required))
	for _, token := range required {
		if _, ok := existing[token]; ok {
			continue
		}
		if _, dropped := drop[token]; dropped {
			continue
		}
		existing[token] = struct{}{}
		added = append(added, token)
	}
	if len(added) == 0 {
		return header, false
	}

	if strings.TrimSpace(header) == "" {
		return strings.Join(added, ","), true
	}
	return header + "," + strings.Join(added, ","), true
}
