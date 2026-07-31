//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/usdtdeposit"
	"github.com/Wei-Shaw/sub2api/ent/usdtpaymentintent"
	"github.com/Wei-Shaw/sub2api/internal/payment/usdt"
	"github.com/shopspring/decimal"
)

// settleRecorder captures what the reconciler decided to settle, so matching
// logic can be tested without dragging in the whole fulfillment stack.
type settleRecorder struct {
	calls []usdtSettlement
	err   error
}

type usdtSettlement struct {
	OrderID   int64
	TxHash    string
	AmountUsd string
}

func (r *settleRecorder) settle(_ context.Context, intent *dbent.USDTPaymentIntent, deposit *dbent.USDTDeposit) error {
	if r.err != nil {
		return r.err
	}
	r.calls = append(r.calls, usdtSettlement{
		OrderID:   intent.OrderID,
		TxHash:    deposit.TxHash,
		AmountUsd: deposit.AmountUsdt,
	})
	return nil
}

type reconcileFixture struct {
	client    *dbent.Client
	recorder  *settleRecorder
	svc       *USDTReconcileService
	transfers []usdt.Transfer
	fetches   int
}

func newReconcileFixture(t *testing.T) *reconcileFixture {
	t.Helper()
	fixture := &reconcileFixture{
		client:   newUSDTTestClient(t),
		recorder: &settleRecorder{},
	}
	fixture.svc = NewUSDTReconcileService(fixture.client, fixture.recorder.settle)
	fixture.svc.fetchTransfers = func(_ context.Context, _ usdt.Config, _ time.Time) ([]usdt.Transfer, error) {
		fixture.fetches++
		return fixture.transfers, nil
	}
	fixture.svc.loadConfig = func(_ context.Context, _ string) (usdt.Config, error) {
		return usdt.ParseConfig(map[string]string{
			usdt.ConfigKeyWalletAddress: testUSDTWallet,
			usdt.ConfigKeyTronAPIKey:    "tron-key",
		})
	}
	return fixture
}

func (f *reconcileFixture) seedIntent(t *testing.T, orderID int64, amount string, expiresAt time.Time) *dbent.USDTPaymentIntent {
	t.Helper()
	intent, err := f.client.USDTPaymentIntent.Create().
		SetOrderID(orderID).
		SetOutTradeNo("sub2_order_" + decimal.NewFromInt(orderID).String()).
		SetProviderInstanceID("7").
		SetAddress(testUSDTWallet).
		SetNetwork(usdt.NetworkTRC20).
		SetTokenContract(testUSDTContract).
		SetAmountUsdt(usdt.CanonicalAmount(decimal.RequireFromString(amount))).
		SetRate("7.42").SetBaseRate("7.42").SetPremiumPercent("0").
		SetRateSource(usdt.RateSourceCoinGecko).
		SetRateQuotedAt(time.Now()).
		SetStatus(USDTIntentStatusPending).
		SetExpiresAt(expiresAt).
		Save(context.Background())
	if err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	return intent
}

func transferOf(txHash, amount string, at time.Time) usdt.Transfer {
	return usdt.Transfer{
		TxHash:         txHash,
		From:           "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
		To:             testUSDTWallet,
		TokenContract:  testUSDTContract,
		Amount:         decimal.RequireFromString(amount),
		BlockTimestamp: at,
	}
}

