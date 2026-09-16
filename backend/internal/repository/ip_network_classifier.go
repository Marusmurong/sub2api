package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// proxycheck.io 出口 IP 分类器。
//
// 选它的原因（2026-09-16 实测）：ip-api.com 免费层的 hosting 字段不可用——
// 130.117.139.191（AS174 Cogent，纯机房）也返回 hosting=false，整列不可信。
// proxycheck 的 type 字段则正确区分了 Cogent=Business / Cox=Wireless / MxFiber=Residential。
//
// 免费额度：无 key 100 次/天，免费 key 1000 次/天。质量检测是人工触发的低频操作，够用；
// 也正因为有额度上限，超限时必须返回 error 而不是「未知」，否则会让所有代理静默掉分。

const (
	proxyCheckEndpoint         = "https://proxycheck.io/v2/"
	proxyCheckDefaultTimeout   = 6 * time.Second
	proxyCheckMaxResponseBytes = int64(64 * 1024)
	proxyCheckStatusOK         = "ok"
)

type proxyCheckClassifier struct {
	apiKey  string
	timeout time.Duration
	// endpoint 仅供测试注入，生产恒为 proxyCheckEndpoint。
	endpoint string
}

// NewIPNetworkClassifier 构造出口 IP 分类器。
//
// 配置了多个数据源时返回组合分类器：并行查询、取最保守的结果。单源漏判是常态——
// Cogent 机房只有 ipdata 抓得到，而 ipdata 的 is_datacenter 又完全不可用，
// 所以「任一源判为非住宅即按非住宅」是这里唯一安全的合并策略。
//
// 全部关闭时返回 nil，调用方需按「无分类器」处理。
func NewIPNetworkClassifier(cfg *config.Config) service.IPNetworkClassifier {
	if cfg == nil {
		return nil
	}
	c := cfg.Security.ProxyProbe.IPClassifier
	if !c.Enabled {
		return nil
	}
	timeout := proxyCheckDefaultTimeout
	if c.TimeoutSeconds > 0 {
		timeout = time.Duration(c.TimeoutSeconds) * time.Second
	}

	var classifiers []service.IPNetworkClassifier
	for _, provider := range normalizeClassifierProviders(c.Provider) {
		switch provider {
		case "proxycheck":
			classifiers = append(classifiers, &proxyCheckClassifier{
				apiKey:   strings.TrimSpace(c.APIKey),
				timeout:  timeout,
				endpoint: proxyCheckEndpoint,
			})
		case "ipdata":
			key := strings.TrimSpace(c.IPDataAPIKey)
			if key == "" {
				continue // 没配 key 就当没开这一路，不报错
			}
			classifiers = append(classifiers, &ipDataClassifier{
				apiKey:   key,
				timeout:  timeout,
				endpoint: ipDataEndpoint,
			})
		}
	}

	switch len(classifiers) {
	case 0:
		return nil
	case 1:
		return classifiers[0]
	default:
		return &compositeClassifier{classifiers: classifiers}
	}
}

// normalizeClassifierProviders 解析逗号分隔的 provider 列表；留空默认两家都用。
func normalizeClassifierProviders(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{"proxycheck", "ipdata"}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		p := strings.ToLower(strings.TrimSpace(part))
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// compositeClassifier 并行查询多个数据源，合并取最保守的结果。
type compositeClassifier struct {
	classifiers []service.IPNetworkClassifier
}

func (c *compositeClassifier) ClassifyIP(ctx context.Context, ip string) (*service.IPNetworkInfo, error) {
	type outcome struct {
		info *service.IPNetworkInfo
		err  error
	}
	results := make([]outcome, len(c.classifiers))
	var wg sync.WaitGroup
	for i, classifier := range c.classifiers {
		wg.Add(1)
		go func(idx int, cl service.IPNetworkClassifier) {
			defer wg.Done()
			info, err := cl.ClassifyIP(ctx, ip)
			results[idx] = outcome{info: info, err: err}
		}(i, classifier)
	}
	wg.Wait()

	infos := make([]*service.IPNetworkInfo, 0, len(results))
	errs := make([]string, 0, len(results))
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, r.err.Error())
			continue
		}
		infos = append(infos, r.info)
	}

	if merged := service.MergeIPNetworkInfo(infos...); merged != nil {
		return merged, nil
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("ip classifier: all sources failed: %s", strings.Join(errs, "; "))
	}
	// 所有源都答了，但都归不了类。返回 unknown 而非 error：
	// 调用方据此记 warn，而不是当成「问不到」。
	return &service.IPNetworkInfo{Type: service.IPNetworkTypeUnknown}, nil
}

