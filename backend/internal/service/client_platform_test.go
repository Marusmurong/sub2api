//go:build unit

package service

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 真实 Claude Code 2.1.263 的 system 环境块形态（本机抓包）。
const ccEnvSystemDarwin = "You are Claude Code...\n\nHere is useful information about the environment you are working in:\n<env>\nWorking directory: /Users/x/proj\nIs directory a git repo: Yes\nPlatform: darwin\nOS Version: Darwin 24.6.0\nShell: zsh\n</env>\nYou are powered by the model named Opus."

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestClassifyClientPlatform(t *testing.T) {
	cases := []struct {
		name string
		h    http.Header
		body string
		want ClientPlatformObservation
	}{
		{
			name: "env 块 darwin + 头 arm64",
			h:    hdr("X-Stainless-OS", "MacOS", "X-Stainless-Arch", "arm64"),
			body: `{"system":[{"type":"text","text":"billing"},{"type":"text","text":` + jsonStr(ccEnvSystemDarwin) + `}]}`,
			want: ClientPlatformObservation{Platform: ClientPlatformMacOSArm64, OS: "darwin", Arch: "arm64", Source: ClientPlatformSourceEnvBlock},
		},
		{
			name: "env 块 win32，头说 x64",
			h:    hdr("X-Stainless-OS", "Windows", "X-Stainless-Arch", "x64"),
			body: `{"system":"<env>\nWorking directory: C:\\proj\nPlatform: win32\nOS Version: Windows 10\nShell: powershell\n</env>"}`,
			want: ClientPlatformObservation{Platform: ClientPlatformWindowsX64, OS: "win32", Arch: "x64", Source: ClientPlatformSourceEnvBlock},
		},
		{
			name: "env 块优先于头（头被网关自己的覆盖污染过也不怕）",
			h:    hdr("X-Stainless-OS", "MacOS", "X-Stainless-Arch", "arm64"),
			body: `{"system":"<env>\nPlatform: linux\n</env>"}`,
			want: ClientPlatformObservation{Platform: ClientPlatformLinuxArm64, OS: "linux", Arch: "arm64", Source: ClientPlatformSourceEnvBlock},
		},
		{
			name: "没有 env 块，只有头",
			h:    hdr("X-Stainless-OS", "Linux", "X-Stainless-Arch", "x86_64"),
			body: `{"system":"plain prompt"}`,
			want: ClientPlatformObservation{Platform: ClientPlatformLinuxX64, OS: "linux", Arch: "x64", Source: ClientPlatformSourceHeader},
		},
		{
			name: "头没有 arch 时按 OS 推断：macOS → arm64",
			h:    hdr("X-Stainless-OS", "MacOS"),
			body: `{}`,
			want: ClientPlatformObservation{Platform: ClientPlatformMacOSArm64, OS: "darwin", Arch: "arm64", Source: ClientPlatformSourceHeader},
		},
		{
			name: "头没有 arch 时按 OS 推断：Windows → x64",
			h:    hdr("X-Stainless-OS", "Windows"),
			body: `{}`,
			want: ClientPlatformObservation{Platform: ClientPlatformWindowsX64, OS: "win32", Arch: "x64", Source: ClientPlatformSourceHeader},
		},
		{
			name: "非 CC 客户端：什么都没有",
			h:    hdr("User-Agent", "opencode/1.18.27"),
			body: `{"system":"you are a helper"}`,
			want: ClientPlatformObservation{Source: ClientPlatformSourceNone},
		},
		{
			name: "messages 里提到 Platform: win32 不算数（只解析 system）",
			h:    hdr(),
			body: `{"system":"x","messages":[{"role":"user","content":"my env shows\nPlatform: win32\nwhy?"}]}`,
			want: ClientPlatformObservation{Source: ClientPlatformSourceNone},
		},
		{
			name: "没有 <env> 标签时退化为 system 整行匹配",
			h:    hdr(),
			body: `{"system":[{"type":"text","text":"Working directory: /x\nPlatform: darwin\nShell: zsh"}]}`,
			want: ClientPlatformObservation{Platform: ClientPlatformMacOSArm64, OS: "darwin", Arch: "arm64", Source: ClientPlatformSourceEnvBlock},
		},
		{
			// 2.1.26x 真实形态：环境信息在主 system 提示词末尾的「# Environment」段，
			// 列表项写法，前面还有几十 KB 的正文。
			name: "2.1.26x 的 # Environment 列表项形态，且位于长提示词末尾",
			h:    hdr("X-Stainless-OS", "MacOS", "X-Stainless-Arch", "arm64"),
			body: `{"system":[{"type":"text","text":"billing"},{"type":"text","text":` + jsonStr(strings.Repeat("You are Claude Code. ", 3000)+"\n# Environment\nYou have been invoked in the following environment: \n - Primary working directory: /Users/x/proj\n - Is a git repository: true\n - Platform: win32\n - Shell: powershell\n - OS Version: Windows 11\n") + `}]}`,
			want: ClientPlatformObservation{Platform: ClientPlatformWindowsArm64, OS: "win32", Arch: "arm64", Source: ClientPlatformSourceEnvBlock},
		},
		{
			name: "Intel Mac",
			h:    hdr("X-Stainless-Arch", "x64"),
			body: `{"system":"<env>\nPlatform: darwin\n</env>"}`,
			want: ClientPlatformObservation{Platform: ClientPlatformMacOSX64, OS: "darwin", Arch: "x64", Source: ClientPlatformSourceEnvBlock},
		},
		{
			name: "空 body 与 nil 头不炸",
			h:    nil,
			body: ``,
			want: ClientPlatformObservation{Source: ClientPlatformSourceNone},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, ClassifyClientPlatform(tc.h, []byte(tc.body)))
		})
	}
}

func TestClientIdentityStatField(t *testing.T) {
	h := hdr("User-Agent", "claude-cli/2.1.263 (external, cli)", "X-Stainless-Runtime", "node",
		"X-Stainless-Runtime-Version", "v26.3.0", "X-Stainless-Package-Version", "0.112.1")
	require.Equal(t, "windows-x64|node|v26.3.0|0.112.1|2.1.263", ClientIdentityStatField(h, ClientPlatformWindowsX64))

	// 缺头补 "-"；脏字符与超长被清洗；未知平台返回空
	dirty := hdr("User-Agent", "curl/8", "X-Stainless-Runtime", "no|de x", "X-Stainless-Runtime-Version", strings.Repeat("v", 50))
	require.Equal(t, "linux-x64|nodex|"+strings.Repeat("v", 32)+"|-|-", ClientIdentityStatField(dirty, ClientPlatformLinuxX64))
	require.Equal(t, "", ClientIdentityStatField(h, ClientPlatformUnknown))
	require.Equal(t, "", ClientIdentityStatField(nil, ClientPlatformWindowsX64))
}

func TestClientPlatformStatField(t *testing.T) {
	require.Equal(t, "macos-arm64|env_block|cc",
		ClientPlatformStatField(ClientPlatformObservation{Platform: ClientPlatformMacOSArm64, Source: ClientPlatformSourceEnvBlock}, true))
	require.Equal(t, "unknown|none|other",
		ClientPlatformStatField(ClientPlatformObservation{Source: ClientPlatformSourceNone}, false))
}

func jsonStr(s string) string {
	b := []byte{'"'}
	for _, r := range s {
		switch r {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		case '\n':
			b = append(b, '\\', 'n')
		default:
			b = append(b, string(r)...)
		}
	}
	return string(append(b, '"'))
}
