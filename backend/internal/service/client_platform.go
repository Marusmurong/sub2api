package service

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/tidwall/gjson"
)

// ClientPlatform 是下游客户端所在的操作系统 + 架构，按平台分池（审计 C-1）的判定结果。
//
// 判定只用真实 Claude Code 每个请求本来就发的东西，客户端零改动：
//  1. system 环境块 <env> 里的 "Platform: darwin|win32|linux"（优先——这正是上游读正文看到的那段）
//  2. X-Stainless-OS / X-Stainless-Arch 头
//
// 非 CC 客户端（SDK、OpenCode、codex…）两者都没有，判为未知，归默认池。
type ClientPlatform string

const (
	ClientPlatformUnknown      ClientPlatform = ""
	ClientPlatformMacOSArm64   ClientPlatform = "macos-arm64"
	ClientPlatformMacOSX64     ClientPlatform = "macos-x64"
	ClientPlatformWindowsX64   ClientPlatform = "windows-x64"
	ClientPlatformWindowsArm64 ClientPlatform = "windows-arm64"
	ClientPlatformLinuxX64     ClientPlatform = "linux-x64"
	ClientPlatformLinuxArm64   ClientPlatform = "linux-arm64"
)

// ClientPlatformSource 记录判定依据，观察期用来评估两种来源的覆盖率与一致性。
type ClientPlatformSource string

const (
	ClientPlatformSourceEnvBlock ClientPlatformSource = "env_block"
	ClientPlatformSourceHeader   ClientPlatformSource = "header"
	ClientPlatformSourceNone     ClientPlatformSource = "none"
)

// ClientPlatformObservation 是一次请求的平台判定结果。
type ClientPlatformObservation struct {
	Platform ClientPlatform
	OS       string // darwin / win32 / linux（原始值，便于对照）
	Arch     string // arm64 / x64
	Source   ClientPlatformSource
}

// ClientPlatformStatsCache 是观察期的计数存储（Redis）。所有方法 fail-open：
// 观察指标丢一条不影响请求。
type ClientPlatformStatsCache interface {
	// IncrClientPlatformStat 给当天的 field 计数 +1。field 见 ClientPlatformStatField。
	IncrClientPlatformStat(ctx context.Context, day, field string) error
	// RecordSessionClientPlatform 记录会话首次判定的平台（只写一次），返回之前记录的值；
	// 首次记录返回空串。用来度量同一会话内判定是否稳定。
	RecordSessionClientPlatform(ctx context.Context, sessionHash, platform string, ttl time.Duration) (previous string, err error)
	// IncrClientIdentityStat 给当天的 HTTP 身份组合计数 +1。field 见 ClientIdentityStatField。
	IncrClientIdentityStat(ctx context.Context, day, field string) error
	// ReadClientPlatformStats 读回某天的平台计数（field → 次数），分池按它算目标占比。
	ReadClientPlatformStats(ctx context.Context, day string) (map[string]int64, error)
}

// WithClientPlatform 把本请求判定出的平台放进 context，供调度层分池读取。
func WithClientPlatform(ctx context.Context, p ClientPlatform) context.Context {
	if p == ClientPlatformUnknown {
		return ctx
	}
	return context.WithValue(ctx, ctxkey.ClientPlatform, p)
}

// ClientPlatformFromContext 读出本请求的平台；没有则返回 Unknown。
func ClientPlatformFromContext(ctx context.Context) ClientPlatform {
	if ctx == nil {
		return ClientPlatformUnknown
	}
	if p, ok := ctx.Value(ctxkey.ClientPlatform).(ClientPlatform); ok {
		return p
	}
	return ClientPlatformUnknown
}

// ParseClientPlatform 把配置 / 账号 extra 里的字符串规范成 ClientPlatform；不认识返回 Unknown。
func ParseClientPlatform(s string) ClientPlatform {
	switch ClientPlatform(strings.ToLower(strings.TrimSpace(s))) {
	case ClientPlatformMacOSArm64:
		return ClientPlatformMacOSArm64
	case ClientPlatformMacOSX64:
		return ClientPlatformMacOSX64
	case ClientPlatformWindowsX64:
		return ClientPlatformWindowsX64
	case ClientPlatformWindowsArm64:
		return ClientPlatformWindowsArm64
	case ClientPlatformLinuxX64:
		return ClientPlatformLinuxX64
	case ClientPlatformLinuxArm64:
		return ClientPlatformLinuxArm64
	default:
		return ClientPlatformUnknown
	}
}

