package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/usdtdeposit"
	"github.com/Wei-Shaw/sub2api/ent/usdtpaymentintent"
	"github.com/Wei-Shaw/sub2api/internal/payment/usdt"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

// USDT deposit ledger statuses.
const (
	USDTDepositStatusUnmatched = "UNMATCHED"
	USDTDepositStatusMatched   = "MATCHED"
	USDTDepositStatusIgnored   = "IGNORED"
)

const (
	// usdtDepositLookbackSkew widens the chain query window slightly before the
	// oldest pending intent, covering clock drift between us and TronGrid.
	usdtDepositLookbackSkew = 10 * time.Minute
	// usdtTransferEarlySkew is how far before an intent was created a transfer
	// may land and still be considered payment for it. Some wallets broadcast
	// moments before our order row commits.
	usdtTransferEarlySkew = 5 * time.Minute

	usdtPendingIntentScanLimit  = 500
	usdtUnmatchedDepositDefault = 100
)

// usdtSettleFunc credits an order once a deposit has been matched to it.
type usdtSettleFunc func(ctx context.Context, intent *dbent.USDTPaymentIntent, deposit *dbent.USDTDeposit) error

// USDTReconcileService matches confirmed on-chain deposits against pending
// payment intents.
//
// This is the only thing that can settle a USDT order. The provider always
// reports "pending", so there is exactly one code path from "money arrived" to
// "order paid", and it runs through the exact-amount match below.
type USDTReconcileService struct {
	entClient *dbent.Client
	settle    usdtSettleFunc

	// Injection points, overridden in tests. Production wiring is set by
	// NewUSDTReconcileService.
	fetchTransfers func(ctx context.Context, cfg usdt.Config, since time.Time) ([]usdt.Transfer, error)
	loadConfig     func(ctx context.Context, providerInstanceID string) (usdt.Config, error)
}

// NewUSDTReconcileService creates the reconciler. settle is invoked once per
// successfully claimed deposit.
func NewUSDTReconcileService(entClient *dbent.Client, settle usdtSettleFunc) *USDTReconcileService {
	svc := &USDTReconcileService{entClient: entClient, settle: settle}
	svc.fetchTransfers = fetchUSDTTransfers
	return svc
}

// SetConfigLoader injects how a provider instance's decrypted config is read.
func (s *USDTReconcileService) SetConfigLoader(loader func(ctx context.Context, providerInstanceID string) (usdt.Config, error)) {
	s.loadConfig = loader
}

func fetchUSDTTransfers(ctx context.Context, cfg usdt.Config, since time.Time) ([]usdt.Transfer, error) {
	client, err := usdt.NewTronClient(cfg.TronOptions())
	if err != nil {
		return nil, err
	}
	return client.ListIncomingTransfers(ctx, cfg.WalletAddress, since)
}

