package service

// 真实 Claude Code 2.1.220 的顶层默认值，实测自它发出的请求体。
//
// 采样覆盖两个入口且结论一致，不是单一入口的偶然：
//
//	cc_entrypoint=cli      （交互式，用伪终端驱动一次真实会话）
//	cc_entrypoint=sdk-cli  （claude -p）
//
// 两者的顶层字段集都是：
//
//	context_management max_tokens messages metadata model
//	output_config stream system thinking tools
//
// 2026-08-03 的生产抓包（15 条转发，其中 5 条为真 CC 主对话）复核了同一组结论。
//
// 据此可以确定三件事：max_tokens 是 64000（不是此前写的 128000）；output_config 恒发
// {"effort":"high"}；没有 temperature / top_p / top_k。
//
// 但这里只保留 max_tokens。output_config 曾按同样的理由补过，当天就在生产上打出
// "This model does not support the effort parameter." —— 实测「真实 CC 会发」不等于
// 「我们补上去是安全的」：真实 CC 只对自己支持的模型发，而我们面对的是下游任意模型。
// 详见 gateway_claude_oauth_body.go 中该处的注释。
//
// 只在客户端自己没给的时候补——伪装是补齐缺失，不是改写调用方的意图。
const claudeCodeDefaultMaxTokens = 64000
