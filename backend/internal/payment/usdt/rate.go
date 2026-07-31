package usdt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

// RateSourceCoinGecko identifies the upstream a quote came from. It is
// persisted with every order so a disputed price can be traced back.
const RateSourceCoinGecko = "coingecko"

const (
	// DefaultRateAPIBase is CoinGecko's public API root.
	DefaultRateAPIBase = "https://api.coingecko.com/api/v3"
	// DefaultRateRefreshInterval keeps the free tier's 10k monthly credits
	// comfortable: 6 calls/hour ≈ 4.3k/month across all instances.
	DefaultRateRefreshInterval = 10 * time.Minute
	// DefaultRateMaxStaleness is how long a last-good rate may still be used
	// once the upstream starts failing. Past this, orders are refused.
	DefaultRateMaxStaleness = 30 * time.Minute

	// MaxPremiumPercent bounds the OTC markup an admin can configure. USDT/CNY
	// OTC sits ~2–4% over the market rate; anything past 20% is a typo, and a
	// typo here silently overcharges every customer.
	MaxPremiumPercent = 20

	rateHTTPTimeout     = 10 * time.Second
	rateMaxResponseSize = 64 << 10
)

// Plausibility bounds on the raw CNY-per-USDT rate. USDT is dollar-pegged, so
// a value outside this band means the upstream returned something we should
// not price orders against — a broken payload, a different asset, or an error
// page that happened to parse.
var (
	minPlausibleRate = decimal.RequireFromString("3")
	maxPlausibleRate = decimal.RequireFromString("20")
)

var (
	// ErrRateUnavailable means no usable quote has ever been obtained.
	ErrRateUnavailable = errors.New("usdt exchange rate unavailable")
	// ErrRateStale means the last good quote is older than MaxStaleness.
	// Callers must refuse to create new orders rather than price them blind.
	ErrRateStale = errors.New("usdt exchange rate is stale")
)

// Quote is a point-in-time CNY price for 1 USDT, snapshotted onto an order.
type Quote struct {
	// Rate is what the customer is charged at: BaseRate with premium applied.
	Rate           decimal.Decimal
	BaseRate       decimal.Decimal
	PremiumPercent decimal.Decimal
	Source         string
	QuotedAt       time.Time
	// Stale reports that the upstream is currently failing and this quote came
	// from cache. Still usable, but worth surfacing to operators.
	Stale bool
}

// RateOptions carries the per-instance rate configuration. Every field comes
// from the provider instance config an admin fills in, never from env vars.
type RateOptions struct {
	APIBase         string
	APIKey          string
	PremiumPercent  decimal.Decimal
	RefreshInterval time.Duration
	MaxStaleness    time.Duration
}

func (o RateOptions) withDefaults() RateOptions {
	if strings.TrimSpace(o.APIBase) == "" {
		o.APIBase = DefaultRateAPIBase
	}
	o.APIBase = strings.TrimRight(strings.TrimSpace(o.APIBase), "/")
	if o.RefreshInterval <= 0 {
		o.RefreshInterval = DefaultRateRefreshInterval
	}
	if o.MaxStaleness <= 0 {
		o.MaxStaleness = DefaultRateMaxStaleness
	}
	return o
}

func (o RateOptions) validate() error {
	if o.PremiumPercent.IsNegative() {
		return fmt.Errorf("usdt rate premium must not be negative, got %s", o.PremiumPercent)
	}
	if o.PremiumPercent.GreaterThan(decimal.NewFromInt(MaxPremiumPercent)) {
		return fmt.Errorf("usdt rate premium must not exceed %d%%, got %s", MaxPremiumPercent, o.PremiumPercent)
	}
	return nil
}

// cacheKey groups instances that can share one upstream fetch. The premium is
// deliberately excluded: it is applied locally, so two instances with different
// markups still cost a single CoinGecko call.
func (o RateOptions) cacheKey() string {
	return o.APIBase + "\x00" + o.APIKey
}

type rateCacheEntry struct {
	// mu serialises fetches for one upstream, so a burst of checkouts produces
	// one request instead of one per checkout.
	mu        sync.Mutex
	baseRate  decimal.Decimal
	fetchedAt time.Time
	hasValue  bool
}

// RateProvider fetches and caches USDT/CNY base rates.
//
// It is shared process-wide across provider instances; per-instance settings
// arrive with each Quote call rather than being baked in at construction.
type RateProvider struct {
	httpClient *http.Client
	now        func() time.Time

	mu      sync.Mutex
	entries map[string]*rateCacheEntry
}