// AccountClientPlatformExtraKey 是账号 extra 里的定型字段。空 = 未定型。
// 导出给 repository 的调度投影白名单（scheduler_cache.go filterSchedulerExtra）使用。
const AccountClientPlatformExtraKey = "client_platform"

// accountClientPlatformKey 是包内别名。
const accountClientPlatformKey = AccountClientPlatformExtraKey

// ClientPlatform 返回账号定型的客户端平台；未定型返回 Unknown。
func (a *Account) ClientPlatform() ClientPlatform {
	if a == nil || a.Extra == nil {
		return ClientPlatformUnknown
	}
	v, _ := a.Extra[accountClientPlatformKey].(string)
	return ParseClientPlatform(v)
}

// ClientPlatformIdentity 是平台 → 出站 stainless 头 (OS, Arch) 的表。
//
// 2026-09-07 的证据：Windows 客户抓样 + 线上身份计数都表明 Runtime / Runtime-Version /
// Package-Version 与 TLS 握手（Bun 静态链接 BoringSSL）跟操作系统无关，各平台只有
// OS / Arch 两个头不同，TLS 全平台复用 profile 6。所以表里只需要这两项。
func ClientPlatformIdentity(p ClientPlatform) (os, arch string, ok bool) {
	switch p {
	case ClientPlatformMacOSArm64:
		return "MacOS", "arm64", true
	case ClientPlatformMacOSX64:
		return "MacOS", "x64", true
	case ClientPlatformWindowsX64, ClientPlatformWindowsArm64:
		// coerceOSArch 会把 Windows 强制为 x64，与此一致。
		return "Windows", "x64", true
	case ClientPlatformLinuxX64:
		return "Linux", "x64", true
	case ClientPlatformLinuxArm64:
		return "Linux", "arm64", true
	default:
		return "", "", false
	}
}

var (
	envBlockRe = regexp.MustCompile(`(?s)<env>(.*?)</env>`)
	// 两种线上形态都要认：
	//   旧：<env> 块里的 "Platform: darwin"
	//   新（2.1.26x）：system 末尾「# Environment」段下的列表项 " - Platform: darwin"
	platformLineRe = regexp.MustCompile(`(?m)^\s*(?:[-*]\s+)?Platform:\s*(darwin|win32|linux)\s*$`)
)

// ClassifyClientPlatform 判定请求来自哪个平台。见 ClientPlatform 的说明。
func ClassifyClientPlatform(headers http.Header, body []byte) ClientPlatformObservation {
	archHint := clientPlatformArchFromStainless(headerValue(headers, "X-Stainless-Arch"))

	if os := platformFromEnvBlock(body); os != "" {
		arch := archHint
		if arch == "" {
			arch = defaultArchForOS(os)
		}
		return ClientPlatformObservation{Platform: composeClientPlatform(os, arch), OS: os, Arch: arch, Source: ClientPlatformSourceEnvBlock}
	}

	if os := clientPlatformOSFromStainless(headerValue(headers, "X-Stainless-OS")); os != "" {
		arch := archHint
		if arch == "" {
			arch = defaultArchForOS(os)
		}
		return ClientPlatformObservation{Platform: composeClientPlatform(os, arch), OS: os, Arch: arch, Source: ClientPlatformSourceHeader}
	}

	return ClientPlatformObservation{Source: ClientPlatformSourceNone}
}

// platformFromEnvBlock 只看 system（string 或 text 块数组），不看 messages：
// messages 里的 system-reminder / 用户文本可能引用 "Platform: win32" 这样的字样
// （比如用户在讨论这份代码），按全文匹配会误判——沿用「只解析确定位置」的纪律。
// 优先取 <env>…</env> 内的行（旧形态），没有 <env> 标签时退化为 system 文本内的整行匹配
// （2.1.26x 的「# Environment」列表项形态就靠这一步命中）。
func platformFromEnvBlock(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	system := gjson.GetBytes(body, "system")
	if !system.Exists() {
		return ""
	}
	texts := make([]string, 0, 4)
	switch {
	case system.Type == gjson.String:
		texts = append(texts, boundedScanText(system.String()))
	case system.IsArray():
		system.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "text" {
				texts = append(texts, boundedScanText(block.Get("text").String()))
			}
			return true
		})
	}
	// 先在 <env> 内找
	for _, t := range texts {
		for _, m := range envBlockRe.FindAllStringSubmatch(t, -1) {
			if os := matchPlatformLine(m[1]); os != "" {
				return os
			}
		}
	}
	// 再退化为整行匹配
	for _, t := range texts {
		if os := matchPlatformLine(t); os != "" {
			return os
		}
	}
	return ""
}