func TestReconcileSettlesAnExactAmountMatch(t *testing.T) {
	f := newReconcileFixture(t)
	intent := f.seedIntent(t, 42, "13.4837", time.Now().Add(30*time.Minute))
	f.transfers = []usdt.Transfer{transferOf("tx-abc", "13.4837", time.Now())}

	if err := f.svc.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce() error = %v", err)
	}

	if len(f.recorder.calls) != 1 {
		t.Fatalf("settled %d orders, want 1", len(f.recorder.calls))
	}
	if got := f.recorder.calls[0]; got.OrderID != 42 || got.TxHash != "tx-abc" {
		t.Fatalf("settled %+v, want order 42 via tx-abc", got)
	}

	reloaded := f.client.USDTPaymentIntent.GetX(context.Background(), intent.ID)
	if reloaded.Status != USDTIntentStatusMatched {
		t.Fatalf("intent status = %q, want %q", reloaded.Status, USDTIntentStatusMatched)
	}
	if reloaded.MatchedTxHash == nil || *reloaded.MatchedTxHash != "tx-abc" {
		t.Fatalf("intent matched_tx_hash = %v, want tx-abc", reloaded.MatchedTxHash)
	}

	deposit := f.client.USDTDeposit.Query().Where(usdtdeposit.TxHashEQ("tx-abc")).OnlyX(context.Background())
	if deposit.Status != USDTDepositStatusMatched {
		t.Fatalf("deposit status = %q, want %q", deposit.Status, USDTDepositStatusMatched)
	}
	if deposit.MatchedOrderID == nil || *deposit.MatchedOrderID != 42 {
		t.Fatalf("deposit matched_order_id = %v, want 42", deposit.MatchedOrderID)
	}
}

// The uniqueness tag only works if matching is exact. A near miss must not
// settle anything — it goes to an operator instead.
func TestReconcileRefusesNearMissAmounts(t *testing.T) {
	for _, amount := range []string{"13.4836", "13.4838", "13.48", "13.483700001", "13.4837001"} {
		t.Run(amount, func(t *testing.T) {
			f := newReconcileFixture(t)
			f.seedIntent(t, 42, "13.4837", time.Now().Add(30*time.Minute))
			f.transfers = []usdt.Transfer{transferOf("tx-near", amount, time.Now())}

			if err := f.svc.ReconcileOnce(context.Background()); err != nil {
				t.Fatalf("ReconcileOnce() error = %v", err)
			}
			if len(f.recorder.calls) != 0 {
				t.Fatalf("settled %d orders on a %s transfer against a 13.4837 intent, want 0",
					len(f.recorder.calls), amount)
			}
			deposit := f.client.USDTDeposit.Query().OnlyX(context.Background())
			if deposit.Status != USDTDepositStatusUnmatched {
				t.Fatalf("deposit status = %q, want %q so an operator can handle it",
					deposit.Status, USDTDepositStatusUnmatched)
			}
		})
	}
}

// Equal numbers written with different precision are the same money and must
// still match.
func TestReconcileMatchesEquivalentAmountWritings(t *testing.T) {
	f := newReconcileFixture(t)
	f.seedIntent(t, 42, "13.4837", time.Now().Add(30*time.Minute))
	f.transfers = []usdt.Transfer{transferOf("tx-abc", "13.483700", time.Now())}

	if err := f.svc.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce() error = %v", err)
	}
	if len(f.recorder.calls) != 1 {
		t.Fatalf("settled %d orders, want 1", len(f.recorder.calls))
	}
}

// Rescanning is normal — the lookback window overlaps every cycle. A transfer
// already consumed must never settle a second order.
func TestReconcileNeverConsumesOneTransferTwice(t *testing.T) {
	f := newReconcileFixture(t)
	f.seedIntent(t, 42, "13.4837", time.Now().Add(30*time.Minute))
	f.transfers = []usdt.Transfer{transferOf("tx-abc", "13.4837", time.Now())}

	for range 3 {
		if err := f.svc.ReconcileOnce(context.Background()); err != nil {
			t.Fatalf("ReconcileOnce() error = %v", err)
		}
	}

	if len(f.recorder.calls) != 1 {
		t.Fatalf("settled %d times across 3 passes, want exactly 1", len(f.recorder.calls))
	}
	count := f.client.USDTDeposit.Query().CountX(context.Background())
	if count != 1 {
		t.Fatalf("recorded %d deposit rows for one transfer, want 1", count)
	}
}

