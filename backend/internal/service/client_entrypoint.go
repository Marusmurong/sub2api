package service

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/tidwall/gjson"
)

// 出站入口声明（cc_entrypoint + User-Agent 括号后缀）跟随下游真实产品。
//
// 背景：此前所有 OAuth 请求一律声明 `(external, cli)` + `cc_entrypoint=cli`，本意是
// 消除「单个账号 7 分钟内出现 3 种 cc_entrypoint」这种中转特征。但它只统一了**声明**，
// 没有统一**正文**：claude-vscode / agent-sdk 这些客户端同样能通过 claude_code_only
// 校验（它们的 system prompt 就在白名单里），于是它们的 tools 数组与 system 尾块原样
// 上行——头和 billing 块说"官方命令行"，正文却装着 VS Code 扩展的东西，每条都自相矛盾。
//
// 2026-09-10 的实测把方向定了下来：在"统一成 cli"已经生效的前提下，首 7 小时非 CLI
// 入口占比最高的四个号（94% / 79% / 75% / 74%）全部死在 6.2–7.2 小时，而占比 <50%
// 的号活了 32–143 小时。统一并没有在保护。
//
// 所以改为跟随：一台开发机同时装 CLI 和 VS Code 扩展本来就是常见形态，让三者自洽比
// 强行统一更接近真实。**机器身份（OS / arch / runtime / package / device_id / TLS）
// 仍按账号统一**——那才是"同一台机器"的定义，与本文件无关。

// ClientEntrypoint 是一次请求最终要对外声明的入口。零值表示"按 cli 处理"。
type ClientEntrypoint struct {
	Product  string // cli / claude-vscode / sdk-ts / ...
	UASuffix string // "(external, claude-vscode, agent-sdk/0.3.263)"
}

// clientEntrypointProducts 是允许原样声明的产品名。
//
// 名单来自线上 7 天真实流量里出现过的全部取值，不含推测项：发一个真实客户端从未
// 产生过的组合，比统一成 cli 更糟。
//
// 值表示该产品的 UA 后缀是否带 agent-sdk 段：
//   - false：`(external, cli)` / `(external, sdk-cli)` 这类裸形态
//   - true ：`(external, claude-vscode, agent-sdk/0.3.x)`
//
// sdk-cli 两种形态都实测出现过，取裸形态（更常见）。
// sdk-py 刻意不在名单里：它的 agent-sdk 属于 0.2.x 家族，与下面的版本钉法不兼容，
// 遇到时回落 cli。
var clientEntrypointProducts = map[string]bool{
	"cli":               false,
	"sdk-cli":           false,
	"claude-vscode":     true,
	"claude-desktop-3p": true,
	"local-agent":       true,
	"sdk-ts":            true,
}

var clientUASuffixRe = regexp.MustCompile(`(?i)^claude-cli/\d+\.\d+\.\d+\s+\(([^)]*)\)\s*$`)

// ccEntrypointValueRe 从 billing attribution 行里取 cc_entrypoint 的值。
var ccEntrypointValueRe = regexp.MustCompile(`cc_entrypoint=([^;]*);`)

// defaultClientEntrypoint 是回落形态，与改动前的行为逐字相同。
func defaultClientEntrypoint() ClientEntrypoint {
	return ClientEntrypoint{Product: "cli", UASuffix: "(external, cli)"}
}

// reclaudeClientEntrypoint 是 reclaude 账号钉死的入口：**sdk-cli**（headless `claude -p`）形态。
//
// 🔴 2026-09-28 翻回 sdk-cli（依据 8x 存活黄金基准，docs/captures/reclaude-embedded-8x-2026-09-28，
// device 44511，142 条 /proxy 全 200）：内嵌 claude 存活时逐字节发的就是完整 sdk-cli 画像 ——
//   - 内层 UA = `claude-cli/2.1.280 (external, sdk-cli)`（8/8）
//   - billing = `cc_version=…; cc_entrypoint=sdk-cli; …; cc_turn_origin=sdk;`
//   - system[1] = "You are a Claude agent, built on Anthropic's Claude Agent SDK."
//   - bootstrap `?entrypoint=sdk-cli`、event_logging / datadog entrypoint=sdk-cli、is_interactive=false
//
// 之前（163016c61）钉 cli 的前提错了：以为 sub 的 system[1] 硬编码 official CLI 文案"改不动"，
// 只能钉 cli。但真客户端跑的是 `claude -p`（sdk-cli），身份块本来就是 Agent SDK 文案 ——
// 那块"改不动"的硬编码恰恰是必须改的（现由 reclaude 路径注入 claudeAgentSDKSystemPrompt）。
// 钉 cli 时每条推理的 UA/cc_entrypoint/身份块/dispatch-id 都与真客户端不同 → 累计约 7 次撤。
//
// 对照抓包：docs/captures/reclaude-embedded-8x-2026-09-28（存活 sdk-cli 黄金基准）
// + docs/captures/reclaude-proxy-sdkcli-full-2026-09-27（60 条完整 sdk-cli 会话）。
var reclaudeClientEntrypoint = ClientEntrypoint{Product: "sdk-cli", UASuffix: "(external, sdk-cli)"}

