package service

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
)

// ExtraKeyReclaudePassthroughFile 指向同机 reclaude 客户端的 passthrough.json。
//
// 🔴 背景(2026-09-29 逆向 + 真机抓包):真客户端 daemon 每 300s POST /client/telemetry,
// body 的 passthrough_hosts 是它 MITM 观测到的内嵌 claude 直连的非拦截 host
// (static.reclaude.ai / api.ipify.org / ipinfo.io 等),落盘在 ~/.reclaude/passthrough.json。
// sub 此前 telemetry 的 passthrough_hosts 恒空 —— 与真值(有观测)不符。
//
// sub 和 reclaude 同机同用户(ubuntu)时,可直接读这个真值文件填进上报。配置了此键
// (账号 extra 或全局)才启用;未配置回落到空 passthrough(旧行为,不影响非同机部署)。
const ExtraKeyReclaudePassthroughFile = "reclaude_passthrough_file"

// reclaudeDefaultPassthroughFile 是同机 reclaude 客户端 passthrough.json 的默认路径。
//
// 生产 sub 与 reclaude 同机同用户(ubuntu),reclaude 落盘在 ~/.reclaude/passthrough.json。
// 账号 extra 的 reclaude_passthrough_file 可覆盖(多用户/非标准 HOME 时)。
const reclaudeDefaultPassthroughFile = "/home/ubuntu/.reclaude/passthrough.json"

// ReclaudePassthroughFilePath 返回该账号的 passthrough.json 路径。
//
// 优先账号 extra 的显式配置;否则回落默认路径。返回空串表示不读(账号未启用)。
// 🔴 只在账号显式配了 reclaude_passthrough_file、或用默认路径能读到时才有 host;
// 读不到一律降级空 passthrough,不阻断上报。
func ReclaudePassthroughFilePath(account *Account) string {
	if account == nil || !account.IsReclaude() {
		return ""
	}
	if p := strings.TrimSpace(credentialString(account.Extra, ExtraKeyReclaudePassthroughFile)); p != "" {
		return p
	}
	return reclaudeDefaultPassthroughFile
}

// reclaudePassthroughFileRaw 是 ~/.reclaude/passthrough.json 的磁盘结构。
//
// ✅ 真机实测:{"updated_at_ms":..., "no_sni":31, "hosts":{"<host>":{n,listeners,...}}}
// hosts 是 map[host]entry;no_sni 是顶层计数(不进 telemetry body)。
type reclaudePassthroughFileRaw struct {
	Hosts map[string]reclaudePassthroughEntryRaw `json:"hosts"`
}

type reclaudePassthroughEntryRaw struct {
	N          int      `json:"n"`
	Listeners  []string `json:"listeners"`
	Reportable *bool    `json:"reportable"`
}

// LoadReclaudePassthroughHosts 从 passthrough.json 读出可上报的 host 列表。
//
// 只取 reportable != false 的条目(真机字段,缺省视为可报);host 去空、按名排序保证
// 输出稳定。文件不存在/坏 → 返回 nil(降级到空 passthrough,不报错,不阻断心跳)。
func LoadReclaudePassthroughHosts(path string) []ReclaudeTelemetryPassthroughHost {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw reclaudePassthroughFileRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	if len(raw.Hosts) == 0 {
		return nil
	}

	hosts := make([]ReclaudeTelemetryPassthroughHost, 0, len(raw.Hosts))
	for host, entry := range raw.Hosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		if entry.Reportable != nil && !*entry.Reportable {
			continue
		}
		listeners := entry.Listeners
		if len(listeners) == 0 {
			// 真值里 listeners 恒非空(["connect"]);缺失时补默认,避免发出 null。
			listeners = []string{"connect"}
		}
		hosts = append(hosts, ReclaudeTelemetryPassthroughHost{
			Host:      host,
			N:         entry.N,
			Listeners: listeners,
		})
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Host < hosts[j].Host })
	return hosts
}