// proxyCheckRecord 是响应里以 IP 为键的那个对象。字段名照抄实测响应。
type proxyCheckRecord struct {
	ASN          string `json:"asn"`
	Provider     string `json:"provider"`
	Organisation string `json:"organisation"`
	VPN          string `json:"vpn"`
	Proxy        string `json:"proxy"`
	Type         string `json:"type"`
}

func (c *proxyCheckClassifier) ClassifyIP(ctx context.Context, ip string) (*service.IPNetworkInfo, error) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return nil, fmt.Errorf("proxycheck: empty ip")
	}

	client, err := httpclient.GetClient(httpclient.Options{Timeout: c.timeout})
	if err != nil {
		return nil, fmt.Errorf("proxycheck: build client: %w", err)
	}

	query := url.Values{}
	query.Set("vpn", "3")
	query.Set("asn", "1")
	if c.apiKey != "" {
		query.Set("key", c.apiKey)
	}
	endpoint := c.endpoint
	if endpoint == "" {
		endpoint = proxyCheckEndpoint
	}
	reqURL := endpoint + url.PathEscape(ip) + "?" + query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("proxycheck: build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("proxycheck: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("proxycheck: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, proxyCheckMaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("proxycheck: read response: %w", err)
	}
	if int64(len(body)) > proxyCheckMaxResponseBytes {
		return nil, fmt.Errorf("proxycheck: response exceeds %d bytes", proxyCheckMaxResponseBytes)
	}

	return parseProxyCheckResponse(body, ip)
}

// parseProxyCheckResponse 解析形如 {"status":"ok","<ip>":{...}} 的响应。
// IP 是动态键，所以先整体解成 RawMessage 再按 ip 取值。
func parseProxyCheckResponse(body []byte, ip string) (*service.IPNetworkInfo, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		preview := string(body)
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		return nil, fmt.Errorf("proxycheck: parse response: %w (body: %s)", err, preview)
	}

	var status string
	if raw, ok := envelope["status"]; ok {
		_ = json.Unmarshal(raw, &status)
	}
	if !strings.EqualFold(status, proxyCheckStatusOK) {
		var message string
		if raw, ok := envelope["message"]; ok {
			_ = json.Unmarshal(raw, &message)
		}
		if message == "" {
			message = "status=" + status
		}
		return nil, fmt.Errorf("proxycheck: request rejected: %s", message)
	}

	raw, ok := envelope[ip]
	if !ok {
		return nil, fmt.Errorf("proxycheck: no record for %s", ip)
	}
	var record proxyCheckRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("proxycheck: parse record: %w", err)
	}

	networkType := service.NormalizeProxyCheckType(record.Type)
	// vpn / proxy 是独立于 type 的标记：一个住宅 IP 也可能同时被标为 VPN 出口，
	// 那种情况下 VPN 才是对上游更重要的特征，优先级高于 type。
	if strings.EqualFold(record.VPN, "yes") || strings.EqualFold(record.Proxy, "yes") {
		networkType = service.IPNetworkTypeVPN
	}

	return &service.IPNetworkInfo{
		Type:    networkType,
		Source:  service.IPNetworkSourceProxyCheck,
		RawType: record.Type,
		ISP:     record.Provider,
		Org:     record.Organisation,
		ASN:     record.ASN,
		ASName:  record.Provider,
	}, nil
}
