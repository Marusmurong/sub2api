package service

import (
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"time"
)

// Datadog 日志信封 —— 一条推理最大的伴随信封（2026-09-27 cli 交互式抓包，
// 单批 130KB+）。真客户端把 CLI 遥测同时上报到 Anthropic event_logging 和
// Datadog（http-intake.logs.us5.datadoghq.com）。sub2api 此前只发 event_logging，
// 缺 Datadog 这一路 —— 网关看到的会话形态因此比真客户端薄一大截。
//
// ✅ 真值依据：docs/RECLAUDE_ACCOMPANYING_ENVELOPES_2026-09-27.md（Datadog 段）。
//
// 🔴 与 event_logging 同一条红线：**只发我方真实发生过的事**。真客户端一批
// Datadog 有 75 条，其中 59 条是 tengu_feature_ok（skill_load_dir / plugin_load_*
// 等 CLI 内部子系统加载事件）—— 我们没有那些子系统，照发就是「声称加载了一堆
// skill 却从不使用」的矛盾证词，比沉默更重。只有 tengu_api_success（推理成功）
// 是我方真实对应的，故只发它。缺席可解释为「精简环境」，矛盾不能解释。

// ReclaudeDatadogLogsURL 是 Datadog 日志 intake 端点（内层 meta.url，经 /proxy 信封发）。
const ReclaudeDatadogLogsURL = "https://http-intake.logs.us5.datadoghq.com/api/v2/logs"

// reclaudeDatadogAPIKey 是真客户端硬编码的 Datadog **公开** pub key（真值定值）。
// pub 前缀 = client-side 公开 key，本就设计成随客户端分发，不是密钥。
const reclaudeDatadogAPIKey = "pubea5604404508cdd34afb69e6f42a05bc"

// Datadog 事件的定值字段（真值 75/75 条一致）。
const (
	reclaudeDatadogSource   = "nodejs"
	reclaudeDatadogService  = "claude-code"
	reclaudeDatadogHostname = "claude-code"
	reclaudeDatadogEnv      = "external"
	reclaudeDatadogVCS      = "git"
)

// ReclaudeDatadogEvent 是 Datadog logs 数组里的一条。
//
// 结构 = event_logging 的 env 块**平铺** + Datadog 专属字段（ddsource/ddtags/
// message/service/hostname/betas/subscription_type/user_bucket/process_metrics）。
// 字段来源与 event_logging 完全一致（DRY）：env 取账号真机快照、process 现算、
// entrypoint/is_interactive/client_type 钉 cli。
type ReclaudeDatadogEvent struct {
	DDSource string `json:"ddsource"`
	DDTags   string `json:"ddtags"`
	Message  string `json:"message"`
	Service  string `json:"service"`
	Hostname string `json:"hostname"`
	Env      string `json:"env"`
	Model    string `json:"model"`

	SessionID    string `json:"session_id"`
	UserType     string `json:"user_type"`
	Betas        string `json:"betas"`
	IsClaudeAIAuth bool `json:"is_claude_ai_auth"`

	Entrypoint    string `json:"entrypoint"`
	IsInteractive string `json:"is_interactive"` // 🔴 真值是字符串 "true"，不是 bool
	ClientType    string `json:"client_type"`

	// process_metrics：每次现算，逐会话变化（同 event_logging 的 process 块）。
	ProcessMetrics ReclaudeProcessStats `json:"process_metrics"`

	SubscriptionType string `json:"subscription_type"`
	UserBucket       int    `json:"user_bucket"`

	// 平铺的 env 主机指纹（取自 buildReclaudeEventEnv 的产物 —— 同一份真机快照）。
	Platform              string `json:"platform"`
	PlatformRaw           string `json:"platform_raw"`
	Arch                  string `json:"arch"`
	NodeVersion           string `json:"node_version"`
	Terminal              string `json:"terminal"`
	Shell                 string `json:"shell"`
	PackageManagers       string `json:"package_managers"`
	Runtimes              string `json:"runtimes"`
	IsRunningWithBun      bool   `json:"is_running_with_bun"`
	Version               string `json:"version"`
	VersionBase           string `json:"version_base"`
	BuildTime             string `json:"build_time"`
	DeploymentEnvironment string `json:"deployment_environment"`
	VCS                   string `json:"vcs"`
	LinuxDistroID         string `json:"linux_distro_id,omitempty"`
	LinuxDistroVersion    string `json:"linux_distro_version,omitempty"`
	LinuxKernel           string `json:"linux_kernel,omitempty"`

	IsCI               bool `json:"is_ci"`
	IsClaubbit         bool `json:"is_claubbit"`
	IsClaudeCodeRemote bool `json:"is_claude_code_remote"`
	IsLocalAgentMode   bool `json:"is_local_agent_mode"`
	IsConductor        bool `json:"is_conductor"`
	IsGithubAction     bool `json:"is_github_action"`
	IsClaudeCodeAction bool `json:"is_claude_code_action"`

	// tengu_started 专属字段（真值：全空串 / false —— 我们不是 swe-bench/tmux/worktree
	// 环境，如实填。swe_bench_* 是空字符串不是省略，真值里始终存在）。
	SweBenchRunID      string `json:"swe_bench_run_id"`
	SweBenchInstanceID string `json:"swe_bench_instance_id"`
	SweBenchTaskID     string `json:"swe_bench_task_id"`
	InTmuxWorktree     bool   `json:"in_tmux_worktree"`
	TmuxFlag           bool   `json:"tmux_flag"`
	WorktreeFlag       bool   `json:"worktree_flag"`
}

