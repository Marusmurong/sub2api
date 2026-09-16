package service

import "strings"

// 出口 IP 的网络类型识别。
//
// 为什么需要：一个上游订阅账号挂在机房 IP 上，本身就是「这不是个人用户」的直白特征。
// 2026-09-08 的账号 216 挂在 130.117.139.191（AS174 Cogent，纯机房）上，6.4 小时被撤销——
// 而这个代理在原有质量检测里（连通性 + 四个 AI 端点可达性）全部通过，风险完全看不出来，
// 只能靠人在代理名字里手写「机房勿用」。
//
// 这里只做分类与打标，不改调度：不因为 IP 类型自动禁用或降权代理。

type IPNetworkType string

const (
	IPNetworkTypeResidential IPNetworkType = "residential"
	IPNetworkTypeMobile      IPNetworkType = "mobile"
	IPNetworkTypeBusiness    IPNetworkType = "business"
	IPNetworkTypeHosting     IPNetworkType = "hosting"
	IPNetworkTypeVPN         IPNetworkType = "vpn"
	IPNetworkTypeUnknown     IPNetworkType = "unknown"
)

// 分类来源。区分「第三方权威判定」与「本地按名称猜的」，排查时需要知道可信度。
const (
	IPNetworkSourceProxyCheck   = "proxycheck"
	IPNetworkSourceIPData       = "ipdata"
	IPNetworkSourceProviderName = "provider_name"
)

// ipNetworkTypeSeverity 给类型排序，数值越大越「不像个人订阅」。
// 多数据源结果不一致时取较大者，见 MergeIPNetworkInfo。
func ipNetworkTypeSeverity(t IPNetworkType) int {
	switch t {
	case IPNetworkTypeResidential, IPNetworkTypeMobile:
		return 1
	case IPNetworkTypeBusiness:
		return 2
	case IPNetworkTypeHosting, IPNetworkTypeVPN:
		return 3
	default: // unknown
		return 0
	}
}

// MergeIPNetworkInfo 合并多个数据源的判定，取最保守（severity 最高）的那个。
//
// 为什么要取保守值而不是多数表决：2026-09-16 实测，130.117.139.191（AS174 Cogent，
// 确凿的机房）在 proxycheck 是 Business、在 ipdata 的 is_datacenter 是 false，只有
// ipdata 的 asn.type=internet_backbone 抓住了它。任何一家漏判都会让机房 IP 显示成
// 「商业」甚至「住宅」——而这个功能的全部意义就是别漏掉机房 IP。
//
// 全为 nil 或全部 unknown 时返回 nil：调用方据此区分「问不到」与「问了但归不了类」。
func MergeIPNetworkInfo(infos ...*IPNetworkInfo) *IPNetworkInfo {
	var best *IPNetworkInfo
	sources := make([]string, 0, len(infos))
	for _, info := range infos {
		if info == nil || info.Type == IPNetworkTypeUnknown {
			continue
		}
		sources = append(sources, info.Source)
		if best == nil || ipNetworkTypeSeverity(info.Type) > ipNetworkTypeSeverity(best.Type) {
			best = info
		}
	}
	if best == nil {
		return nil
	}
	merged := *best
	// 多源一致时把来源都记下来，排查时能看出这个判定有几家背书。
	if len(sources) > 1 {
		merged.Source = strings.Join(sources, "+")
	}
	// 缺失字段从其它源补齐，保证前端 tooltip 有内容。
	for _, info := range infos {
		if info == nil {
			continue
		}
		if merged.ISP == "" {
			merged.ISP = info.ISP
		}
		if merged.Org == "" {
			merged.Org = info.Org
		}
		if merged.ASN == "" {
			merged.ASN = info.ASN
		}
		if merged.ASName == "" {
			merged.ASName = info.ASName
		}
	}
	return &merged
}

// NormalizeIPDataASNType 把 ipdata.co 的 asn.type 映射到本地枚举。
//
// 实测取值（2026-09-16）：Cogent → internet_backbone，Cox/MxFiber/FPT → isp，
// 14.225.20.224 → gov（ipdata 认为它属于河内科技厅，与 ip-api 报的 VNPT 不一致）。
//
// 注意不要用 ipdata 的 threat.is_datacenter：它对 Cogent AS174 返回 false，不可靠。
func NormalizeIPDataASNType(raw string) IPNetworkType {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "isp":
		return IPNetworkTypeResidential
	case "hosting", "internet_backbone", "cdn":
		return IPNetworkTypeHosting
	case "business", "education", "gov", "government", "org", "organization", "banking":
		return IPNetworkTypeBusiness
	default:
		return IPNetworkTypeUnknown
	}
}

