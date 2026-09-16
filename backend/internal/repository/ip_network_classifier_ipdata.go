package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ipdata.co 出口 IP 分类器。
//
// 与 proxycheck 互补，不是替代：2026-09-16 实测，130.117.139.191（AS174 Cogent，
// 确凿的机房）在 proxycheck 只被判为 Business，唯一抓住它的是 ipdata 的
// asn.type=internet_backbone。反过来 ipdata 的 threat.is_datacenter 对同一个 IP
// 返回 false——**这个字段三家数据源都不可靠，一律不用**。
//
// 需要 API key（免费层 1500 次/天）。key 从配置读，不落代码。

const (
	ipDataEndpoint         = "https://api.ipdata.co/"
	ipDataMaxResponseBytes = int64(64 * 1024)
)

type ipDataClassifier struct {
	apiKey   string
	timeout  time.Duration
	endpoint string // 仅供测试注入
}

type ipDataResponse struct {
	Message string `json:"message"` // 出错时才有
	ASN     struct {
		ASN    string `json:"asn"`
		Name   string `json:"name"`
		Domain string `json:"domain"`
		Type   string `json:"type"`
	} `json:"asn"`
}

func (c *ipDataClassifier) ClassifyIP(ctx context.Context, ip string) (*service.IPNetworkInfo, error) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return nil, fmt.Errorf("ipdata: empty ip")
	}
	if c.apiKey == "" {
		return nil, fmt.Errorf("ipdata: api key not configured")
	}

	client, err := httpclient.GetClient(httpclient.Options{Timeout: c.timeout})
	if err != nil {
		return nil, fmt.Errorf("ipdata: build client: %w", err)
	}

	endpoint := c.endpoint
	if endpoint == "" {
		endpoint = ipDataEndpoint
	}
	query := url.Values{}
	query.Set("api-key", c.apiKey)
	reqURL := endpoint + url.PathEscape(ip) + "?" + query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("ipdata: build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ipdata: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, ipDataMaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("ipdata: read response: %w", err)
	}
	if int64(len(body)) > ipDataMaxResponseBytes {
		return nil, fmt.Errorf("ipdata: response exceeds %d bytes", ipDataMaxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ipdata: unexpected status %d: %s", resp.StatusCode, ipDataErrorMessage(body))
	}

	return parseIPDataResponse(body)
}

func ipDataErrorMessage(body []byte) string {
	var parsed ipDataResponse
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Message != "" {
		return parsed.Message
	}
	preview := string(body)
	if len(preview) > 160 {
		preview = preview[:160] + "..."
	}
	return preview
}

func parseIPDataResponse(body []byte) (*service.IPNetworkInfo, error) {
	var parsed ipDataResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		preview := string(body)
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		return nil, fmt.Errorf("ipdata: parse response: %w (body: %s)", err, preview)
	}
	if parsed.ASN.Type == "" {
		return nil, fmt.Errorf("ipdata: response has no asn.type")
	}
	return &service.IPNetworkInfo{
		Type:    service.NormalizeIPDataASNType(parsed.ASN.Type),
		Source:  service.IPNetworkSourceIPData,
		RawType: parsed.ASN.Type,
		ISP:     parsed.ASN.Name,
		ASN:     parsed.ASN.ASN,
		ASName:  parsed.ASN.Name,
	}, nil
}
