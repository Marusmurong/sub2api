package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type stubIPNetworkClassifier struct {
	info *IPNetworkInfo
	err  error
}

func (s stubIPNetworkClassifier) ClassifyIP(context.Context, string) (*IPNetworkInfo, error) {
	return s.info, s.err
}

func newQualityResultWithBasePass() *ProxyQualityCheckResult {
	return &ProxyQualityCheckResult{
		Items:       []ProxyQualityCheckItem{{Target: "base_connectivity", Status: "pass"}},
		PassedCount: 1,
	}
}

func findQualityItem(result *ProxyQualityCheckResult, target string) *ProxyQualityCheckItem {
	for i := range result.Items {
		if result.Items[i].Target == target {
			return &result.Items[i]
		}
	}
	return nil
}

func TestAppendIPNetworkTypeItem_HostingFails(t *testing.T) {
	svc := &adminServiceImpl{ipNetworkClassifier: stubIPNetworkClassifier{
		info: &IPNetworkInfo{
			Type:   IPNetworkTypeHosting,
			Source: IPNetworkSourceIPData,
			ISP:    "Cogent Communications, LLC",
			ASN:    "AS174",
		},
	}}
	result := newQualityResultWithBasePass()

	svc.appendIPNetworkTypeItem(context.Background(), result, &ProxyExitInfo{IP: "130.117.139.191"})

	item := findQualityItem(result, proxyQualityTargetIPType)
	require.NotNil(t, item)
	require.Equal(t, "fail", item.Status)
	require.Contains(t, item.Message, "机房")
	require.Contains(t, item.Message, "Cogent Communications, LLC", "说明里要带运营商，方便一眼对账")
	require.Contains(t, item.Message, "AS174")
	require.Equal(t, 1, result.FailedCount)
	require.Equal(t, string(IPNetworkTypeHosting), result.NetworkType)
	require.Equal(t, "AS174", result.ASN)

	// 与既有算分公式联动：fail 扣 22。
	finalizeProxyQualityResult(result)
	require.Equal(t, 78, result.Score)
}

func TestAppendIPNetworkTypeItem_ResidentialPasses(t *testing.T) {
	svc := &adminServiceImpl{ipNetworkClassifier: stubIPNetworkClassifier{
		info: &IPNetworkInfo{Type: IPNetworkTypeResidential, Source: IPNetworkSourceProxyCheck, ISP: "MxFiber LLC"},
	}}
	result := newQualityResultWithBasePass()

	svc.appendIPNetworkTypeItem(context.Background(), result, &ProxyExitInfo{IP: "207.228.201.171"})

	item := findQualityItem(result, proxyQualityTargetIPType)
	require.NotNil(t, item)
	require.Equal(t, "pass", item.Status)
	require.Equal(t, 2, result.PassedCount)

	finalizeProxyQualityResult(result)
	require.Equal(t, 100, result.Score)
}

// 分类器彻底失败且本地兜底也认不出来时：不追加检测项、不扣分。
//
// 这是整个功能里最容易写错的一条。proxycheck 免费层有每日额度，额度用完时若一律
// 按 unknown 记 warn，会让全部代理在同一天静默掉 10 分，看起来像代理集体劣化。
func TestAppendIPNetworkTypeItem_ClassifierUnavailableAddsNothing(t *testing.T) {
	svc := &adminServiceImpl{ipNetworkClassifier: stubIPNetworkClassifier{err: errors.New("quota exhausted")}}
	result := newQualityResultWithBasePass()

	svc.appendIPNetworkTypeItem(context.Background(), result,
		&ProxyExitInfo{IP: "1.2.3.4", ISP: "Upstart Network, Inc."})

	require.Nil(t, findQualityItem(result, proxyQualityTargetIPType))
	require.Equal(t, 1, result.PassedCount)
	require.Zero(t, result.WarnCount)
	require.Zero(t, result.FailedCount)
	require.Empty(t, result.NetworkType)

	finalizeProxyQualityResult(result)
	require.Equal(t, 100, result.Score, "问不到不能扣分")
}

// 第三方挂了但 ip-api 的 ISP 名称能认出来 → 用本地兜底，不产生额外请求。
func TestAppendIPNetworkTypeItem_FallsBackToProviderName(t *testing.T) {
	svc := &adminServiceImpl{ipNetworkClassifier: stubIPNetworkClassifier{err: errors.New("network down")}}
	result := newQualityResultWithBasePass()

	svc.appendIPNetworkTypeItem(context.Background(), result, &ProxyExitInfo{
		IP:     "130.117.139.191",
		ISP:    "Cogent Communications",
		ASN:    "AS174 Cogent Communications, LLC",
		ASName: "COGENT-174",
	})

	item := findQualityItem(result, proxyQualityTargetIPType)
	require.NotNil(t, item)
	require.Equal(t, "fail", item.Status)
	require.Equal(t, IPNetworkSourceProviderName, result.NetworkTypeSource)
}

// 完全没配分类器时也要能走兜底，而不是直接跳过。
func TestAppendIPNetworkTypeItem_NoClassifierStillUsesProviderName(t *testing.T) {
	svc := &adminServiceImpl{}
	result := newQualityResultWithBasePass()

	svc.appendIPNetworkTypeItem(context.Background(), result,
		&ProxyExitInfo{IP: "24.249.245.231", ISP: "Cox Communications Inc."})

	item := findQualityItem(result, proxyQualityTargetIPType)
	require.NotNil(t, item)
	require.Equal(t, "pass", item.Status)
	require.Equal(t, string(IPNetworkTypeResidential), result.NetworkType)
}

// 第三方答了 unknown，但本地按名称能认出来 → 用本地结果，别浪费信息。
func TestAppendIPNetworkTypeItem_UnknownFromSourceFallsBackToName(t *testing.T) {
	svc := &adminServiceImpl{ipNetworkClassifier: stubIPNetworkClassifier{
		info: &IPNetworkInfo{Type: IPNetworkTypeUnknown},
	}}
	result := newQualityResultWithBasePass()

	svc.appendIPNetworkTypeItem(context.Background(), result,
		&ProxyExitInfo{IP: "1.2.3.4", ISP: "Hetzner Online GmbH"})

	item := findQualityItem(result, proxyQualityTargetIPType)
	require.NotNil(t, item)
	require.Equal(t, "fail", item.Status)
	require.Equal(t, string(IPNetworkTypeHosting), result.NetworkType)
}

func TestAppendIPNetworkTypeItem_SkipsWhenNoExitIP(t *testing.T) {
	svc := &adminServiceImpl{}
	result := newQualityResultWithBasePass()

	svc.appendIPNetworkTypeItem(context.Background(), result, &ProxyExitInfo{})
	svc.appendIPNetworkTypeItem(context.Background(), result, nil)

	require.Len(t, result.Items, 1)
}

func TestMergeIPNetworkInfo_AllNilReturnsNil(t *testing.T) {
	require.Nil(t, MergeIPNetworkInfo())
	require.Nil(t, MergeIPNetworkInfo(nil, nil))
	require.Nil(t, MergeIPNetworkInfo(&IPNetworkInfo{Type: IPNetworkTypeUnknown}))
}