// ReconcileOnce runs a single reconciliation pass.
//
// It exits immediately when nothing is outstanding: TronGrid's free tier is
// finite and polling it with no pending intent cannot produce a settlement.
func (s *USDTReconcileService) ReconcileOnce(ctx context.Context) error {
	intents, err := s.pendingIntents(ctx)
	if err != nil {
		return err
	}
	if len(intents) == 0 {
		return nil
	}

	var firstErr error
	for instanceID, group := range groupIntentsByInstance(intents) {
		if err := s.reconcileInstance(ctx, instanceID, group); err != nil {
			slog.Error("[USDTReconcile] instance pass failed", "instance", instanceID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (s *USDTReconcileService) pendingIntents(ctx context.Context) ([]*dbent.USDTPaymentIntent, error) {
	intents, err := s.entClient.USDTPaymentIntent.Query().
		Where(
			usdtpaymentintent.StatusEQ(USDTIntentStatusPending),
			usdtpaymentintent.ExpiresAtGT(time.Now()),
		).
		Order(dbent.Asc(usdtpaymentintent.FieldCreatedAt)).
		Limit(usdtPendingIntentScanLimit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query pending usdt intents: %w", err)
	}
	return intents, nil
}

func groupIntentsByInstance(intents []*dbent.USDTPaymentIntent) map[string][]*dbent.USDTPaymentIntent {
	grouped := make(map[string][]*dbent.USDTPaymentIntent)
	for _, intent := range intents {
		grouped[intent.ProviderInstanceID] = append(grouped[intent.ProviderInstanceID], intent)
	}
	return grouped
}

// reconcileInstance runs one chain query per receiving address, not per order.
func (s *USDTReconcileService) reconcileInstance(ctx context.Context, instanceID string, intents []*dbent.USDTPaymentIntent) error {
	if s.loadConfig == nil {
		return fmt.Errorf("usdt reconcile: no config loader wired")
	}
	cfg, err := s.loadConfig(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("load usdt channel config for instance %s: %w", instanceID, err)
	}

	transfers, err := s.fetchTransfers(ctx, cfg, oldestIntentCreatedAt(intents).Add(-usdtDepositLookbackSkew))
	if err != nil {
		return fmt.Errorf("list usdt transfers for instance %s: %w", instanceID, err)
	}

	var firstErr error
	for _, transfer := range transfers {
		if err := s.processTransfer(ctx, transfer, intents); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func oldestIntentCreatedAt(intents []*dbent.USDTPaymentIntent) time.Time {
	oldest := time.Now()
	for _, intent := range intents {
		if intent.CreatedAt.Before(oldest) {
			oldest = intent.CreatedAt
		}
	}
	return oldest
}

// processTransfer records a transfer in the ledger and settles it if — and only
// if — it exactly matches a pending intent.
func (s *USDTReconcileService) processTransfer(ctx context.Context, transfer usdt.Transfer, intents []*dbent.USDTPaymentIntent) error {
	deposit, err := s.recordDeposit(ctx, transfer)
	if err != nil {
		return err
	}
	if deposit.Status != USDTDepositStatusUnmatched {
		// Already consumed by an earlier pass. Rescanning the same window every
		// cycle is normal, so this is the common case, not an anomaly.
		return nil
	}

	intent := matchIntent(transfer, intents)
	if intent == nil {
		// Wrong amount, or outside the intent's window. Deliberately left for an
		// operator: guessing which order a mismatched payment belongs to is how
		// you credit the wrong customer.
		return nil
	}
	return s.claimAndSettle(ctx, intent, deposit)
}

// recordDeposit writes the transfer to the ledger, returning the existing row
// if it was already seen. The unique index on (tx_hash, address, amount) is the
// replay guard: one transfer can only ever be recorded once.
func (s *USDTReconcileService) recordDeposit(ctx context.Context, transfer usdt.Transfer) (*dbent.USDTDeposit, error) {
	amount := usdt.CanonicalAmount(transfer.Amount)
	deposit, err := s.entClient.USDTDeposit.Create().
		SetTxHash(transfer.TxHash).
		SetAddress(transfer.To).
		SetFromAddress(transfer.From).
		SetTokenContract(transfer.TokenContract).
		SetAmountUsdt(amount).
		SetBlockTimestamp(transfer.BlockTimestamp).
		SetStatus(USDTDepositStatusUnmatched).
		Save(ctx)
	if err == nil {
		return deposit, nil
	}
	if !dbent.IsConstraintError(err) {
		return nil, fmt.Errorf("record usdt deposit %s: %w", transfer.TxHash, err)
	}

	existing, lookupErr := s.entClient.USDTDeposit.Query().
		Where(
			usdtdeposit.TxHashEQ(transfer.TxHash),
			usdtdeposit.AddressEQ(transfer.To),
			usdtdeposit.AmountUsdtEQ(amount),
		).
		Only(ctx)
	if lookupErr != nil {
		return nil, fmt.Errorf("reload existing usdt deposit %s: %w", transfer.TxHash, lookupErr)
	}
	return existing, nil
}

// matchIntent finds the pending intent a transfer settles, or nil.
//
// Matching is on exact amount equality plus a time window. There is no
// tolerance and no nearest-match fallback: the whole design rests on each
// pending order having a distinct amount, so an amount that does not match
// exactly is not evidence of anything.
func matchIntent(transfer usdt.Transfer, intents []*dbent.USDTPaymentIntent) *dbent.USDTPaymentIntent {
	amount := usdt.CanonicalAmount(transfer.Amount)
	for _, intent := range intents {
		if intent.Status != USDTIntentStatusPending {
			continue
		}
		if intent.AmountUsdt != amount {
			continue
		}
		if !transferWithinIntentWindow(transfer, intent) {
			continue
		}
		return intent
	}
	return nil
}

func transferWithinIntentWindow(transfer usdt.Transfer, intent *dbent.USDTPaymentIntent) bool {
	if transfer.BlockTimestamp.Before(intent.CreatedAt.Add(-usdtTransferEarlySkew)) {
		return false
	}
	return !transfer.BlockTimestamp.After(intent.ExpiresAt)
}

// claimAndSettle takes both sides of the match before crediting anything.
//
// Both updates are compare-and-swap on the current status, so two passes racing
// on the same deposit cannot both settle it. If settlement then fails, the
// claim is released: leaving the deposit marked spent while the order never
// completes would strand the customer's money with no retry path.
func (s *USDTReconcileService) claimAndSettle(ctx context.Context, intent *dbent.USDTPaymentIntent, deposit *dbent.USDTDeposit) error {
	claimedDeposit, err := s.entClient.USDTDeposit.Update().
		Where(
			usdtdeposit.IDEQ(deposit.ID),
			usdtdeposit.StatusEQ(USDTDepositStatusUnmatched),
		).
		SetStatus(USDTDepositStatusMatched).
		SetMatchedOrderID(intent.OrderID).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("claim usdt deposit %d: %w", deposit.ID, err)
	}
	if claimedDeposit != 1 {
		return nil // another pass got there first
	}

	claimedIntent, err := s.entClient.USDTPaymentIntent.Update().
		Where(
			usdtpaymentintent.IDEQ(intent.ID),
			usdtpaymentintent.StatusEQ(USDTIntentStatusPending),
		).
		SetStatus(USDTIntentStatusMatched).
		SetMatchedTxHash(deposit.TxHash).
		Save(ctx)
	if err != nil || claimedIntent != 1 {
		s.releaseDepositClaim(ctx, deposit.ID)
		if err != nil {
			return fmt.Errorf("claim usdt intent %d: %w", intent.ID, err)
		}
		return nil // intent was taken concurrently
	}

	if err := s.settle(ctx, intent, deposit); err != nil {
		s.releaseDepositClaim(ctx, deposit.ID)
		s.releaseIntentClaim(ctx, intent.ID)
		return fmt.Errorf("settle usdt order %d from tx %s: %w", intent.OrderID, deposit.TxHash, err)
	}
	slog.Info("[USDTReconcile] settled order from on-chain deposit",
		"order_id", intent.OrderID, "tx_hash", deposit.TxHash, "amount_usdt", deposit.AmountUsdt)
	return nil
}

func (s *USDTReconcileService) releaseDepositClaim(ctx context.Context, depositID int64) {
	if _, err := s.entClient.USDTDeposit.UpdateOneID(depositID).
		SetStatus(USDTDepositStatusUnmatched).
		ClearMatchedOrderID().
		Save(ctx); err != nil {
		slog.Error("[USDTReconcile] failed to release deposit claim", "deposit_id", depositID, "error", err)
	}
}

func (s *USDTReconcileService) releaseIntentClaim(ctx context.Context, intentID int64) {
	if _, err := s.entClient.USDTPaymentIntent.UpdateOneID(intentID).
		SetStatus(USDTIntentStatusPending).
		ClearMatchedTxHash().
		Save(ctx); err != nil {
		slog.Error("[USDTReconcile] failed to release intent claim", "intent_id", intentID, "error", err)
	}
}

// ListUnmatchedDeposits returns money that arrived without settling an order,
// for operator review.
func (s *USDTReconcileService) ListUnmatchedDeposits(ctx context.Context, limit int) ([]*dbent.USDTDeposit, error) {
	if limit <= 0 || limit > usdtUnmatchedDepositDefault*10 {
		limit = usdtUnmatchedDepositDefault
	}
	return s.entClient.USDTDeposit.Query().
		Where(usdtdeposit.StatusEQ(USDTDepositStatusUnmatched)).
		Order(dbent.Desc(usdtdeposit.FieldBlockTimestamp)).
		Limit(limit).
		All(ctx)
}

// BindDepositToOrder settles an unmatched deposit against an order by hand.
//
// The amount is still verified. force exists because some real situations
// genuinely need it — a customer who sent a slightly wrong amount, or paid
// after the window — but it must be an explicit decision that gets recorded,
// not the default behaviour.
func (s *USDTReconcileService) BindDepositToOrder(ctx context.Context, depositID, orderID int64, force bool) error {
	deposit, err := s.entClient.USDTDeposit.Get(ctx, depositID)
	if err != nil {
		return infraerrors.NotFound("NOT_FOUND", "usdt deposit not found")
	}
	if deposit.Status != USDTDepositStatusUnmatched {
		return infraerrors.Conflict("DEPOSIT_ALREADY_RESOLVED",
			"this deposit has already been matched or ignored").
			WithMetadata(map[string]string{"status": deposit.Status})
	}

	intent, err := s.entClient.USDTPaymentIntent.Query().
		Where(usdtpaymentintent.OrderIDEQ(orderID)).
		Only(ctx)
	if err != nil {
		return infraerrors.NotFound("NOT_FOUND", "no usdt payment intent for this order")
	}
	if intent.Status != USDTIntentStatusPending {
		return infraerrors.Conflict("INTENT_ALREADY_RESOLVED",
			"this order's usdt payment has already been resolved").
			WithMetadata(map[string]string{"status": intent.Status})
	}
	if !force && !sameUSDTAmount(deposit.AmountUsdt, intent.AmountUsdt) {
		return infraerrors.BadRequest("AMOUNT_MISMATCH",
			"deposit amount does not match the order; confirm and retry with force to override").
			WithMetadata(map[string]string{
				"deposit_amount": deposit.AmountUsdt,
				"order_amount":   intent.AmountUsdt,
			})
	}
	return s.claimAndSettle(ctx, intent, deposit)
}

// IgnoreDeposit marks a deposit as reviewed and requiring no further action.
func (s *USDTReconcileService) IgnoreDeposit(ctx context.Context, depositID int64, notes string) error {
	updated, err := s.entClient.USDTDeposit.Update().
		Where(
			usdtdeposit.IDEQ(depositID),
			usdtdeposit.StatusEQ(USDTDepositStatusUnmatched),
		).
		SetStatus(USDTDepositStatusIgnored).
		SetNotes(strings.TrimSpace(notes)).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("ignore usdt deposit: %w", err)
	}
	if updated == 0 {
		return infraerrors.Conflict("DEPOSIT_ALREADY_RESOLVED", "this deposit has already been resolved")
	}
	return nil
}

func sameUSDTAmount(a, b string) bool {
	left, errLeft := decimal.NewFromString(a)
	right, errRight := decimal.NewFromString(b)
	if errLeft != nil || errRight != nil {
		return a == b
	}
	return left.Equal(right)
}
