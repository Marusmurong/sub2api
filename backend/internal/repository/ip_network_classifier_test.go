package repository

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 响应体照抄 2026-09-16 对 24.249.245.231 的实测输出，只裁掉无关字段。
const proxyCheckCoxResponse = `{
  "status": "ok",
  "24.249.245.231": {
    "asn": "AS22773",
    "range": "24.249.245.231/26",
    "hostname": "wsip-24-249-245-231.oc.oc.cox.net",
    "provider": "Cox Communications Inc.",
    "organisation": "Cox Communications Inc",
    "vpn": "no",
    "proxy": "no",
    "type": "Wireless"
  }
}`

func TestProxyCheckClassifier_ParsesDynamicIPKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(proxyCheckCoxResponse))
	}))
	defer server.Close()

	c := &proxyCheckClassifier{timeout: 5 * time.Second, endpoint: server.URL + "/"}
	info, err := c.ClassifyIP(context.Background(), "24.249.245.231")

	require.NoError(t, err)
	require.Equal(t, service.IPNetworkTypeResidential, info.Type)
	require.Equal(t, service.IPNetworkSourceProxyCheck, info.Source)
	require.Equal(t, "Wireless", info.RawType)
	require.Equal(t, "Cox Communications Inc.", info.ISP)
	require.Equal(t, "AS22773", info.ASN)
}

// vpn/proxy 标记独立于 type：住宅 IP 同时被标为 VPN 出口时，VPN 才是对上游更重要的特征。
func TestProxyCheckClassifier_VPNFlagOverridesType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","1.2.3.4":{"type":"Residential","vpn":"yes","proxy":"no","provider":"Someone"}}`))
	}))
	defer server.Close()

	c := &proxyCheckClassifier{timeout: 5 * time.Second, endpoint: server.URL + "/"}
	info, err := c.ClassifyIP(context.Background(), "1.2.3.4")

	require.NoError(t, err)
	require.Equal(t, service.IPNetworkTypeVPN, info.Type)
}

// 额度耗尽/拒绝必须是 error，不能降级成「未知」——否则全部代理会静默掉分。
func TestProxyCheckClassifier_RejectedStatusIsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"denied","message":"Daily query limit reached"}`))
	}))
	defer server.Close()

	c := &proxyCheckClassifier{timeout: 5 * time.Second, endpoint: server.URL + "/"}
	_, err := c.ClassifyIP(context.Background(), "1.2.3.4")

	require.Error(t, err)
	require.Contains(t, err.Error(), "Daily query limit reached")
}

func TestProxyCheckClassifier_MissingRecordIsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	c := &proxyCheckClassifier{timeout: 5 * time.Second, endpoint: server.URL + "/"}
	_, err := c.ClassifyIP(context.Background(), "1.2.3.4")
	require.Error(t, err)
}

// 响应体照抄 2026-09-16 对 130.117.139.191（Cogent 机房）的实测输出。
// 关键点：threat.is_datacenter 是 false，只有 asn.type 认出了它。
const ipDataCogentResponse = `{
  "ip": "130.117.139.191",
  "asn": {
    "asn": "AS174",
    "name": "Cogent Communications, LLC",
    "domain": null,
    "route": "130.117.139.0/24",
    "type": "internet_backbone"
  },
  "threat": {"is_datacenter": false, "is_proxy": false}
}`

func TestIPDataClassifier_BackboneIsHosting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(ipDataCogentResponse))
	}))
	defer server.Close()

	c := &ipDataClassifier{apiKey: "test-key", timeout: 5 * time.Second, endpoint: server.URL + "/"}
	info, err := c.ClassifyIP(context.Background(), "130.117.139.191")

	require.NoError(t, err)
	require.Equal(t, service.IPNetworkTypeHosting, info.Type,
		"is_datacenter=false 不可信，判据必须是 asn.type")
	require.Equal(t, "internet_backbone", info.RawType)
	require.Equal(t, "AS174", info.ASN)
}

func TestIPDataClassifier_InvalidKeyIsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message": "You have not provided a valid API Key."}`))
	}))
	defer server.Close()

	c := &ipDataClassifier{apiKey: "bad", timeout: 5 * time.Second, endpoint: server.URL + "/"}
	_, err := c.ClassifyIP(context.Background(), "1.2.3.4")

	require.Error(t, err)
	require.Contains(t, err.Error(), "valid API Key")
}