// A second order later reusing the same amount must not be settled by the
// already-spent transfer.
func TestReconcileDoesNotReuseASpentTransferForANewOrder(t *testing.T) {
	f := newReconcileFixture(t)
	f.seedIntent(t, 42, "13.4837", time.Now().Add(30*time.Minute))
	f.transfers = []usdt.Transfer{transferOf("tx-abc", "13.4837", time.Now())}

	if err := f.svc.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("first ReconcileOnce() error = %v", err)
	}
	// The first intent is now MATCHED, freeing the amount for a new order.
	f.seedIntent(t, 43, "13.4837", time.Now().Add(30*time.Minute))
	if err := f.svc.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("second ReconcileOnce() error = %v", err)
	}

	if len(f.recorder.calls) != 1 {
		t.Fatalf("settled %d orders, want 1 — the spent transfer must not pay for order 43",
			len(f.recorder.calls))
	}
}

func TestReconcileIgnoresTransfersOlderThanTheIntent(t *testing.T) {
	f := newReconcileFixture(t)
	f.seedIntent(t, 42, "13.4837", time.Now().Add(30*time.Minute))
	// Paid long before this order existed: it belongs to some earlier activity,
	// not to this invoice.
	f.transfers = []usdt.Transfer{transferOf("tx-old", "13.4837", time.Now().Add(-24*time.Hour))}

	if err := f.svc.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce() error = %v", err)
	}
	if len(f.recorder.calls) != 0 {
		t.Fatalf("settled %d orders from a day-old transfer, want 0", len(f.recorder.calls))
	}
}

func TestReconcileIgnoresTransfersAfterTheIntentWindow(t *testing.T) {
	f := newReconcileFixture(t)
	f.seedIntent(t, 42, "13.4837", time.Now().Add(-time.Hour))
	f.transfers = []usdt.Transfer{transferOf("tx-late", "13.4837", time.Now())}

	if err := f.svc.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce() error = %v", err)
	}
	if len(f.recorder.calls) != 0 {
		t.Fatalf("settled %d orders past the intent window, want 0 (operator handles these)",
			len(f.recorder.calls))
	}
}

// TronGrid's free tier is finite; polling it when nothing is outstanding wastes
// quota for no possible gain.
func TestReconcileSkipsUpstreamWhenNothingIsPending(t *testing.T) {
	f := newReconcileFixture(t)

	if err := f.svc.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce() error = %v", err)
	}
	if f.fetches != 0 {
		t.Fatalf("made %d upstream calls with no pending intents, want 0", f.fetches)
	}
}

// One upstream call per address, not per order.
func TestReconcileBatchesIntentsSharingAnAddress(t *testing.T) {
	f := newReconcileFixture(t)
	for i := range 5 {
		f.seedIntent(t, int64(100+i), decimal.RequireFromString("13.48").
			Add(decimal.New(int64(i), -4)).String(), time.Now().Add(30*time.Minute))
	}

	if err := f.svc.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce() error = %v", err)
	}
	if f.fetches != 1 {
		t.Fatalf("made %d upstream calls for 5 intents on one address, want 1", f.fetches)
	}
}

// If settlement fails the intent must stay claimable, otherwise the customer's
// money is recorded as consumed while the order never completes.
func TestReconcileLeavesIntentClaimableWhenSettlementFails(t *testing.T) {
	f := newReconcileFixture(t)
	intent := f.seedIntent(t, 42, "13.4837", time.Now().Add(30*time.Minute))
	f.transfers = []usdt.Transfer{transferOf("tx-abc", "13.4837", time.Now())}
	f.recorder.err = context.DeadlineExceeded

	if err := f.svc.ReconcileOnce(context.Background()); err == nil {
		t.Fatal("ReconcileOnce() = nil error, want the settlement failure surfaced")
	}

	reloaded := f.client.USDTPaymentIntent.GetX(context.Background(), intent.ID)
	if reloaded.Status != USDTIntentStatusPending {
		t.Fatalf("intent status = %q after a failed settlement, want %q so the next pass retries",
			reloaded.Status, USDTIntentStatusPending)
	}
	deposit := f.client.USDTDeposit.Query().OnlyX(context.Background())
	if deposit.Status != USDTDepositStatusUnmatched {
		t.Fatalf("deposit status = %q after a failed settlement, want %q",
			deposit.Status, USDTDepositStatusUnmatched)
	}
}

