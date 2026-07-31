package usdt

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// rateStub is a CoinGecko stand-in that counts calls so tests can assert the
// cache actually prevents upstream traffic.
type rateStub struct {
	server *httptest.Server
	calls  atomic.Int64
	mu     sync.Mutex
	body   string
	status int
	delay  time.Duration
	seenKe []string
}

func newRateStub(t *testing.T) *rateStub {
	t.Helper()
	stub := &rateStub{body: `{"tether":{"cny":7.2000}}`, status: http.StatusOK}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.calls.Add(1)
		stub.mu.Lock()
		body, status, delay := stub.body, stub.status, stub.delay
		stub.seenKe = append(stub.seenKe, r.Header.Get("x-cg-demo-api-key"))
		stub.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *rateStub) set(body string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.body, s.status = body, status
}

func (s *rateStub) apiKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seenKe...)
}

// fakeClock lets staleness tests move time without sleeping.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func testRateOptions(stub *rateStub, premium string) RateOptions {
	return RateOptions{
		APIBase:         stub.server.URL,
		APIKey:          "demo-key",
		PremiumPercent:  decimal.RequireFromString(premium),
		RefreshInterval: 10 * time.Minute,
		MaxStaleness:    30 * time.Minute,
	}
}

func TestQuoteAppliesPremiumToBaseRate(t *testing.T) {
	stub := newRateStub(t)
	provider := NewRateProvider(stub.server.Client(), newFakeClock().Now)

	quote, err := provider.Quote(context.Background(), testRateOptions(stub, "3"))
	if err != nil {
		t.Fatalf("Quote() error = %v", err)
	}
	// 7.2000 × 1.03 = 7.416
	if want := decimal.RequireFromString("7.416"); !quote.Rate.Equal(want) {
		t.Fatalf("Quote().Rate = %s, want %s", quote.Rate, want)
	}
	if want := decimal.RequireFromString("7.2"); !quote.BaseRate.Equal(want) {
		t.Fatalf("Quote().BaseRate = %s, want %s", quote.BaseRate, want)
	}
	if quote.Source != RateSourceCoinGecko {
		t.Fatalf("Quote().Source = %q, want %q", quote.Source, RateSourceCoinGecko)
	}
}

func TestQuoteSendsDemoAPIKeyHeader(t *testing.T) {
	stub := newRateStub(t)
	provider := NewRateProvider(stub.server.Client(), newFakeClock().Now)

	if _, err := provider.Quote(context.Background(), testRateOptions(stub, "0")); err != nil {
		t.Fatalf("Quote() error = %v", err)
	}
	keys := stub.apiKeys()
	if len(keys) != 1 || keys[0] != "demo-key" {
		t.Fatalf("upstream saw api keys %v, want exactly [demo-key]", keys)
	}
}

func TestQuoteServesCachedBaseRateWithinRefreshInterval(t *testing.T) {
	stub := newRateStub(t)
	clock := newFakeClock()
	provider := NewRateProvider(stub.server.Client(), clock.Now)
	opts := testRateOptions(stub, "0")

	for range 5 {
		if _, err := provider.Quote(context.Background(), opts); err != nil {
			t.Fatalf("Quote() error = %v", err)
		}
		clock.advance(time.Minute)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("upstream called %d times, want 1 (cache should absorb the rest)", got)
	}
}

func TestQuoteRefetchesAfterRefreshInterval(t *testing.T) {
	stub := newRateStub(t)
	clock := newFakeClock()
	provider := NewRateProvider(stub.server.Client(), clock.Now)
	opts := testRateOptions(stub, "0")

	if _, err := provider.Quote(context.Background(), opts); err != nil {
		t.Fatalf("first Quote() error = %v", err)
	}
	clock.advance(11 * time.Minute)
	stub.set(`{"tether":{"cny":7.5000}}`, http.StatusOK)

	quote, err := provider.Quote(context.Background(), opts)
	if err != nil {
		t.Fatalf("second Quote() error = %v", err)
	}
	if want := decimal.RequireFromString("7.5"); !quote.BaseRate.Equal(want) {
		t.Fatalf("Quote().BaseRate = %s, want refreshed %s", quote.BaseRate, want)
	}
	if got := stub.calls.Load(); got != 2 {
		t.Fatalf("upstream called %d times, want 2", got)
	}
}