func TestIPDataClassifier_NoKeyIsError(t *testing.T) {
	c := &ipDataClassifier{timeout: time.Second}
	_, err := c.ClassifyIP(context.Background(), "1.2.3.4")
	require.Error(t, err)
}

// --- 组合分类器 ---

type stubClassifier struct {
	info *service.IPNetworkInfo
	err  error
}

func (s stubClassifier) ClassifyIP(context.Context, string) (*service.IPNetworkInfo, error) {
	return s.info, s.err
}

// 这条守的是整个功能的核心：Cogent 在 proxycheck 是 Business（商业，只扣 10 分），
// 在 ipdata 是 internet_backbone（机房，扣 22 分）。取保守值才不会把机房 IP 放过去。
func TestCompositeClassifier_TakesMostSevere(t *testing.T) {
	c := &compositeClassifier{classifiers: []service.IPNetworkClassifier{
		stubClassifier{info: &service.IPNetworkInfo{Type: service.IPNetworkTypeBusiness, Source: service.IPNetworkSourceProxyCheck, ISP: "Cogent Communications"}},
		stubClassifier{info: &service.IPNetworkInfo{Type: service.IPNetworkTypeHosting, Source: service.IPNetworkSourceIPData, ASN: "AS174"}},
	}}

	info, err := c.ClassifyIP(context.Background(), "130.117.139.191")

	require.NoError(t, err)
	require.Equal(t, service.IPNetworkTypeHosting, info.Type)
	require.Equal(t, "proxycheck+ipdata", info.Source, "两家都答了要记下来")
	require.Equal(t, "Cogent Communications", info.ISP, "缺失字段从另一个源补齐")
	require.Equal(t, "AS174", info.ASN)
}

func TestCompositeClassifier_OneSourceFailsOtherWins(t *testing.T) {
	c := &compositeClassifier{classifiers: []service.IPNetworkClassifier{
		stubClassifier{err: errors.New("quota exhausted")},
		stubClassifier{info: &service.IPNetworkInfo{Type: service.IPNetworkTypeResidential, Source: service.IPNetworkSourceIPData}},
	}}

	info, err := c.ClassifyIP(context.Background(), "1.2.3.4")

	require.NoError(t, err)
	require.Equal(t, service.IPNetworkTypeResidential, info.Type)
	require.Equal(t, service.IPNetworkSourceIPData, info.Source, "单源命中时不拼接来源")
}

// 全部源都失败 → error（调用方按「问不到」处理，不追加检测项、不扣分）。
func TestCompositeClassifier_AllFailReturnsError(t *testing.T) {
	c := &compositeClassifier{classifiers: []service.IPNetworkClassifier{
		stubClassifier{err: errors.New("boom")},
		stubClassifier{err: errors.New("bang")},
	}}

	_, err := c.ClassifyIP(context.Background(), "1.2.3.4")

	require.Error(t, err)
	require.Contains(t, err.Error(), "boom")
	require.Contains(t, err.Error(), "bang")
}

// 都答了但都归不了类 → unknown 而非 error（调用方按 warn 处理）。
func TestCompositeClassifier_AllUnknownReturnsUnknown(t *testing.T) {
	c := &compositeClassifier{classifiers: []service.IPNetworkClassifier{
		stubClassifier{info: &service.IPNetworkInfo{Type: service.IPNetworkTypeUnknown}},
		stubClassifier{info: &service.IPNetworkInfo{Type: service.IPNetworkTypeUnknown}},
	}}

	info, err := c.ClassifyIP(context.Background(), "1.2.3.4")

	require.NoError(t, err)
	require.Equal(t, service.IPNetworkTypeUnknown, info.Type)
}

func TestNormalizeClassifierProviders(t *testing.T) {
	require.Equal(t, []string{"proxycheck", "ipdata"}, normalizeClassifierProviders(""))
	require.Equal(t, []string{"ipdata"}, normalizeClassifierProviders(" IPData "))
	require.Equal(t, []string{"proxycheck", "ipdata"}, normalizeClassifierProviders("proxycheck, ipdata ,proxycheck"))
}