// reclaudeDatadogEventName 是我方真实对应、可如实上报的 Datadog 事件。
//
// 🔴 选 tengu_started 而非 tengu_api_success：
//   - tengu_api_success（真值 97 字段）带 40+ 个推理内部度量 —— ttft_ms /
//     first_content_ms / query_chain_id / snapshot_hash / tools_char_length 等
//     是 CLI 自身的内部状态，我们代理层拿不到真值，填即矛盾（违背 event_logging
//     那套「不编造子系统状态」的红线）。
//   - tengu_started（真值 47 字段）是**会话启动**事件，字段全是 env 骨架 +
//     6 个可如实填的会话属性（swe_bench_* 空、tmux/worktree false）。会话确实
//     启动了 —— 这是我方真实发生、且能逐字段如实填的事件。
const reclaudeDatadogEventName = "tengu_started"

// BuildReclaudeDatadogBatch 构造一批 Datadog 日志事件。
//
// 🔴 缺任何真值身份字段（account_uuid / device_id）就返回 nil —— 与
// BuildReclaudeEventBatch 同：不发可解释，用合成 UUID 顶上是主动造矛盾证据。
//
// 复用 buildReclaudeEventEnv 拿真机 env、collectReclaudeProcessStats 现算 process，
// 保证 Datadog 与 event_logging 两路的字段严格同源（对端交叉比对不会打架）。
func BuildReclaudeDatadogBatch(ctx ReclaudeEventContext) []ReclaudeDatadogEvent {
	if ctx.Account == nil {
		return nil
	}
	accountUUID := strings.TrimSpace(ctx.Account.GetCredential(CredKeyReclaudeAccountUUID))
	if accountUUID == "" {
		return nil
	}
	if ReclaudeMetadataDeviceID(ctx.Account) == "" {
		return nil
	}

	env := buildReclaudeEventEnv(ctx.Account)
	process := collectReclaudeProcessStats(ctx.Uptime)
	bucket := reclaudeUserBucket(accountUUID)

	event := ReclaudeDatadogEvent{
		DDSource:       reclaudeDatadogSource,
		DDTags:         reclaudeDatadogTags(env, ctx.Model, bucket),
		Message:        reclaudeDatadogEventName,
		Service:        reclaudeDatadogService,
		Hostname:       reclaudeDatadogHostname,
		Env:            reclaudeDatadogEnv,
		Model:          ctx.Model,
		SessionID:      ctx.SessionID,
		UserType:       "external",
		Betas:          reclaudeEventBetas,
		IsClaudeAIAuth: true,
		// 🔴 与 event_logging / 推理 UA / billing 全链路一致：cli。
		// is_interactive 是**字符串** "true"（真值 2026-09-27）。
		Entrypoint:    "cli",
		IsInteractive: "true",
		ClientType:    "cli",
		ProcessMetrics: process,

		SubscriptionType: reclaudeDatadogSubscription,
		UserBucket:       bucket,

		Platform:              env.Platform,
		PlatformRaw:           env.PlatformRaw,
		Arch:                  env.Arch,
		NodeVersion:           env.NodeVersion,
		Terminal:              env.Terminal,
		Shell:                 env.Shell,
		PackageManagers:       env.PackageManagers,
		Runtimes:              env.Runtimes,
		IsRunningWithBun:      env.IsRunningWithBun,
		Version:               env.Version,
		VersionBase:           env.VersionBase,
		BuildTime:             env.BuildTime,
		DeploymentEnvironment: env.DeploymentEnvironment,
		VCS:                   reclaudeDatadogVCS,
		LinuxDistroID:         env.LinuxDistroID,
		LinuxDistroVersion:    env.LinuxDistroVersion,
		LinuxKernel:           env.LinuxKernel,

		IsCI:               false,
		IsClaubbit:         false,
		IsClaudeCodeRemote: false,
		IsLocalAgentMode:   false,
		IsConductor:        false,
		IsGithubAction:     false,
		IsClaudeCodeAction: false,
	}

	// Datadog 事件真值里没有 event_id 字段（不同于 event_logging）。
	return []ReclaudeDatadogEvent{event}
}