// The whole point of caching a last-good rate: a CoinGecko blip must not take
// the payment method offline.
func TestQuoteFallsBackToLastGoodRateWhenUpstreamFails(t *testing.T) {
	stub := newRateStub(t)
	clock := newFakeClock()
	provider := NewRateProvider(stub.server.Client(), clock.Now)
	opts := testRateOptions(stub, "0")

	if _, err := provider.Quote(context.Background(), opts); err != nil {
		t.Fatalf("warmup Quote() error = %v", err)
	}
	clock.advance(11 * time.Minute)
	stub.set(`{"error":"rate limited"}`, http.StatusTooManyRequests)

	quote, err := provider.Quote(context.Background(), opts)
	if err != nil {
		t.Fatalf("Quote() during upstream outage error = %v, want cached fallback", err)
	}
	if want := decimal.RequireFromString("7.2"); !quote.BaseRate.Equal(want) {
		t.Fatalf("Quote().BaseRate = %s, want cached %s", quote.BaseRate, want)
	}
	if !quote.Stale {
		t.Fatal("Quote().Stale = false, want true so callers can surface the degraded state")
	}
}

// Fail closed: past MaxStaleness we refuse to price an order at all rather than
// quote a number nobody can vouch for.
func TestQuoteRefusesWhenCachedRateExceedsMaxStaleness(t *testing.T) {
	stub := newRateStub(t)
	clock := newFakeClock()
	provider := NewRateProvider(stub.server.Client(), clock.Now)
	opts := testRateOptions(stub, "0")

	if _, err := provider.Quote(context.Background(), opts); err != nil {
		t.Fatalf("warmup Quote() error = %v", err)
	}
	stub.set(``, http.StatusInternalServerError)
	clock.advance(31 * time.Minute)

	_, err := provider.Quote(context.Background(), opts)
	if !errors.Is(err, ErrRateStale) {
		t.Fatalf("Quote() error = %v, want ErrRateStale", err)
	}
}

func TestQuoteFailsWhenUpstreamNeverSucceeded(t *testing.T) {
	stub := newRateStub(t)
	stub.set(``, http.StatusInternalServerError)
	provider := NewRateProvider(stub.server.Client(), newFakeClock().Now)

	_, err := provider.Quote(context.Background(), testRateOptions(stub, "0"))
	if !errors.Is(err, ErrRateUnavailable) {
		t.Fatalf("Quote() error = %v, want ErrRateUnavailable", err)
	}
}

func TestQuoteRejectsUnusableUpstreamPayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing tether key", `{"bitcoin":{"cny":500000}}`},
		{"missing cny key", `{"tether":{"usd":1.0}}`},
		{"zero rate", `{"tether":{"cny":0}}`},
		{"negative rate", `{"tether":{"cny":-7.2}}`},
		{"not json", `<html>blocked</html>`},
		{"absurdly high rate", `{"tether":{"cny":100000}}`},
		{"absurdly low rate", `{"tether":{"cny":0.0001}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := newRateStub(t)
			stub.set(tc.body, http.StatusOK)
			provider := NewRateProvider(stub.server.Client(), newFakeClock().Now)

			if _, err := provider.Quote(context.Background(), testRateOptions(stub, "0")); err == nil {
				t.Fatalf("Quote() with body %s = nil error, want error", tc.body)
			}
		})
	}
}

// Ten concurrent checkouts must not turn into ten CoinGecko calls — the free
// tier allows 10k/month and stampedes would burn it in bursts.
func TestQuoteCollapsesConcurrentFetches(t *testing.T) {
	stub := newRateStub(t)
	stub.delay = 50 * time.Millisecond
	provider := NewRateProvider(stub.server.Client(), newFakeClock().Now)
	opts := testRateOptions(stub, "0")

	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = provider.Quote(context.Background(), opts)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Quote()[%d] error = %v", i, err)
		}
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("upstream called %d times, want 1", got)
	}
}

// Two USDT instances sharing one CoinGecko key must share the fetch too; only
// the premium differs between them.
func TestQuoteSharesBaseRateAcrossPremiums(t *testing.T) {
	stub := newRateStub(t)
	provider := NewRateProvider(stub.server.Client(), newFakeClock().Now)

	low, err := provider.Quote(context.Background(), testRateOptions(stub, "0"))
	if err != nil {
		t.Fatalf("Quote(premium=0) error = %v", err)
	}
	high, err := provider.Quote(context.Background(), testRateOptions(stub, "5"))
	if err != nil {
		t.Fatalf("Quote(premium=5) error = %v", err)
	}

	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("upstream called %d times, want 1 shared fetch", got)
	}
	if !low.Rate.Equal(decimal.RequireFromString("7.2")) {
		t.Fatalf("premium=0 rate = %s, want 7.2", low.Rate)
	}
	if !high.Rate.Equal(decimal.RequireFromString("7.56")) {
		t.Fatalf("premium=5 rate = %s, want 7.56", high.Rate)
	}
}

func TestQuoteRejectsInvalidPremium(t *testing.T) {
	stub := newRateStub(t)
	provider := NewRateProvider(stub.server.Client(), newFakeClock().Now)

	for _, premium := range []string{"-1", "21"} {
		opts := testRateOptions(stub, premium)
		if _, err := provider.Quote(context.Background(), opts); err == nil {
			t.Fatalf("Quote(premium=%s) = nil error, want error", premium)
		}
	}
}
