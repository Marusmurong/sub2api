package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 取值取自 2026-09-16 对生产代理的实测，不是构造出来的样本。
func TestNormalizeProxyCheckType(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want IPNetworkType
	}{
		{name: "Cogent 机房实测返回 Business", raw: "Business", want: IPNetworkTypeBusiness},
		{name: "Cox 家宽实测返回 Wireless", raw: "Wireless", want: IPNetworkTypeResidential},
		{name: "MxFiber 实测返回 Residential", raw: "Residential", want: IPNetworkTypeResidential},
		{name: "Hosting 直接判机房", raw: "Hosting", want: IPNetworkTypeHosting},
		{name: "被攻陷的服务器同样按机房处理", raw: "Compromised Server", want: IPNetworkTypeHosting},
		{name: "VPN", raw: "VPN", want: IPNetworkTypeVPN},
		{name: "TOR", raw: "TOR", want: IPNetworkTypeVPN},
		{name: "Mobile", raw: "Mobile", want: IPNetworkTypeMobile},
		{name: "大小写与空白不敏感", raw: "  rEsIdEnTiAl  ", want: IPNetworkTypeResidential},
		{name: "空串", raw: "", want: IPNetworkTypeUnknown},
		{name: "没见过的取值不瞎猜", raw: "Something New", want: IPNetworkTypeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, NormalizeProxyCheckType(tc.raw))
		})
	}
}

func TestClassifyByProviderName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		names []string
		want  IPNetworkType
	}{
		{
			name:  "Cogent 命中机房词",
			names: []string{"Cogent Communications", "AS174 Cogent Communications, LLC"},
			want:  IPNetworkTypeHosting,
		},
		{
			name:  "Cox 命中住宅词",
			names: []string{"Cox Communications Inc.", "AS22773 Cox Communications Inc."},
			want:  IPNetworkTypeResidential,
		},
		{
			name:  "VNPT 命中住宅词",
			names: []string{"Vietnam Posts and Telecommunications Group", "AS135905"},
			want:  IPNetworkTypeResidential,
		},
		{
			name:  "FPT 命中住宅词",
			names: []string{"FPT Telecom Company", "AS18403 FPT Telecom Company"},
			want:  IPNetworkTypeResidential,
		},
		{
			// MxFiber LLC 靠 "fiber" 命中住宅词，与 proxycheck 实测的 Residential 一致。
			name:  "MxFiber 靠 fiber 命中住宅，与第三方判定一致",
			names: []string{"MxFiber LLC", "AS13875 MxFiber LLC"},
			want:  IPNetworkTypeResidential,
		},
		{
			// 兜底表的真实局限：名称里没有任何特征词时不硬猜。
			// 宁可 unknown 也不要猜成住宅——猜错会掩盖真实风险。
			name:  "两张表都不命中时返回 unknown 而不是硬猜",
			names: []string{"Upstart Network, Inc.", "AS64512"},
			want:  IPNetworkTypeUnknown,
		},
		{
			// 机房词优先级高于住宅词：DigitalOcean 名字里没有住宅词，
			// 但很多机房厂商名里带 Communications，顺序反了就会误判。
			name:  "同时命中两类时机房优先",
			names: []string{"Example Cloud Communications Inc"},
			want:  IPNetworkTypeHosting,
		},
		{name: "全空返回 unknown", names: []string{"", "  "}, want: IPNetworkTypeUnknown},
		{name: "无入参返回 unknown", names: nil, want: IPNetworkTypeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, ClassifyByProviderName(tc.names...))
		})
	}
}

// 取值同样取自 2026-09-16 实测。关键一条是 internet_backbone：
// Cogent 在 proxycheck 只是 Business，唯一识破它是机房的就是这个值。
func TestNormalizeIPDataASNType(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want IPNetworkType
	}{
		{name: "Cogent 实测 internet_backbone", raw: "internet_backbone", want: IPNetworkTypeHosting},
		{name: "Cox / MxFiber / FPT 实测 isp", raw: "isp", want: IPNetworkTypeResidential},
		{name: "14.225.20.224 实测 gov", raw: "gov", want: IPNetworkTypeBusiness},
		{name: "hosting", raw: "hosting", want: IPNetworkTypeHosting},
		{name: "cdn 归机房", raw: "cdn", want: IPNetworkTypeHosting},
		{name: "education 归商业", raw: "education", want: IPNetworkTypeBusiness},
		{name: "大小写不敏感", raw: "ISP", want: IPNetworkTypeResidential},
		{name: "空串", raw: "", want: IPNetworkTypeUnknown},
		{name: "没见过的取值不瞎猜", raw: "satellite", want: IPNetworkTypeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, NormalizeIPDataASNType(tc.raw))
		})
	}
}

func TestMergeIPNetworkInfo_TakesMostSevere(t *testing.T) {
	residential := &IPNetworkInfo{Type: IPNetworkTypeResidential, Source: "a"}
	business := &IPNetworkInfo{Type: IPNetworkTypeBusiness, Source: "b"}
	hosting := &IPNetworkInfo{Type: IPNetworkTypeHosting, Source: "c"}

	require.Equal(t, IPNetworkTypeHosting, MergeIPNetworkInfo(residential, business, hosting).Type)
	require.Equal(t, IPNetworkTypeBusiness, MergeIPNetworkInfo(residential, business).Type)
	require.Equal(t, IPNetworkTypeResidential, MergeIPNetworkInfo(residential).Type)

	// unknown 参与合并时被忽略，不拉低也不拉高。
	unknown := &IPNetworkInfo{Type: IPNetworkTypeUnknown, Source: "d"}
	merged := MergeIPNetworkInfo(unknown, residential)
	require.Equal(t, IPNetworkTypeResidential, merged.Type)
	require.Equal(t, "a", merged.Source, "只有一个有效源时不拼接来源")
}

func TestIPNetworkTypeQualityStatus(t *testing.T) {
	for _, tc := range []struct {
		typ        IPNetworkType
		wantStatus string
	}{
		{typ: IPNetworkTypeResidential, wantStatus: "pass"},
		{typ: IPNetworkTypeMobile, wantStatus: "pass"},
		{typ: IPNetworkTypeBusiness, wantStatus: "warn"},
		{typ: IPNetworkTypeHosting, wantStatus: "fail"},
		{typ: IPNetworkTypeVPN, wantStatus: "fail"},
		{typ: IPNetworkTypeUnknown, wantStatus: "warn"},
	} {
		t.Run(string(tc.typ), func(t *testing.T) {
			status, message := ipNetworkTypeQualityStatus(tc.typ)
			require.Equal(t, tc.wantStatus, status)
			require.NotEmpty(t, message, "每种类型都要有给人看的说明")
		})
	}
}
