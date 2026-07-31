package usdt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	// TronMainnetAPIBase and TronShastaAPIBase are the only hosts we will talk
	// to. Pointing this at an arbitrary URL would let a misconfiguration feed
	// us fabricated "deposits", so the allowlist is enforced at construction.
	TronMainnetAPIBase = "https://api.trongrid.io"
	TronShastaAPIBase  = "https://api.shasta.trongrid.io"

	// DefaultUSDTContract is the official USDT-TRC20 contract on TRON mainnet.
	DefaultUSDTContract = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"

	tronHTTPTimeout      = 15 * time.Second
	tronMaxResponseSize  = 4 << 20
	tronPageSize         = 200
	tronMaxPages         = 10
	tronTransferTypeName = "Transfer"
)

var tronAllowedHosts = map[string]struct{}{
	"api.trongrid.io":        {},
	"api.shasta.trongrid.io": {},
}

// Transfer is a single confirmed TRC20 credit to one of our receiving
// addresses, normalised into human units.
type Transfer struct {
	TxHash        string
	From          string
	To            string
	TokenContract string
	// Amount is in whole USDT, converted from the raw integer using the
	// contract's own decimals. Kept as decimal end-to-end because
	// reconciliation matches on exact equality.
	Amount         decimal.Decimal
	BlockTimestamp time.Time
}

// TronOptions is the per-instance TronGrid configuration, sourced from the
// provider instance config an admin fills in.
type TronOptions struct {
	APIBase       string
	APIKey        string
	TokenContract string
	HTTPClient    *http.Client
}

// TronClient reads confirmed TRC20 transfers from TronGrid.
type TronClient struct {
	apiBase       string
	apiKey        string
	tokenContract string
	httpClient    *http.Client
}

// NewTronClient validates the configuration and builds a client. Validation
// happens here so a bad API key or contract address is reported when the admin
// saves the channel, not when the first customer tries to pay.
func NewTronClient(opts TronOptions) (*TronClient, error) {
	apiBase, err := normalizeTronAPIBase(opts.APIBase)
	if err != nil {
		return nil, err
	}
	apiKey := strings.TrimSpace(opts.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("trongrid apiKey is required")
	}
	tokenContract := strings.TrimSpace(opts.TokenContract)
	if tokenContract == "" {
		tokenContract = DefaultUSDTContract
	}
	if err := ValidateTronAddress(tokenContract); err != nil {
		return nil, fmt.Errorf("trongrid tokenContract: %w", err)
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: tronHTTPTimeout}
	}
	return &TronClient{
		apiBase:       apiBase,
		apiKey:        apiKey,
		tokenContract: tokenContract,
		httpClient:    httpClient,
	}, nil
}

// TokenContract returns the contract this client reconciles against.
func (c *TronClient) TokenContract() string { return c.tokenContract }

func normalizeTronAPIBase(raw string) (string, error) {
	base := strings.TrimSpace(raw)
	if base == "" {
		return TronMainnetAPIBase, nil
	}
	base = strings.TrimRight(base, "/")
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("trongrid apiBase must be a valid URL")
	}
	// Tests point this at an httptest server; production must be HTTPS on a
	// known TronGrid host.
	if isLoopbackHost(parsed.Host) {
		return base, nil
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("trongrid apiBase must use https")
	}
	if _, ok := tronAllowedHosts[strings.ToLower(parsed.Host)]; !ok {
		return "", fmt.Errorf("trongrid apiBase host must be api.trongrid.io or api.shasta.trongrid.io")
	}
	return base, nil
}

