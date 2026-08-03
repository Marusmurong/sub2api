package service

import (
	_ "embed"
	"encoding/json"
	"sync"
)

// 非 Claude Code 客户端的工具集补全。
//
// 背景（2026-08-03 实测）：把 ToB-CC 分组放行非 CC 流量后，即便账号已用上实测正确的
// TLS 指纹（profile 2）、HTTP 身份维度也早已字节级对齐，上游仍然返回
//
//	Third-party apps now draw from your extra usage, not your plan limits.
//
// 差异因此只可能在请求体里。而请求体上唯一「明知没做」的维度就是 tools——出口规格
// 文档第 9 节把它记为「未抽取（sub2api 透传客户端 tools，优先级低）」。
//
// 此前的处理有两个问题：
//
//  1. 客户端没带 tools 时补一个空数组 `tools: []`。真实 Claude Code 每个请求都带
//     完整工具集，空数组是它从不产生的形态——字段在、内容空，比干脆不带更可疑。
//  2. 客户端带了 tools 时把工具名混淆成 invoke_Xxx / analyze_Yyy 之类的假名。那是
//     「隐藏」不是「伪装」：假名同样不是 Claude Code 的工具名，上游看到的依旧是一个
//     从不存在的工具集。而判定恰恰叫 "Third-party apps"。
//
// 本文件提供真实 Claude Code 的核心工具定义，用于填掉第 1 种情况。
//
// # 取值来源
//
// 从真实 Claude Code 2.1.220（Bun 编译的 macOS arm64 原生二进制）实际发出的请求体中
// 原样摘取，非手工誊写：起一个本地 HTTP 监听，用 `claude --settings <临时json> -p hi`
// 把 ANTHROPIC_BASE_URL 指过去，落盘后按名字挑出这 8 个。
//
// # 为什么只取 8 个
//
// 那次抓到 64 个工具，其中 32 个是 MCP（`mcp__` 前缀）、4 个来自本机插件与技能——
// 都属于**该机器的配置**，注入到别人的请求里反而更假。剩下 28 个官方原生工具共约
// 1.9 万 token，而这里只取跨版本稳定存在的 8 个核心工具，约 3000 token：
// 成本降到六分之一，也不会因为对方跑的是别的 CC 版本而露馅（Cron*/Task*/Workflow
// 这类是较新版本才有的）。
//
//go:embed assets/claudecode_core_tools.json
var claudeCodeCoreToolsJSON []byte

var (
	claudeCodeCoreToolsOnce sync.Once
	claudeCodeCoreToolsRaw  json.RawMessage
)

// ClaudeCodeCoreToolsRaw 返回可直接写进请求体 tools 字段的 JSON 数组。
//
// 解析失败时返回 nil，调用方据此跳过注入——宁可维持原样，也不要写进一个残缺的
// tools 字段，那比不注入更容易被识别。
func ClaudeCodeCoreToolsRaw() json.RawMessage {
	claudeCodeCoreToolsOnce.Do(func() {
		var probe []map[string]any
		if err := json.Unmarshal(claudeCodeCoreToolsJSON, &probe); err != nil || len(probe) == 0 {
			return
		}
		// 压缩掉仓库里为可读性保留的缩进：真实客户端发的是紧凑 JSON。
		compact, err := json.Marshal(probe)
		if err != nil {
			return
		}
		claudeCodeCoreToolsRaw = compact
	})
	return claudeCodeCoreToolsRaw
}

// ClaudeCodeCoreToolNames 返回核心工具名，供测试与诊断使用。
func ClaudeCodeCoreToolNames() []string {
	var parsed []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(claudeCodeCoreToolsJSON, &parsed); err != nil {
		return nil
	}
	names := make([]string, 0, len(parsed))
	for _, t := range parsed {
		names = append(names, t.Name)
	}
	return names
}