// IPNetworkInfo 是一次出口 IP 分类的完整结果。
type IPNetworkInfo struct {
	Type    IPNetworkType `json:"type"`
	Source  string        `json:"source"`
	RawType string        `json:"raw_type,omitempty"` // 数据源的原始取值，便于对账
	ISP     string        `json:"isp,omitempty"`
	Org     string        `json:"org,omitempty"`
	ASN     string        `json:"asn,omitempty"`
	ASName  string        `json:"as_name,omitempty"`
}

// NormalizeProxyCheckType 把 proxycheck.io 的 type 取值映射到本地枚举。
//
// 取值来自 2026-09-16 对生产代理的实测：Cogent → "Business"、Cox → "Wireless"、
// MxFiber → "Residential"。Wireless 归住宅而非移动：它指的是固定无线宽带接入
// （Cox 这类有线运营商的家庭宽带），不是蜂窝网络。
func NormalizeProxyCheckType(raw string) IPNetworkType {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "residential", "wireless", "fixed line isp", "isp":
		return IPNetworkTypeResidential
	case "mobile", "cellular":
		return IPNetworkTypeMobile
	case "business", "corporate", "organization", "education", "government":
		return IPNetworkTypeBusiness
	case "hosting", "compromised server", "data center", "datacenter", "cdn", "search engine robot":
		return IPNetworkTypeHosting
	case "vpn", "tor", "proxy", "openvpn", "socks", "socks4", "socks5", "web proxy", "public proxy":
		return IPNetworkTypeVPN
	default:
		return IPNetworkTypeUnknown
	}
}

// 关键词表按「先判机房、再判住宅」的顺序使用：机房词更具排他性（Cogent、OVH 这些
// 不会同时是家宽），而住宅词里的 "Communications"、"Telecom" 在机房厂商名里也常出现。
var (
	hostingProviderKeywords = []string{
		"cogent", "digitalocean", "digital ocean", "amazon", "aws", "google cloud", "gcp",
		"azure", "microsoft corporation", "ovh", "hetzner", "linode", "vultr", "choopa",
		"m247", "leaseweb", "contabo", "scaleway", "datacamp", "packet host", "equinix",
		"oracle cloud", "alibaba cloud", "tencent cloud", "cloudflare", "fastly", "akamai",
		"colocation", "datacenter", "data center", "hosting", "vps", "dedicated server",
		"server", "cloud",
	}
	residentialProviderKeywords = []string{
		"comcast", "cox communications", "charter", "spectrum", "verizon", "at&t",
		"centurylink", "frontier communications", "optimum", "altice", "t-mobile",
		"vnpt", "vietnam posts", "fpt telecom", "viettel",
		"broadband", "cable", "fiber", "fibre", "dsl", "telecom", "telekom",
		"telecommunications", "communications inc",
	}
)

// ClassifyByProviderName 在第三方数据源不可用时，按 ISP / org / AS 名称猜网络类型。
//
// 这是**兜底**，不是主判据：小众 ISP（例如 MxFiber LLC）在两张表里都不命中，返回 unknown。
// 宁可返回 unknown，也不要把拿不准的机房 IP 猜成住宅——后者会掩盖真实风险。
func ClassifyByProviderName(names ...string) IPNetworkType {
	joined := strings.ToLower(strings.Join(names, " "))
	if strings.TrimSpace(joined) == "" {
		return IPNetworkTypeUnknown
	}
	for _, kw := range hostingProviderKeywords {
		if strings.Contains(joined, kw) {
			return IPNetworkTypeHosting
		}
	}
	for _, kw := range residentialProviderKeywords {
		if strings.Contains(joined, kw) {
			return IPNetworkTypeResidential
		}
	}
	return IPNetworkTypeUnknown
}

// ipNetworkTypeQualityStatus 把网络类型翻译成质量检测项的状态与说明。
//
// 评分权重沿用 finalizeProxyQualityResult 的既有公式：warn -10、fail -22。
// 机房 / VPN 记 fail，是因为它对「账号看起来像个人订阅」这件事是直接否定的。
func ipNetworkTypeQualityStatus(t IPNetworkType) (status string, message string) {
	switch t {
	case IPNetworkTypeResidential:
		return "pass", "出口为家庭住宅 IP"
	case IPNetworkTypeMobile:
		return "pass", "出口为移动网络 IP"
	case IPNetworkTypeBusiness:
		return "warn", "出口为商业专线 IP，非家庭住宅"
	case IPNetworkTypeHosting:
		return "fail", "出口为机房 / 数据中心 IP"
	case IPNetworkTypeVPN:
		return "fail", "出口被标记为 VPN / 代理 IP"
	default:
		return "warn", "无法判定出口 IP 类型"
	}
}