func TestReconcileRecordsUnmatchedDepositsForOperators(t *testing.T) {
	f := newReconcileFixture(t)
	f.seedIntent(t, 42, "13.4837", time.Now().Add(30*time.Minute))
	f.transfers = []usdt.Transfer{
		transferOf("tx-wrong-amount", "20.0000", time.Now()),
		transferOf("tx-right", "13.4837", time.Now()),
	}

	if err := f.svc.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce() error = %v", err)
	}

	unmatched, err := f.svc.ListUnmatchedDeposits(context.Background(), 50)
	if err != nil {
		t.Fatalf("ListUnmatchedDeposits() error = %v", err)
	}
	if len(unmatched) != 1 || unmatched[0].TxHash != "tx-wrong-amount" {
		t.Fatalf("unmatched deposits = %+v, want only tx-wrong-amount", unmatched)
	}
}

func TestCloseExpiredIntentsReleasesSlots(t *testing.T) {
	f := newReconcileFixture(t)
	f.seedIntent(t, 42, "13.4837", time.Now().Add(-time.Minute))
	f.seedIntent(t, 43, "13.4838", time.Now().Add(time.Hour))

	closed, err := NewUSDTIntentService(f.client, nil).CloseExpiredIntents(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("CloseExpiredIntents() error = %v", err)
	}
	if closed != 1 {
		t.Fatalf("closed %d intents, want 1", closed)
	}
	stillPending := f.client.USDTPaymentIntent.Query().
		Where(usdtpaymentintent.StatusEQ(USDTIntentStatusPending)).CountX(context.Background())
	if stillPending != 1 {
		t.Fatalf("%d intents still pending, want 1", stillPending)
	}
}

// Manual binding is the escape hatch for deposits the matcher deliberately
// refused. It must still verify the amount rather than trusting the operator.
func TestBindDepositToOrderRequiresMatchingAmount(t *testing.T) {
	f := newReconcileFixture(t)
	intent := f.seedIntent(t, 42, "13.4837", time.Now().Add(30*time.Minute))
	f.transfers = []usdt.Transfer{transferOf("tx-wrong", "20.0000", time.Now())}
	if err := f.svc.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce() error = %v", err)
	}
	deposit := f.client.USDTDeposit.Query().OnlyX(context.Background())

	if err := f.svc.BindDepositToOrder(context.Background(), deposit.ID, intent.OrderID, false); err == nil {
		t.Fatal("BindDepositToOrder() with a mismatched amount = nil error, want refusal")
	}
	if len(f.recorder.calls) != 0 {
		t.Fatalf("settled %d orders on a refused bind, want 0", len(f.recorder.calls))
	}

	// With an explicit override an operator can still force it through, and the
	// forced decision is what gets recorded.
	if err := f.svc.BindDepositToOrder(context.Background(), deposit.ID, intent.OrderID, true); err != nil {
		t.Fatalf("BindDepositToOrder(force) error = %v", err)
	}
	if len(f.recorder.calls) != 1 {
		t.Fatalf("settled %d orders on a forced bind, want 1", len(f.recorder.calls))
	}
}

func TestBindDepositToOrderRejectsAlreadyMatchedDeposits(t *testing.T) {
	f := newReconcileFixture(t)
	f.seedIntent(t, 42, "13.4837", time.Now().Add(30*time.Minute))
	f.transfers = []usdt.Transfer{transferOf("tx-abc", "13.4837", time.Now())}
	if err := f.svc.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce() error = %v", err)
	}
	deposit := f.client.USDTDeposit.Query().OnlyX(context.Background())
	f.seedIntent(t, 99, "13.4837", time.Now().Add(30*time.Minute))

	if err := f.svc.BindDepositToOrder(context.Background(), deposit.ID, 99, true); err == nil {
		t.Fatal("BindDepositToOrder() on a spent deposit = nil error, want refusal")
	}
}
