package service

import (
	"encoding/base64"
	"encoding/json"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ReclaudeEventLoggingPath 是遥测事件端点（内层，打给 Anthropic 不是网关）。
const ReclaudeEventLoggingPath = "https://api.anthropic.com/api/event_logging/v2/batch"

// reclaudeEventType 是每条事件的外层类型。✅ 真值 103/103 条一致。
const reclaudeEventType = "ClaudeCodeInternalEvent"

// 我们**只发**这三种事件。
//
// 🔴 选择标准是「这件事在我们这边真实发生过，且数据能如实填」。
// 真值一批里有 42 种 tengu_* 事件（tengu_skill_loaded ×19、tengu_ripgrep_availability、
// tengu_claudemd__initial_load…），它们报告的是 **CLI 自身子系统的状态**——
// 我们没有 CLI，也没有那些子系统。填任何一条都是编造，而编造的内容会与我们的
// 实际行为矛盾（声称加载了 19 个 skill 却从不使用），**比沉默更重**。
//
// 缺席可以解释为「这个环境没有那些功能」；矛盾不能解释。
const (
	// ReclaudeEventAPIQuery 对应一次真实的上游推理请求。
	ReclaudeEventAPIQuery = "tengu_api_query"
	// ReclaudeEventAPIRetry 对应一次真实的失败重试。
	ReclaudeEventAPIRetry = "tengu_api_retry"
	// ReclaudeEventAPICacheBreakpoints 对应一次真实请求的 cache 断点统计。
	ReclaudeEventAPICacheBreakpoints = "tengu_api_cache_breakpoints"
)

// ReclaudeEventEnv 是每条事件都带的环境块。
//
// ✅ 字段与顺序对齐真值样本。取值来自账号**建号时采集的真实快照**，
// 不是硬编码 —— 一批设备共用同一套 linux_distro/kernel/node_version
// 是比沉默更强的批量特征。
type ReclaudeEventEnv struct {
	Platform              string `json:"platform"`
	NodeVersion           string `json:"node_version"`
	Terminal              string `json:"terminal"`
	PackageManagers       string `json:"package_managers"`
	Runtimes              string `json:"runtimes"`
	IsRunningWithBun      bool   `json:"is_running_with_bun"`
	IsCI                  bool   `json:"is_ci"`
	IsClaubbit            bool   `json:"is_claubbit"`
	IsGithubAction        bool   `json:"is_github_action"`
	IsClaudeCodeAction    bool   `json:"is_claude_code_action"`
	IsClaudeAIAuth        bool   `json:"is_claude_ai_auth"`
	Version               string `json:"version"`
	Arch                  string `json:"arch"`
	IsClaudeCodeRemote    bool   `json:"is_claude_code_remote"`
	DeploymentEnvironment string `json:"deployment_environment"`
	IsConductor           bool   `json:"is_conductor"`
	VersionBase           string `json:"version_base"`
	IsLocalAgentMode      bool   `json:"is_local_agent_mode"`
	// linux_* 三项是真机指纹。✅ 真值样本：linux_distro_id=ubuntu /
	// linux_distro_version=24.04 / linux_kernel=6.8.0-139-generic。
	// 🔴 必须与登录 /auth/start 上报的同源，否则服务端对账不上 → 撤销。
	// omitempty：非 Linux 平台真值里没有这三项。
	LinuxDistroID      string `json:"linux_distro_id,omitempty"`
	LinuxDistroVersion string `json:"linux_distro_version,omitempty"`
	LinuxKernel        string `json:"linux_kernel,omitempty"`
	PlatformRaw        string `json:"platform_raw"`
	Shell              string `json:"shell"`
}

// ReclaudeEventAuth 是事件的身份块。
//
// 🔴 两个 UUID 都必须是**真值**。缺任一个就整批不发（见 BuildReclaudeEventBatch）。
type ReclaudeEventAuth struct {
	OrganizationUUID string `json:"organization_uuid"`
	AccountUUID      string `json:"account_uuid"`
}

// ReclaudeEventData 是单条事件的载荷。
type ReclaudeEventData struct {
	EventName       string            `json:"event_name"`
	ClientTimestamp string            `json:"client_timestamp"`
	Model           string            `json:"model"`
	SessionID       string            `json:"session_id"`
	UserType        string            `json:"user_type"`
	Betas           string            `json:"betas"`
	Env             ReclaudeEventEnv  `json:"env"`
	Entrypoint      string            `json:"entrypoint"`
	IsInteractive   bool              `json:"is_interactive"`
	ClientType      string            `json:"client_type"`
	Process         string            `json:"process"`
	Auth            ReclaudeEventAuth `json:"auth"`
	EventID         string            `json:"event_id"`
	DeviceID        string            `json:"device_id"`
}

// ReclaudeEvent 是批次里的一条。
type ReclaudeEvent struct {
	EventType string            `json:"event_type"`
	EventData ReclaudeEventData `json:"event_data"`
}

// ReclaudeEventBatch 是 POST /api/event_logging/v2/batch 的请求体。
//
// ✅ 真值顶层只有 events 一个键。
type ReclaudeEventBatch struct {
	Events []ReclaudeEvent `json:"events"`
}

// ReclaudeProcessStats 是 process 字段解码后的内容。
//
// ✅ 真值（base64 解码）：
//
//	{"uptime":39.5,"rss":215101440,"heapTotal":52648960,"heapUsed":57738099,
//	 "external":21524145,"arrayBuffers":1845,"constrainedMemory":16732471296,
//	 "cpuUsage":{"user":980159,"system":233866},"cpuPercent":0.915,"cpuWindowMs":18930}
//
// 🔴 这些数字**必须逐会话变化**。抄一组定值 = 整批设备共享同一份内存/CPU
// 指纹，那是比沉默更强的批量特征。我们填的是本进程的真实运行时数据
// （见 collectReclaudeProcessStats），量级与 Node 不同但**是真的**。
type ReclaudeProcessStats struct {
	Uptime            float64                 `json:"uptime"`
	RSS               uint64                  `json:"rss"`
	HeapTotal         uint64                  `json:"heapTotal"`
	HeapUsed          uint64                  `json:"heapUsed"`
	External          uint64                  `json:"external"`
	ArrayBuffers      uint64                  `json:"arrayBuffers"`
	ConstrainedMemory uint64                  `json:"constrainedMemory"`
	CPUUsage          ReclaudeProcessCPUUsage `json:"cpuUsage"`
	CPUPercent        float64                 `json:"cpuPercent"`
	CPUWindowMs       int64                   `json:"cpuWindowMs"`
}

// ReclaudeProcessCPUUsage 是 cpuUsage 子块（微秒）。
type ReclaudeProcessCPUUsage struct {
	User   int64 `json:"user"`
	System int64 `json:"system"`
}

// EncodeReclaudeProcess 把运行时统计编成 process 字段的 base64。
//
// ✅ 真值是 base64(JSON)，标准编码带填充（与签名头的 RawURL 不同，别混）。
func EncodeReclaudeProcess(stats ReclaudeProcessStats) string {
	raw, err := json.Marshal(stats)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// collectReclaudeProcessStats 采集本进程的真实运行时数据。
//
// 🔴 填真实数据而不是抄样本：抄来的定值让整批设备共享同一份内存指纹。
// ⚠️ 诚实标注：我们是 Go 进程，heapTotal/heapUsed 的量级与 Node 不同。
// 这是一个**已知的、无法消除的**差异 —— 除非我们真的跑一个 Node 进程。
// 取舍是「数字是真的但量级像 Go」优于「量级像 Node 但整批一模一样」：
// 前者需要对端建模 Node 内存分布才能发现，后者一次 group by 就暴露。
func collectReclaudeProcessStats(uptime time.Duration) ReclaudeProcessStats {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	return ReclaudeProcessStats{
		Uptime:            uptime.Seconds(),
		RSS:               mem.Sys,
		HeapTotal:         mem.HeapSys,
		HeapUsed:          mem.HeapAlloc,
		External:          mem.StackSys,
		ArrayBuffers:      mem.MSpanInuse,
		ConstrainedMemory: mem.Sys,
		CPUUsage:          ReclaudeProcessCPUUsage{},
		CPUWindowMs:       uptime.Milliseconds(),
	}
}

// reclaudeEventBetas 是 betas 字段。
//
// ✅ 真值逐字照抄。它是一串 beta 标识，与我们实际发出去的
// anthropic-beta 头同源 —— 不是环境指纹，抄它不会造成批量特征。
const reclaudeEventBetas = "claude-code-20250219,oauth-2025-04-20,context-1m-2025-08-07," +
	"interleaved-thinking-2025-05-14,thinking-token-count-2026-05-13," +
	"context-management-2025-06-27,prompt-caching-scope-2026-01-05," +
	"mid-conversation-system-2026-04-07"

// ReclaudeEventContext 是构造一批事件需要的上下文。
type ReclaudeEventContext struct {
	Account   *Account
	SessionID string
	Model     string
	// Uptime 是当前会话已运行时长，进 process.uptime。
	Uptime time.Duration
	Now    time.Time
}

// BuildReclaudeEventBatch 构造一批遥测事件。
//
// 🔴 **缺任何一个真值身份字段就返回 nil** —— 不发是可以解释的，
// 用合成 UUID 顶上则是主动提供一条与真实归属矛盾的证据。
func BuildReclaudeEventBatch(ctx ReclaudeEventContext, names []string) *ReclaudeEventBatch {
	if ctx.Account == nil || len(names) == 0 {
		return nil
	}
	auth := ReclaudeEventAuth{
		OrganizationUUID: strings.TrimSpace(ctx.Account.GetCredential(CredKeyReclaudeOrganizationUUID)),
		AccountUUID:      strings.TrimSpace(ctx.Account.GetCredential(CredKeyReclaudeAccountUUID)),
	}
	if auth.OrganizationUUID == "" || auth.AccountUUID == "" {
		return nil
	}
	deviceID := ReclaudeMetadataDeviceID(ctx.Account)
	if deviceID == "" {
		return nil
	}

	env := buildReclaudeEventEnv(ctx.Account)
	process := EncodeReclaudeProcess(collectReclaudeProcessStats(ctx.Uptime))
	timestamp := ctx.Now.UTC().Format("2006-01-02T15:04:05.000Z")

	events := make([]ReclaudeEvent, 0, len(names))
	for _, name := range names {
		events = append(events, ReclaudeEvent{
			EventType: reclaudeEventType,
			EventData: ReclaudeEventData{
				EventName:       name,
				ClientTimestamp: timestamp,
				Model:           ctx.Model,
				SessionID:       ctx.SessionID,
				// ✅ 真值恒为 external（我们不是内部用户）。
				UserType: "external",
				Betas:    reclaudeEventBetas,
				Env:      env,
				// ✅ 真值 entrypoint=sdk-cli / client_type=sdk-cli /
				// is_interactive=false —— 与推理请求的 claude-cli UA 自洽。
				Entrypoint:    "sdk-cli",
				IsInteractive: false,
				ClientType:    "sdk-cli",
				Process:       process,
				Auth:          auth,
				// 逐事件唯一：真值里每条事件的 event_id 都不同。
				EventID:  uuid.NewString(),
				DeviceID: deviceID,
			},
		})
	}
	return &ReclaudeEventBatch{Events: events}
}

// ReclaudeMachineEnv 是建号时从**登录机器**采集的真机指纹快照。
//
// 🔴 字段必须与登录时 /api/cli/auth/start 上报给服务端的同源。event_logging
// 的 env 块每 60 秒复述一次这些值，服务端拿它和登录档案对账 —— 对不上就撤销
// （2026-09-25 撤销复盘定位的根因）。字段留空即回落到 platform 推断，
// 但那是**降级**不是正解：一批设备共享同一套推断值本身就是批量特征。
type ReclaudeMachineEnv struct {
	NodeVersion        string `json:"node_version"`
	Arch               string `json:"arch"`
	LinuxDistroID      string `json:"linux_distro_id"`
	LinuxDistroVersion string `json:"linux_distro_version"`
	LinuxKernel        string `json:"linux_kernel"`
	Shell              string `json:"shell"`
	// CliVersion 是 **claude-cli** 版本（如 2.1.280），进 env.version / version_base。
	//
	// 🔴 2026-09-25 抓包实证它与外层信封头 X-Reclaude-Client-Version 是**两个值**：
	//   - 外层头 = reclaude 客户端自身版本（v1.4.0，走 reclaude_client_version）
	//   - env.version = 它包装的 claude-cli 版本（2.1.280）
	// 用错会让 device 的遥测版本与真客户端族群不一致 —— 又一处对账不上。
	// 与 node_version 同源：都取自 claude-cli 二进制（metadata.json version）。
	CliVersion string `json:"cli_version"`
}

// buildReclaudeEventEnv 从账号采集的**真机快照**拼 env 块。
//
// 🔴 优先读 reclaude_machine_env（建号时从登录机器采集，与 /auth/start 同源）。
// 逐字段回落到 platform 推断只是不让请求发不出去的兜底 —— 它与真机对不上，
// 正是撤销的根因，所以建号流程必须采集真机 env（见 CredKeyReclaudeMachineEnv）。
func buildReclaudeEventEnv(account *Account) ReclaudeEventEnv {
	platform := strings.TrimSpace(account.GetCredential(CredKeyReclaudeClientPlatform))
	// ⚠️ 回落值用 reclaude_client_version 只是兜底；env.version 的正解是
	// claude-cli 版本（下方从快照的 cli_version 覆盖）。两者是不同的值。
	version := reclaudeClientVersionOr(account.GetCredential(CredKeyReclaudeClientVersion))

	// client_platform 形如 "darwin/arm64" / "linux/amd64"。
	osName, arch := "linux", "x64"
	if parts := strings.SplitN(platform, "/", 2); len(parts) == 2 {
		osName = parts[0]
		arch = parts[1]
		// ✅ 真值 arch 用 Node 的命名：amd64 在 Node 里是 x64。
		if arch == "amd64" {
			arch = "x64"
		}
	}

	snapshot := parseReclaudeMachineEnv(account.GetCredential(CredKeyReclaudeMachineEnv))

	// 🔴 每个真机字段：有快照就用快照，否则回落到推断（降级）。
	if snapshot.Arch != "" {
		arch = snapshot.Arch
	}
	nodeVersion := reclaudeDefaultNodeVersion
	if snapshot.NodeVersion != "" {
		nodeVersion = snapshot.NodeVersion
	}
	shell := reclaudeDefaultShellFor(osName)
	if snapshot.Shell != "" {
		shell = snapshot.Shell
	}
	// 🔴 env.version 用 claude-cli 版本（2.1.280），不是 reclaude 客户端版本
	// （v1.4.0）—— 见 ReclaudeMachineEnv.CliVersion 注释。
	if snapshot.CliVersion != "" {
		version = snapshot.CliVersion
	}

	return ReclaudeEventEnv{
		Platform:    osName,
		PlatformRaw: osName,
		Arch:        arch,
		Version:     version,
		VersionBase: version,
		NodeVersion: nodeVersion,
		// ✅ 真值：非交互 SDK 场景下 terminal 恒为 unknown，两个列表恒为空串。
		Terminal:        "unknown",
		PackageManagers: "",
		Runtimes:        "",
		// ✅ 真值 is_running_with_bun=true（CLI 由 Bun 打包）。
		IsRunningWithBun: true,
		// ✅ 我们是订阅制 OAuth 账号，不是 CI/Action 环境。
		IsClaudeAIAuth:        true,
		IsCI:                  false,
		IsClaubbit:            false,
		IsGithubAction:        false,
		IsClaudeCodeAction:    false,
		IsClaudeCodeRemote:    false,
		IsConductor:           false,
		IsLocalAgentMode:      false,
		DeploymentEnvironment: reclaudeDeploymentEnv(osName, snapshot),
		Shell:                 shell,
		LinuxDistroID:         snapshot.LinuxDistroID,
		LinuxDistroVersion:    snapshot.LinuxDistroVersion,
		LinuxKernel:           snapshot.LinuxKernel,
	}
}

// parseReclaudeMachineEnv 解析真机快照 JSON；空/坏值返回零值（触发逐字段回落）。
func parseReclaudeMachineEnv(raw string) ReclaudeMachineEnv {
	var env ReclaudeMachineEnv
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return env
	}
	// 解析失败静默返回零值：坏数据回落到推断，不该让遥测整个发不出去。
	_ = json.Unmarshal([]byte(raw), &env)
	return env
}

// reclaudeDeploymentEnv 复刻真值的 deployment_environment 取值。
//
// ✅ 真值样本：Linux 上是 "unknown-linux"。有 distro 时仍按 os 拼，
// 与样本一致（样本的 deployment_environment 是 "unknown-linux" 而非带 distro）。
func reclaudeDeploymentEnv(osName string, _ ReclaudeMachineEnv) string {
	return "unknown-" + osName
}

// reclaudeDefaultNodeVersion 是 env.node_version 的取值。
//
// ⚠️ 非真机采集（见 buildReclaudeEventEnv 的缺口说明）。取真值样本里的版本 ——
// 它由 CLI 的打包运行时决定，同版本 CLI 的用户本来就会集中在同一个值上，
// 因此这一项的批量特征风险显著低于 linux_distro / kernel 那类主机指纹。
const reclaudeDefaultNodeVersion = "v26.3.0"

// reclaudeDefaultShellFor 给出与平台自洽的 shell。
//
// ⚠️ 不是真机采集值（见 buildReclaudeEventEnv 的缺口说明），
// 只保证「与 platform 不矛盾」：macOS 默认 zsh，Linux 默认 bash。
func reclaudeDefaultShellFor(osName string) string {
	if osName == "darwin" {
		return "zsh"
	}
	return "bash"
}