// clientPlatformScanLimit 是每个 system 文本块参与扫描的上限。2.1.26x 把环境信息放在
// 主 system 提示词**末尾**的「# Environment」段（主提示词本身几十 KB），所以上限不能小；
// 512KB 对 RE2 是毫秒级，上限存在的意义只是防一个病态巨大的 system 拖慢热路径。
// （09-07 上线首版设 16KB，线上零条 env_block 命中，就是被这条截掉的。）
const clientPlatformScanLimit = 512 * 1024

func boundedScanText(s string) string {
	if len(s) <= clientPlatformScanLimit {
		return s
	}
	return s[:clientPlatformScanLimit]
}

func matchPlatformLine(text string) string {
	m := platformLineRe.FindStringSubmatch(text)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func clientPlatformOSFromStainless(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "macos", "darwin":
		return "darwin"
	case "windows", "win32":
		return "win32"
	case "linux":
		return "linux"
	default:
		return ""
	}
}

func clientPlatformArchFromStainless(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "arm64", "aarch64":
		return "arm64"
	case "x64", "x86_64", "amd64":
		return "x64"
	default:
		return ""
	}
}

// defaultArchForOS 是头里没有 arch 时的推断：macOS 默认 arm64（Apple Silicon 是绝对主流），
// 其余默认 x64。观察期会把 Source 一起记下来，能看出这条推断的使用频率。
func defaultArchForOS(os string) string {
	if os == "darwin" {
		return "arm64"
	}
	return "x64"
}

func composeClientPlatform(os, arch string) ClientPlatform {
	switch os {
	case "darwin":
		if arch == "x64" {
			return ClientPlatformMacOSX64
		}
		return ClientPlatformMacOSArm64
	case "win32":
		if arch == "arm64" {
			return ClientPlatformWindowsArm64
		}
		return ClientPlatformWindowsX64
	case "linux":
		if arch == "arm64" {
			return ClientPlatformLinuxArm64
		}
		return ClientPlatformLinuxX64
	default:
		return ClientPlatformUnknown
	}
}

func headerValue(h http.Header, key string) string {
	if h == nil {
		return ""
	}
	return h.Get(key)
}

// ClientIdentityStatField 是观察期「HTTP 身份」计数的字段名：
//
//	{平台}|{runtime}|{runtime_version}|{package_version}|{cli_version}
//
// 用来从真实下游 CC 的请求头里收集各平台的 stainless 身份组合（分池第二阶段要给
// Windows / Linux 账号配的头，就是这里看到的值），不需要在对应系统上跑客户端。
// 只记头不记正文；值来自客户端，每段做长度与字符清洗，避免脏字段撑爆哈希。
func ClientIdentityStatField(headers http.Header, platform ClientPlatform) string {
	if headers == nil || platform == ClientPlatformUnknown {
		return ""
	}
	cli := ""
	if m := claudeCLIVersionRe.FindStringSubmatch(headers.Get("User-Agent")); len(m) == 2 {
		cli = m[1]
	}
	parts := []string{
		string(platform),
		sanitizeStatSegment(headers.Get("X-Stainless-Runtime")),
		sanitizeStatSegment(headers.Get("X-Stainless-Runtime-Version")),
		sanitizeStatSegment(headers.Get("X-Stainless-Package-Version")),
		sanitizeStatSegment(cli),
	}
	return strings.Join(parts, "|")
}

var (
	claudeCLIVersionRe = regexp.MustCompile(`^claude-cli/(\d+\.\d+\.\d+)`)
	statSegmentBadRe   = regexp.MustCompile(`[^A-Za-z0-9._-]`)
)

const statSegmentMaxLen = 32

func sanitizeStatSegment(v string) string {
	v = statSegmentBadRe.ReplaceAllString(strings.TrimSpace(v), "")
	if v == "" {
		return "-"
	}
	if len(v) > statSegmentMaxLen {
		v = v[:statSegmentMaxLen]
	}
	return v
}

// ClientPlatformStatField 是观察期计数的字段名：{平台|unknown}|{来源}|{cc|other}。
func ClientPlatformStatField(obs ClientPlatformObservation, isClaudeCode bool) string {
	platform := string(obs.Platform)
	if platform == "" {
		platform = "unknown"
	}
	client := "other"
	if isClaudeCode {
		client = "cc"
	}
	return platform + "|" + string(obs.Source) + "|" + client
}