func isLoopbackHost(host string) bool {
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	switch strings.ToLower(name) {
	case "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	}
	return false
}

// ListIncomingTransfers returns confirmed TRC20 credits to address with a block
// timestamp at or after since.
//
// only_confirmed is always set: an unconfirmed TRON transfer can still be
// orphaned, and crediting an order from one would hand out balance for money
// that never settled.
func (c *TronClient) ListIncomingTransfers(ctx context.Context, address string, since time.Time) ([]Transfer, error) {
	address, err := NormalizeTronAddress(address)
	if err != nil {
		return nil, fmt.Errorf("trongrid receiving address: %w", err)
	}

	transfers := make([]Transfer, 0, tronPageSize)
	fingerprint := ""
	for range tronMaxPages {
		page, err := c.fetchPage(ctx, address, since, fingerprint)
		if err != nil {
			return nil, err
		}
		for _, raw := range page.Data {
			transfer, ok := c.normalizeTransfer(raw, address)
			if !ok {
				continue
			}
			transfers = append(transfers, transfer)
		}
		fingerprint = strings.TrimSpace(page.Meta.Fingerprint)
		if fingerprint == "" || len(page.Data) == 0 {
			break
		}
	}
	return transfers, nil
}

type tronTRC20Page struct {
	Success *bool             `json:"success"`
	Error   string            `json:"error"`
	Data    []tronTRC20Record `json:"data"`
	Meta    struct {
		Fingerprint string `json:"fingerprint"`
	} `json:"meta"`
}

type tronTRC20Record struct {
	TransactionID  string `json:"transaction_id"`
	BlockTimestamp int64  `json:"block_timestamp"`
	From           string `json:"from"`
	To             string `json:"to"`
	Type           string `json:"type"`
	Value          string `json:"value"`
	TokenInfo      struct {
		Address  string `json:"address"`
		Decimals int    `json:"decimals"`
		Symbol   string `json:"symbol"`
	} `json:"token_info"`
}

func (c *TronClient) fetchPage(ctx context.Context, address string, since time.Time, fingerprint string) (*tronTRC20Page, error) {
	endpoint, err := url.Parse(c.apiBase + "/v1/accounts/" + url.PathEscape(address) + "/transactions/trc20")
	if err != nil {
		return nil, fmt.Errorf("build trongrid url: %w", err)
	}
	query := endpoint.Query()
	query.Set("only_to", "true")
	query.Set("only_confirmed", "true")
	query.Set("contract_address", c.tokenContract)
	query.Set("limit", strconv.Itoa(tronPageSize))
	query.Set("order_by", "block_timestamp,asc")
	query.Set("min_timestamp", strconv.FormatInt(since.UnixMilli(), 10))
	if fingerprint != "" {
		query.Set("fingerprint", fingerprint)
	}
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build trongrid request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("TRON-PRO-API-KEY", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call trongrid: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, tronMaxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("read trongrid response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("trongrid HTTP %d: %s", resp.StatusCode, summarize(body))
	}

	var page tronTRC20Page
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("parse trongrid response: %w", err)
	}
	if page.Success != nil && !*page.Success {
		return nil, fmt.Errorf("trongrid returned success=false: %s", summarize(body))
	}
	return &page, nil
}

// normalizeTransfer converts one raw record, dropping anything that is not a
// confirmed inbound USDT transfer to the address we asked about.
//
// The filters are not redundant with the query parameters: TronGrid has been
// known to include approvals and unrelated tokens, and a scam token airdropped
// with a matching amount must never be able to settle a real order.
func (c *TronClient) normalizeTransfer(raw tronTRC20Record, address string) (Transfer, bool) {
	// Strict rather than lenient: an entry we cannot positively identify as a
	// transfer is dropped. If TronGrid ever changes this field the failure is
	// "nobody's USDT payment settles", which is loud and safe, rather than
	// "unknown record types credit orders", which is silent and expensive.
	if !strings.EqualFold(strings.TrimSpace(raw.Type), tronTransferTypeName) {
		return Transfer{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(raw.TokenInfo.Address), c.tokenContract) {
		return Transfer{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(raw.To), address) {
		return Transfer{}, false
	}

	txHash := strings.TrimSpace(raw.TransactionID)
	if txHash == "" {
		return Transfer{}, false
	}
	if raw.BlockTimestamp <= 0 {
		return Transfer{}, false
	}
	decimals := raw.TokenInfo.Decimals
	if decimals < 0 || decimals > 18 {
		return Transfer{}, false
	}

	rawValue, err := decimal.NewFromString(strings.TrimSpace(raw.Value))
	if err != nil || rawValue.LessThanOrEqual(decimal.Zero) {
		return Transfer{}, false
	}

	return Transfer{
		TxHash:         txHash,
		From:           strings.TrimSpace(raw.From),
		To:             strings.TrimSpace(raw.To),
		TokenContract:  strings.TrimSpace(raw.TokenInfo.Address),
		Amount:         rawValue.Shift(int32(-decimals)),
		BlockTimestamp: time.UnixMilli(raw.BlockTimestamp).UTC(),
	}, true
}