// NewRateProvider creates a rate provider. Pass nil for httpClient to get a
// default one; now is injectable so staleness can be tested without sleeping.
func NewRateProvider(httpClient *http.Client, now func() time.Time) *RateProvider {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: rateHTTPTimeout}
	}
	if now == nil {
		now = time.Now
	}
	return &RateProvider{
		httpClient: httpClient,
		now:        now,
		entries:    make(map[string]*rateCacheEntry),
	}
}

// Quote returns the current CNY price of 1 USDT with the instance's premium
// applied.
//
// Failure modes are deliberately asymmetric: a transient upstream error falls
// back to the last good rate (flagged Stale), but once that rate ages past
// MaxStaleness the call fails with ErrRateStale so no order is priced against
// a number nobody can vouch for.
func (p *RateProvider) Quote(ctx context.Context, opts RateOptions) (*Quote, error) {
	opts = opts.withDefaults()
	if err := opts.validate(); err != nil {
		return nil, err
	}

	entry := p.entryFor(opts.cacheKey())
	entry.mu.Lock()
	defer entry.mu.Unlock()

	now := p.now()
	if entry.hasValue && now.Sub(entry.fetchedAt) < opts.RefreshInterval {
		return p.buildQuote(entry, opts, false), nil
	}

	baseRate, fetchErr := p.fetchBaseRate(ctx, opts)
	if fetchErr == nil {
		entry.baseRate = baseRate
		entry.fetchedAt = now
		entry.hasValue = true
		return p.buildQuote(entry, opts, false), nil
	}

	if !entry.hasValue {
		return nil, fmt.Errorf("%w: %v", ErrRateUnavailable, fetchErr)
	}
	if age := now.Sub(entry.fetchedAt); age > opts.MaxStaleness {
		return nil, fmt.Errorf("%w: last good quote is %s old (limit %s): %v",
			ErrRateStale, age.Truncate(time.Second), opts.MaxStaleness, fetchErr)
	}
	return p.buildQuote(entry, opts, true), nil
}

func (p *RateProvider) buildQuote(entry *rateCacheEntry, opts RateOptions, stale bool) *Quote {
	multiplier := decimal.NewFromInt(1).Add(opts.PremiumPercent.Div(decimal.NewFromInt(100)))
	return &Quote{
		Rate:           entry.baseRate.Mul(multiplier),
		BaseRate:       entry.baseRate,
		PremiumPercent: opts.PremiumPercent,
		Source:         RateSourceCoinGecko,
		QuotedAt:       entry.fetchedAt,
		Stale:          stale,
	}
}

func (p *RateProvider) entryFor(key string) *rateCacheEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.entries[key]
	if !ok {
		entry = &rateCacheEntry{}
		p.entries[key] = entry
	}
	return entry
}

type coinGeckoSimplePriceResponse struct {
	Tether struct {
		CNY *decimal.Decimal `json:"cny"`
	} `json:"tether"`
}

func (p *RateProvider) fetchBaseRate(ctx context.Context, opts RateOptions) (decimal.Decimal, error) {
	endpoint, err := url.Parse(opts.APIBase + "/simple/price")
	if err != nil {
		return decimal.Zero, fmt.Errorf("parse coingecko url: %w", err)
	}
	query := endpoint.Query()
	query.Set("ids", "tether")
	query.Set("vs_currencies", "cny")
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return decimal.Zero, fmt.Errorf("build coingecko request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if apiKey := strings.TrimSpace(opts.APIKey); apiKey != "" {
		req.Header.Set("x-cg-demo-api-key", apiKey)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return decimal.Zero, fmt.Errorf("call coingecko: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, rateMaxResponseSize))
	if err != nil {
		return decimal.Zero, fmt.Errorf("read coingecko response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return decimal.Zero, fmt.Errorf("coingecko HTTP %d: %s", resp.StatusCode, summarize(body))
	}

	var parsed coinGeckoSimplePriceResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return decimal.Zero, fmt.Errorf("parse coingecko response: %w", err)
	}
	if parsed.Tether.CNY == nil {
		return decimal.Zero, fmt.Errorf("coingecko response missing tether.cny: %s", summarize(body))
	}

	rate := *parsed.Tether.CNY
	if rate.LessThan(minPlausibleRate) || rate.GreaterThan(maxPlausibleRate) {
		return decimal.Zero, fmt.Errorf("coingecko returned an implausible USDT/CNY rate %s (expected %s–%s)",
			rate, minPlausibleRate, maxPlausibleRate)
	}
	return rate, nil
}

func summarize(body []byte) string {
	const maxSummary = 256
	summary := strings.Join(strings.Fields(string(body)), " ")
	if summary == "" {
		return "<empty>"
	}
	if len(summary) > maxSummary {
		return summary[:maxSummary] + "..."
	}
	return summary
}