// resolveClientEntrypointForAccount 按账号判定入口。
//
// reclaude 账号无视下游：一台 reclaude 设备就是一台跑 `claude -p`（sdk-cli）的机器，
// 下游是交互式 CLI 还是别的产品，对这台设备的画像没有意义（E2-L1）。其它账号照旧跟随下游。
func resolveClientEntrypointForAccount(account *Account, clientUA string, body []byte) ClientEntrypoint {
	if account != nil && account.IsReclaude() {
		return reclaudeClientEntrypoint
	}
	return ResolveClientEntrypoint(clientUA, body)
}

// ResolveClientEntrypoint 判定本次请求该声明哪个入口。
//
// 安全性质：**只有下游 UA 后缀解析出的产品名，与下游自己 billing 块里的
// cc_entrypoint 完全一致时，才采用它**；两者缺一或不符一律回落 cli。这样我们永远
// 不会发出一个"没在真实客户端上同时观察到"的 (UA 后缀, cc_entrypoint) 组合——
// 猜错一个 token 造出的新矛盾，比原来的旧矛盾更容易被认出来。
//
// agent-sdk 版本按我们锁定的 CLI patch 钉死，不跟着下游漂：一个账号先后冒出十几个
// SDK 版本，本身就是新的多人使用特征。
func ResolveClientEntrypoint(clientUA string, body []byte) ClientEntrypoint {
	product, hasAgentSDK, ok := parseClientUAProduct(clientUA)
	if !ok {
		return defaultClientEntrypoint()
	}
	if declared := clientDeclaredCCEntrypoint(body); declared != product {
		return defaultClientEntrypoint()
	}
	if product == "cli" {
		return defaultClientEntrypoint()
	}
	suffix := "(external, " + product + ")"
	if hasAgentSDK {
		suffix = fmt.Sprintf("(external, %s, agent-sdk/0.3.%s)", product, claude.CLIPatchVersion())
	}
	return ClientEntrypoint{Product: product, UASuffix: suffix}
}

// parseClientUAProduct 从下游 UA 里取产品名，并报告该产品的后缀是否带 agent-sdk 段。
func parseClientUAProduct(ua string) (product string, hasAgentSDK bool, ok bool) {
	m := clientUASuffixRe.FindStringSubmatch(strings.TrimSpace(ua))
	if len(m) != 2 {
		return "", false, false
	}
	parts := strings.Split(m[1], ",")
	if len(parts) < 2 || strings.TrimSpace(parts[0]) != "external" {
		return "", false, false
	}
	product = strings.TrimSpace(parts[1])
	withSDK, known := clientEntrypointProducts[product]
	if !known {
		return "", false, false
	}
	return product, withSDK, true
}

// clientDeclaredCCEntrypoint 读下游自己在 system 里带的 billing 行的 cc_entrypoint。
// 只看 system，不看 messages——与平台判定同一条纪律：正文可能引用这些字样。
func clientDeclaredCCEntrypoint(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	system := gjson.GetBytes(body, "system")
	if !system.Exists() {
		return ""
	}
	scan := func(text string) string {
		if !strings.Contains(strings.ToLower(text), billingHeaderPrefix) {
			return ""
		}
		if m := ccEntrypointValueRe.FindStringSubmatch(text); len(m) == 2 {
			return strings.TrimSpace(m[1])
		}
		return ""
	}
	switch {
	case system.Type == gjson.String:
		return scan(system.String())
	case system.IsArray():
		found := ""
		system.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() != "text" {
				return true
			}
			if v := scan(block.Get("text").String()); v != "" {
				found = v
				return false
			}
			return true
		})
		return found
	}
	return ""
}

// WithClientEntrypoint 把判定结果放进 context，供出站头构造读取。
// 判定发生在 forward 入口（那里同时拿得到未改写的正文与原始头），出站头构造在其后，
// 两处必须用同一个结果，否则头与 billing 块会各说各的。
func WithClientEntrypoint(ctx context.Context, e ClientEntrypoint) context.Context {
	if e.Product == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxkey.ClientEntrypoint, e)
}

// ClientEntrypointFromContext 读回判定结果；没有则返回 cli 回落形态。
func ClientEntrypointFromContext(ctx context.Context) ClientEntrypoint {
	if ctx == nil {
		return defaultClientEntrypoint()
	}
	if e, ok := ctx.Value(ctxkey.ClientEntrypoint).(ClientEntrypoint); ok && e.Product != "" {
		return e
	}
	return defaultClientEntrypoint()
}
