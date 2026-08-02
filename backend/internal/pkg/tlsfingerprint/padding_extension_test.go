//go:build unit

package tlsfingerprint

import (
	"testing"

	utls "github.com/refraction-networking/utls"
)

// padding(21) 必须构造成 UtlsPaddingExtension，不能落到 default 的 GenericExtension。
//
// 背景（2026-08-02 实测）：Claude Code 2.1.220 是 Bun 1.4.0 编译的原生二进制，
// 其 ClientHello 扩展序列尾部带 21：
//
//	[0 23 65281 10 11 35 16 5 13 18 51 45 43 21]
//
// 我们此前不发这一项，JA3 把扩展 ID 全算进摘要，缺一个就是另一个哈希。
// 若靠 default 分支发 GenericExtension，得到的是长度为 0 的 21 号扩展——
// 字节形态依然不对，等于没修。
func TestBuildClientHelloSpec_PaddingUsesRealPaddingExtension(t *testing.T) {
	spec := buildClientHelloSpecFromProfile(&Profile{
		Name:       "cc-2.1.220",
		Extensions: []uint16{0, 23, 65281, 10, 11, 35, 16, 5, 13, 18, 51, 45, 43, 21},
	})
	if spec == nil {
		t.Fatal("spec 为 nil")
	}

	last := spec.Extensions[len(spec.Extensions)-1]
	padding, ok := last.(*utls.UtlsPaddingExtension)
	if !ok {
		t.Fatalf("扩展 21 构造成了 %T，应为 *utls.UtlsPaddingExtension", last)
	}
	if padding.GetPaddingLen == nil {
		t.Fatal("GetPaddingLen 为 nil —— 不填零的 padding 与空扩展无异")
	}
}

// padding 长度必须复刻 BoringSSL 的规则：只在 0xff < 未填充长度 < 0x200 时补到 0x200。
//
// 用规则本身而不是写死长度，才能在每个请求长度上都与真实客户端做出相同选择：
// 该出现时出现、不该出现时不出现。
func TestBuildClientHelloSpec_PaddingFollowsBoringRule(t *testing.T) {
	spec := buildClientHelloSpecFromProfile(&Profile{
		Name:       "cc-2.1.220",
		Extensions: []uint16{0, 21},
	})
	padding := spec.Extensions[len(spec.Extensions)-1].(*utls.UtlsPaddingExtension)

	tests := []struct {
		name        string
		unpaddedLen int
		wantPadding bool
		wantTotal   int // 仅在 wantPadding 时校验
	}{
		{name: "短于区间不补", unpaddedLen: 0x80, wantPadding: false},
		{name: "区间下界内补到 512", unpaddedLen: 0x100, wantPadding: true, wantTotal: 0x200},
		{name: "区间中部补到 512", unpaddedLen: 0x180, wantPadding: true, wantTotal: 0x200},
		{name: "达到 512 不补", unpaddedLen: 0x200, wantPadding: false},
		{name: "超过区间不补", unpaddedLen: 0x300, wantPadding: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLen, gotWill := padding.GetPaddingLen(tt.unpaddedLen)
			if gotWill != tt.wantPadding {
				t.Fatalf("是否补齐 = %v，期望 %v", gotWill, tt.wantPadding)
			}
			if !tt.wantPadding {
				return
			}
			// 扩展头 4 字节 + 填充体，合计应把 ClientHello 顶到 0x200。
			if total := tt.unpaddedLen + 4 + gotLen; total != tt.wantTotal {
				t.Fatalf("补齐后总长 = %d，期望 %d", total, tt.wantTotal)
			}
		})
	}
}

// 扩展顺序必须严格按 profile 给定的序列，JA3 对顺序敏感。
func TestBuildClientHelloSpec_PreservesExtensionOrder(t *testing.T) {
	order := []uint16{0, 23, 65281, 10, 11, 35, 16, 5, 13, 18, 51, 45, 43, 21}
	spec := buildClientHelloSpecFromProfile(&Profile{Name: "cc", Extensions: order})
	if got := len(spec.Extensions); got != len(order) {
		t.Fatalf("扩展数量 = %d，期望 %d", got, len(order))
	}
	// 逐位置抽查几个有代表性的：首位 SNI、中间 ALPN、末位 padding。
	if _, ok := spec.Extensions[0].(*utls.SNIExtension); !ok {
		t.Fatalf("首位应为 SNI，实际 %T", spec.Extensions[0])
	}
	if _, ok := spec.Extensions[6].(*utls.ALPNExtension); !ok {
		t.Fatalf("第 7 位应为 ALPN，实际 %T", spec.Extensions[6])
	}
	if _, ok := spec.Extensions[13].(*utls.UtlsPaddingExtension); !ok {
		t.Fatalf("末位应为 padding，实际 %T", spec.Extensions[13])
	}
}

// 未知扩展仍应走 GenericExtension，不能因为新增 case 21 而改变这条兜底行为。
func TestBuildClientHelloSpec_UnknownExtensionStillGeneric(t *testing.T) {
	spec := buildClientHelloSpecFromProfile(&Profile{Name: "x", Extensions: []uint16{0, 22}})
	last := spec.Extensions[len(spec.Extensions)-1]
	generic, ok := last.(*utls.GenericExtension)
	if !ok {
		t.Fatalf("未知扩展 22 应为 GenericExtension，实际 %T", last)
	}
	if generic.Id != 22 {
		t.Fatalf("GenericExtension.Id = %d，期望 22", generic.Id)
	}
}
