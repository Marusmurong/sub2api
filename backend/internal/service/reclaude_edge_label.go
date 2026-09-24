package service

import "net/url"

// reclaudeKnownEdgeHosts 是 computeEdgeLabel 查表命中的那批主机。
//
// 表里装的是**区域 route 节点**，不含 login 默认下发的主域 www.reclaude.ai。
//
// ✅ 真值来源（两条互相印证）：
//   - `~/.reclaude/telemetry-pending.json` 里真客户端自己落盘的 rollup，
//     edge 恒为 `"asia.route.reclaude.ai"` —— 说明 route 节点**在表里**。
//   - reclaude-lab/sink/dump 的 26/26 条真实信封 edge 全是 `"unknown"`，
//     而那批样本的 daemon 指向的是 www/本地 sink —— 说明主域**不在表里**。
//
// 两条样本此前看起来互相矛盾，其实只是**同一个查表函数在两个不同 host 下的
// 两个输出**。所以这里实现规则本身，而不是把其中一个输出写死成常量。
var reclaudeKnownEdgeHosts = map[string]struct{}{
	"asia.route.reclaude.ai":   {}, // ✅ 真客户端遥测实测命中
	"la.route.reclaude.ai":     {}, // 同族推断
	"misaka.route.reclaude.ai": {}, // 同族推断
	"cloudfront.reclaude.ai":   {}, // ❓ 命名不同族，是否在表里未经实测
}

// reclaudeEdgeLabel 复刻真客户端的 computeEdgeLabel：
// 网关 host 在已知表里就返回 host（含端口），否则返回 "unknown"。
//
// 🔴 不要退化成「恒为 unknown」或「恒取 hostname」。两者都在某一侧穿帮：
// 前者在我们改打 route 节点后与真值不符，后者在打主域时与真值不符 ——
// 2026-09-24 这两个方向我各错了一次。
func reclaudeEdgeLabel(gatewayURL string) string {
	const unknownEdge = "unknown"

	parsed, err := url.Parse(gatewayURL)
	if err != nil || parsed.Host == "" {
		return unknownEdge
	}
	// 用 Host 而非 Hostname()：真实现比较的是含端口的 u.Host。
	if _, ok := reclaudeKnownEdgeHosts[parsed.Host]; ok {
		return parsed.Host
	}
	return unknownEdge
}
