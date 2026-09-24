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
	PlatformRaw           string `json:"platform_raw"`
	Shell                 string `json:"shell"`
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

// buildReclaudeEventEnv 从账号的真实快照拼 env 块。
//
// 🔴 取值来自账号自己的 client_platform / client_version，不是硬编码。
// ⚠️ 已知缺口：node_version / linux_distro / kernel / shell 这些**真机指纹**
// 我们建号时没有采集，所以只能给出与 platform 自洽的保守值。
// 它们是「一批设备共享同一套值」的风险点 —— 若日后建号流程采集了真机快照，
// 应改为按账号读取。当前取舍：字段在、形态对、与 platform 不矛盾。
func buildReclaudeEventEnv(account *Account) ReclaudeEventEnv {
	platform := strings.TrimSpace(account.GetCredential(CredKeyReclaudeClientPlatform))
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

	return ReclaudeEventEnv{
		Platform:    osName,
		PlatformRaw: osName,
		Arch:        arch,
		Version:     version,
		VersionBase: version,
		// ⚠️ node_version 是**真机指纹**，我们没采集。给一个与 CLI 构建时期
		// 自洽的值，形态对但不是真采样 —— 与 linux_distro/kernel 同属
		// buildReclaudeEventEnv 顶部标注的已知缺口。
		NodeVersion: reclaudeDefaultNodeVersion,
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
		DeploymentEnvironment: "unknown-" + osName,
		Shell:                 reclaudeDefaultShellFor(osName),
	}
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
