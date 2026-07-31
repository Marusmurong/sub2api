//go:build unit

package service

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/ent/usdtpaymentintent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/usdt"
	"github.com/shopspring/decimal"

	_ "modernc.org/sqlite"
)

const (
	testUSDTWallet   = "TJmmqjb1DK9TTZbQXzRQ2AuA94z4gKAPFh"
	testUSDTContract = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
)

func newUSDTTestClient(t *testing.T) *dbent.Client {
	t.Helper()
	db, err := sql.Open("sqlite", "file:usdt_"+t.Name()+"?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// usdtRateStub serves a fixed CoinGecko price so intent tests are deterministic.
func usdtRateStub(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func usdtTestConfig(rateServerURL string) map[string]string {
	return map[string]string{
		usdt.ConfigKeyWalletAddress:      testUSDTWallet,
		usdt.ConfigKeyTronAPIKey:         "tron-key",
		usdt.ConfigKeyCoinGeckoAPIBase:   rateServerURL,
		usdt.ConfigKeyRatePremiumPercent: "0",
	}
}

func newUSDTIntentService(t *testing.T, client *dbent.Client, rateServerURL string) *USDTIntentService {
	t.Helper()
	rates := usdt.NewRateProvider(nil, nil)
	svc := NewUSDTIntentService(client, rates)
	_ = rateServerURL
	return svc
}

func usdtAllocateInput(config map[string]string) USDTAllocateIntentInput {
	return USDTAllocateIntentInput{
		OrderID:            1,
		OutTradeNo:         "sub2_20260731aaaaaaaa",
		ProviderInstanceID: "7",
		PayAmountCNY:       100,
		OrderExpiresAt:     time.Now().Add(30 * time.Minute),
		Config:             config,
	}
}

func TestAllocateIntentSnapshotsRateAndAmount(t *testing.T) {
	client := newUSDTTestClient(t)
	rateServer := usdtRateStub(t, `{"tether":{"cny":7.42}}`)
	svc := newUSDTIntentService(t, client, rateServer.URL)

	intent, err := svc.AllocateIntent(context.Background(), usdtAllocateInput(usdtTestConfig(rateServer.URL)))
	if err != nil {
		t.Fatalf("AllocateIntent() error = %v", err)
	}

	if intent.Address != testUSDTWallet {
		t.Fatalf("Address = %q, want %q", intent.Address, testUSDTWallet)
	}
	if intent.Network != usdt.NetworkTRC20 {
		t.Fatalf("Network = %q, want %q", intent.Network, usdt.NetworkTRC20)
	}
	if intent.TokenContract != testUSDTContract {
		t.Fatalf("TokenContract = %q, want %q", intent.TokenContract, testUSDTContract)
	}
	if intent.Status != USDTIntentStatusPending {
		t.Fatalf("Status = %q, want %q", intent.Status, USDTIntentStatusPending)
	}
	if intent.RateSource != usdt.RateSourceCoinGecko {
		t.Fatalf("RateSource = %q, want %q", intent.RateSource, usdt.RateSourceCoinGecko)
	}
	if want := "7.42"; intent.Rate != want {
		t.Fatalf("Rate = %q, want %q", intent.Rate, want)
	}

	// 100 CNY / 7.42 = 13.4770... → base rounds up to 13.48, then the 2-digit
	// uniqueness tag lands somewhere in 13.4800–13.4899.
	amount, err := decimal.NewFromString(intent.AmountUsdt)
	if err != nil {
		t.Fatalf("stored amount %q is not a decimal: %v", intent.AmountUsdt, err)
	}
	low, high := decimal.RequireFromString("13.48"), decimal.RequireFromString("13.4899")
	if amount.LessThan(low) || amount.GreaterThan(high) {
		t.Fatalf("AmountUsdt = %s, want within [%s, %s]", amount, low, high)
	}
	if intent.AmountUsdt != usdt.CanonicalAmount(amount) {
		t.Fatalf("AmountUsdt = %q, want canonical form %q", intent.AmountUsdt, usdt.CanonicalAmount(amount))
	}
}

// The intent has to outlive the order so a transfer that confirms just after
// the order times out can still settle it.
func TestAllocateIntentOutlivesTheOrder(t *testing.T) {
	client := newUSDTTestClient(t)
	rateServer := usdtRateStub(t, `{"tether":{"cny":7.42}}`)
	svc := newUSDTIntentService(t, client, rateServer.URL)

	input := usdtAllocateInput(usdtTestConfig(rateServer.URL))
	intent, err := svc.AllocateIntent(context.Background(), input)
	if err != nil {
		t.Fatalf("AllocateIntent() error = %v", err)
	}
	if !intent.ExpiresAt.After(input.OrderExpiresAt) {
		t.Fatalf("intent expires at %s, want later than the order's %s", intent.ExpiresAt, input.OrderExpiresAt)
	}
}

func TestAllocateIntentIsIdempotentPerOrder(t *testing.T) {
	client := newUSDTTestClient(t)
	rateServer := usdtRateStub(t, `{"tether":{"cny":7.42}}`)
	svc := newUSDTIntentService(t, client, rateServer.URL)
	input := usdtAllocateInput(usdtTestConfig(rateServer.URL))

	first, err := svc.AllocateIntent(context.Background(), input)
	if err != nil {
		t.Fatalf("first AllocateIntent() error = %v", err)
	}
	second, err := svc.AllocateIntent(context.Background(), input)
	if err != nil {
		t.Fatalf("second AllocateIntent() error = %v", err)
	}

	if first.ID != second.ID {
		t.Fatalf("second allocation created intent %d, want the existing %d", second.ID, first.ID)
	}
	count, err := client.USDTPaymentIntent.Query().Count(context.Background())
	if err != nil {
		t.Fatalf("count intents: %v", err)
	}
	if count != 1 {
		t.Fatalf("stored %d intents for one order, want 1", count)
	}
}

// The uniqueness tag is what makes reconciliation unambiguous, so no two
// pending orders on one address may ever share an amount.
func TestAllocateIntentGivesEveryPendingOrderADistinctAmount(t *testing.T) {
	client := newUSDTTestClient(t)
	rateServer := usdtRateStub(t, `{"tether":{"cny":7.42}}`)
	svc := newUSDTIntentService(t, client, rateServer.URL)
	config := usdtTestConfig(rateServer.URL)

	seen := make(map[string]int64, usdt.SuffixCount)
	for i := range usdt.SuffixCount {
		input := usdtAllocateInput(config)
		input.OrderID = int64(i + 1)
		intent, err := svc.AllocateIntent(context.Background(), input)
		if err != nil {
			t.Fatalf("AllocateIntent(order=%d) error = %v", input.OrderID, err)
		}
		if prev, dup := seen[intent.AmountUsdt]; dup {
			t.Fatalf("order %d reused amount %s already held by order %d",
				input.OrderID, intent.AmountUsdt, prev)
		}
		seen[intent.AmountUsdt] = input.OrderID
	}
	if len(seen) != usdt.SuffixCount {
		t.Fatalf("allocated %d distinct amounts, want %d", len(seen), usdt.SuffixCount)
	}
}

// Running out of tags must be a clean refusal, not a duplicate amount.
func TestAllocateIntentFailsWhenEveryTagIsTaken(t *testing.T) {
	client := newUSDTTestClient(t)
	rateServer := usdtRateStub(t, `{"tether":{"cny":7.42}}`)
	svc := newUSDTIntentService(t, client, rateServer.URL)
	config := usdtTestConfig(rateServer.URL)

	for i := range usdt.SuffixCount {
		input := usdtAllocateInput(config)
		input.OrderID = int64(i + 1)
		if _, err := svc.AllocateIntent(context.Background(), input); err != nil {
			t.Fatalf("AllocateIntent(order=%d) error = %v", input.OrderID, err)
		}
	}

	overflow := usdtAllocateInput(config)
	overflow.OrderID = int64(usdt.SuffixCount + 1)
	_, err := svc.AllocateIntent(context.Background(), overflow)
	if !errors.Is(err, ErrUSDTAmountSlotExhausted) {
		t.Fatalf("AllocateIntent() error = %v, want ErrUSDTAmountSlotExhausted", err)
	}
}

// Concurrency is where an application-level uniqueness check would fall over,
// so the database constraint has to be the thing holding the line.
func TestAllocateIntentIsSafeUnderConcurrency(t *testing.T) {
	client := newUSDTTestClient(t)
	rateServer := usdtRateStub(t, `{"tether":{"cny":7.42}}`)
	svc := newUSDTIntentService(t, client, rateServer.URL)
	config := usdtTestConfig(rateServer.URL)

	const concurrent = 20
	var wg sync.WaitGroup
	amounts := make([]string, concurrent)
	errs := make([]error, concurrent)
	for i := range concurrent {
		wg.Add(1)
		go func() {
			defer wg.Done()
			input := usdtAllocateInput(config)
			input.OrderID = int64(i + 1)
			intent, err := svc.AllocateIntent(context.Background(), input)
			if err != nil {
				errs[i] = err
				return
			}
			amounts[i] = intent.AmountUsdt
		}()
	}
	wg.Wait()

	seen := make(map[string]bool, concurrent)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent AllocateIntent[%d] error = %v", i, err)
		}
		if seen[amounts[i]] {
			t.Fatalf("concurrent allocations collided on amount %s", amounts[i])
		}
		seen[amounts[i]] = true
	}
}

// A closed intent releases its tag: the amount is no longer in flight, so a new
// order may reuse it.
func TestClosedIntentReleasesItsAmountSlot(t *testing.T) {
	client := newUSDTTestClient(t)
	rateServer := usdtRateStub(t, `{"tether":{"cny":7.42}}`)
	svc := newUSDTIntentService(t, client, rateServer.URL)
	config := usdtTestConfig(rateServer.URL)

	for i := range usdt.SuffixCount {
		input := usdtAllocateInput(config)
		input.OrderID = int64(i + 1)
		if _, err := svc.AllocateIntent(context.Background(), input); err != nil {
			t.Fatalf("AllocateIntent(order=%d) error = %v", input.OrderID, err)
		}
	}
	if _, err := client.USDTPaymentIntent.Update().
		Where(usdtpaymentintent.OrderIDEQ(1)).
		SetStatus(USDTIntentStatusClosed).
		Save(context.Background()); err != nil {
		t.Fatalf("close intent: %v", err)
	}

	input := usdtAllocateInput(config)
	input.OrderID = int64(usdt.SuffixCount + 1)
	if _, err := svc.AllocateIntent(context.Background(), input); err != nil {
		t.Fatalf("AllocateIntent() after releasing a slot error = %v", err)
	}
}

// Fail closed: with no trustworthy rate we refuse to price the order rather
// than guess.
func TestAllocateIntentRefusesWhenRateIsUnavailable(t *testing.T) {
	client := newUSDTTestClient(t)
	rateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(rateServer.Close)
	svc := newUSDTIntentService(t, client, rateServer.URL)

	_, err := svc.AllocateIntent(context.Background(), usdtAllocateInput(usdtTestConfig(rateServer.URL)))
	if err == nil {
		t.Fatal("AllocateIntent() = nil error, want refusal when no rate is available")
	}
	count, _ := client.USDTPaymentIntent.Query().Count(context.Background())
	if count != 0 {
		t.Fatalf("stored %d intents despite having no rate, want 0", count)
	}
}

func TestAllocateIntentRejectsInvalidConfig(t *testing.T) {
	client := newUSDTTestClient(t)
	rateServer := usdtRateStub(t, `{"tether":{"cny":7.42}}`)
	svc := newUSDTIntentService(t, client, rateServer.URL)

	config := usdtTestConfig(rateServer.URL)
	delete(config, usdt.ConfigKeyWalletAddress)

	if _, err := svc.AllocateIntent(context.Background(), usdtAllocateInput(config)); err == nil {
		t.Fatal("AllocateIntent() with no wallet address = nil error, want error")
	}
}

func TestAllocateIntentRejectsNonPositivePayAmount(t *testing.T) {
	client := newUSDTTestClient(t)
	rateServer := usdtRateStub(t, `{"tether":{"cny":7.42}}`)
	svc := newUSDTIntentService(t, client, rateServer.URL)

	input := usdtAllocateInput(usdtTestConfig(rateServer.URL))
	input.PayAmountCNY = 0
	if _, err := svc.AllocateIntent(context.Background(), input); err == nil {
		t.Fatal("AllocateIntent() with zero pay amount = nil error, want error")
	}
}

func TestUSDTPaymentInfoRendersFourDecimalsForDisplay(t *testing.T) {
	client := newUSDTTestClient(t)
	rateServer := usdtRateStub(t, `{"tether":{"cny":7.42}}`)
	svc := newUSDTIntentService(t, client, rateServer.URL)

	intent, err := svc.AllocateIntent(context.Background(), usdtAllocateInput(usdtTestConfig(rateServer.URL)))
	if err != nil {
		t.Fatalf("AllocateIntent() error = %v", err)
	}

	info := NewUSDTPaymentInfo(intent)
	if len(info.AmountUSDT) < 3 || info.AmountUSDT[len(info.AmountUSDT)-5] != '.' {
		t.Fatalf("AmountUSDT = %q, want the 4-decimal display form the payer must match", info.AmountUSDT)
	}
	if info.Address != testUSDTWallet {
		t.Fatalf("Address = %q, want %q", info.Address, testUSDTWallet)
	}
	if info.Network != usdt.NetworkTRC20 {
		t.Fatalf("Network = %q, want %q", info.Network, usdt.NetworkTRC20)
	}
}

func TestUSDTIntentUsesCNYOrderCurrency(t *testing.T) {
	// The order stays CNY-denominated so revenue stats, daily limits and the
	// cashback plugin need no USDT-specific handling. Guard the assumption.
	if payment.DefaultPaymentCurrency != "CNY" {
		t.Fatalf("DefaultPaymentCurrency = %q, want CNY", payment.DefaultPaymentCurrency)
	}
	if got := paymentProviderConfigCurrency(payment.TypeUSDT, map[string]string{"currency": "USD"}); got != "CNY" {
		t.Fatalf("paymentProviderConfigCurrency(usdt) = %q, want CNY regardless of config", got)
	}
}