// reclaudeDatadogSubscription 是订阅档位。
//
// ⚠️ 真值样本是 max。它是账号级属性，应随账号订阅真实取值 —— 若一批账号并非
// 全是 max，统一填 max 就是批量特征。缺账号订阅字段时回落 max（当前建号默认档），
// 建号流程补上真实订阅后从账号档案取（TODO：CredKeyReclaudeSubscriptionType）。
const reclaudeDatadogSubscription = "max"

// reclaudeUserBucket 把 account_uuid 稳定哈希到 0-9。
//
// 🔴 user_bucket 是 Anthropic 按用户分的桶。**每账号必须固定且跨账号分散** ——
// 统一填一个值（如真值样本的 8）= 整批设备同桶，一次 group by 即暴露。
// 真实算法未知（真值 8 无法从 md5/sha256 反推），但对隐蔽性而言「稳定+分散」
// 就够：同一账号每次同桶（与真客户端行为一致），不同账号散落 0-9。
func reclaudeUserBucket(accountUUID string) int {
	sum := sha256.Sum256([]byte(accountUUID))
	return int(binary.BigEndian.Uint32(sum[:4]) % 10)
}

// reclaudeDatadogTags 拼 ddtags（真值键序）。
//
// ✅ 真值：event:<msg>,arch:<a>,client_type:cli,entrypoint:cli,model:<m>,
// platform:<p>,subscription_type:<s>,user_bucket:<b>,user_type:external,
// version:<v>,version_base:<v>
func reclaudeDatadogTags(env ReclaudeEventEnv, model string, bucket int) string {
	return strings.Join([]string{
		"event:" + reclaudeDatadogEventName,
		"arch:" + env.Arch,
		"client_type:cli",
		"entrypoint:cli",
		"model:" + model,
		"platform:" + env.Platform,
		"subscription_type:" + reclaudeDatadogSubscription,
		"user_bucket:" + itoaBucket(bucket),
		"user_type:external",
		"version:" + env.Version,
		"version_base:" + env.VersionBase,
	}, ",")
}

func itoaBucket(b int) string {
	return string(rune('0' + b))
}

// BuildReclaudeDatadogRequest 把一批 Datadog 事件包成一条生命周期请求（内层信封）。
//
// ✅ 头逐字对齐真值：UA axios/1.15.2、dd-api-key、host datadoghq、**无 authorization**、
// connection close。这条路径在真客户端里由 Datadog SDK（axios）发出，与 CLI 本体
// （claude-cli）、event_logging（claude-code/Bun）分属不同代码路径，头也因此不同。
func BuildReclaudeDatadogRequest(body []byte, delay time.Duration) ReclaudeLifecycleRequest {
	return ReclaudeLifecycleRequest{
		Method: "POST",
		URL:    ReclaudeDatadogLogsURL,
		Headers: map[string]string{
			"accept":          "application/json, text/plain, */*",
			"accept-encoding": "gzip, compress, deflate, br",
			"connection":      "close",
			"content-type":    "application/json",
			"host":            "http-intake.logs.us5.datadoghq.com",
			"user-agent":      reclaudeInnerUAAxios,
			"dd-api-key":      reclaudeDatadogAPIKey,
		},
		// 🔴 Datadog intake 不认 Anthropic 的 OAuth，真值里这条**无 authorization**。
		NeedsAuthorization: false,
		Body:               body,
		Delay:              delay,
	}
}
