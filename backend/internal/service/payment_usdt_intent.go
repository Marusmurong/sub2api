package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/usdtpaymentintent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/usdt"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

// USDT payment intent statuses.
const (
	USDTIntentStatusPending = "PENDING"
	USDTIntentStatusMatched = "MATCHED"
	// USDTIntentStatusClosed is a terminal state for intents that expired
	// without a matching deposit. Closing releases the amount slot.
	USDTIntentStatusClosed = "CLOSED"
)

// usdtIntentGrace is how long an intent outlives its order.
//
// A customer who transfers at the last minute still needs the chain to confirm,
// which takes about a minute on TRON. Without the grace window that money would
// arrive against an intent we had already stopped looking at. Settlement itself
// still works because toPaid accepts CANCELLED and recently-EXPIRED orders.
const usdtIntentGrace = 15 * time.Minute

// ErrUSDTAmountSlotExhausted means every uniqueness tag on this address is
// already held by a pending order.
var ErrUSDTAmountSlotExhausted = errors.New("usdt amount slots exhausted")

// USDTIntentService allocates and closes the USDT side of payment orders.
type USDTIntentService struct {
	entClient *dbent.Client
	rates     *usdt.RateProvider
}

// NewUSDTIntentService creates the intent service. The rate provider is shared
// process-wide so all instances collapse onto one upstream fetch.
func NewUSDTIntentService(entClient *dbent.Client, rates *usdt.RateProvider) *USDTIntentService {
	if rates == nil {
		rates = usdt.NewRateProvider(nil, nil)
	}
	return &USDTIntentService{entClient: entClient, rates: rates}
}

// USDTAllocateIntentInput describes the order an intent is being allocated for.
type USDTAllocateIntentInput struct {
	OrderID            int64
	OutTradeNo         string
	ProviderInstanceID string
	// PayAmountCNY is the order's CNY payable. The order stays CNY-denominated;
	// only the intent knows about USDT.
	PayAmountCNY   float64
	OrderExpiresAt time.Time
	// Config is the decrypted provider instance config from the admin UI.
	Config map[string]string
}

// USDTPaymentInfo is the payment-page payload for a USDT order.
type USDTPaymentInfo struct {
	Address       string `json:"address"`
	Network       string `json:"network"`
	TokenContract string `json:"token_contract"`
	// AmountUSDT is the 4-decimal figure the customer must transfer exactly.
	// The last two digits are the uniqueness tag reconciliation matches on.
	AmountUSDT   string    `json:"amount_usdt"`
	Rate         string    `json:"rate"`
	RateSource   string    `json:"rate_source"`
	RateQuotedAt time.Time `json:"rate_quoted_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// NewUSDTPaymentInfo projects a stored intent into the client payload.
func NewUSDTPaymentInfo(intent *dbent.USDTPaymentIntent) *USDTPaymentInfo {
	if intent == nil {
		return nil
	}
	amount := intent.AmountUsdt
	if parsed, err := decimal.NewFromString(intent.AmountUsdt); err == nil {
		amount = usdt.FormatAmount(parsed)
	}
	return &USDTPaymentInfo{
		Address:       intent.Address,
		Network:       intent.Network,
		TokenContract: intent.TokenContract,
		AmountUSDT:    amount,
		Rate:          intent.Rate,
		RateSource:    intent.RateSource,
		RateQuotedAt:  intent.RateQuotedAt,
		ExpiresAt:     intent.ExpiresAt,
	}
}

// AllocateIntent prices a USDT order and reserves a unique transfer amount.
//
// Idempotent per order: a retry returns the existing intent rather than
// re-quoting, so a customer never sees the amount change under them.
func (s *USDTIntentService) AllocateIntent(ctx context.Context, in USDTAllocateIntentInput) (*dbent.USDTPaymentIntent, error) {
	if existing, err := s.GetIntentByOrderID(ctx, in.OrderID); err == nil && existing != nil {
		return existing, nil
	} else if err != nil && !dbent.IsNotFound(err) {
		return nil, fmt.Errorf("look up existing usdt intent: %w", err)
	}

	cfg, err := usdt.ParseConfig(in.Config)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("USDT_CHANNEL_MISCONFIGURED", err.Error())
	}
	payCNY := decimal.NewFromFloat(in.PayAmountCNY)
	if payCNY.LessThanOrEqual(decimal.Zero) {
		return nil, infraerrors.BadRequest("INVALID_AMOUNT", "usdt order pay amount must be positive")
	}

	quote, err := s.rates.Quote(ctx, cfg.RateOptions())
	if err != nil {
		// Fail closed. Pricing an order against a rate nobody can vouch for is
		// worse than telling the customer this method is briefly unavailable.
		return nil, infraerrors.ServiceUnavailable("USDT_RATE_UNAVAILABLE",
			"usdt exchange rate is unavailable, please try another payment method").
			WithMetadata(map[string]string{"detail": err.Error()})
	}

	base, err := usdt.BaseAmount(payCNY, quote.Rate)
	if err != nil {
		return nil, infraerrors.BadRequest("INVALID_AMOUNT", err.Error())
	}

	expiresAt := in.OrderExpiresAt.Add(usdtIntentGrace)
	for _, suffix := range usdt.SuffixCandidates() {
		amount, err := usdt.SuffixedAmount(base, suffix)
		if err != nil {
			return nil, err
		}
		intent, err := s.insertIntent(ctx, in, cfg, quote, amount, expiresAt)
		if err == nil {
			return intent, nil
		}
		if dbent.IsConstraintError(err) {
			// Either this tag is taken, or a concurrent request just allocated
			// for the same order. Check the latter before trying another tag.
			if existing, lookupErr := s.GetIntentByOrderID(ctx, in.OrderID); lookupErr == nil && existing != nil {
				return existing, nil
			}
			continue
		}
		return nil, fmt.Errorf("create usdt intent: %w", err)
	}

	return nil, infraerrors.TooManyRequests("USDT_AMOUNT_SLOT_EXHAUSTED",
		"too many pending USDT orders right now, please retry in a few minutes").
		WithCause(ErrUSDTAmountSlotExhausted)
}

func (s *USDTIntentService) insertIntent(
	ctx context.Context,
	in USDTAllocateIntentInput,
	cfg usdt.Config,
	quote *usdt.Quote,
	amount decimal.Decimal,
	expiresAt time.Time,
) (*dbent.USDTPaymentIntent, error) {
	return s.entClient.USDTPaymentIntent.Create().
		SetOrderID(in.OrderID).
		SetOutTradeNo(in.OutTradeNo).
		SetProviderInstanceID(in.ProviderInstanceID).
		SetAddress(cfg.WalletAddress).
		SetNetwork(cfg.Network).
		SetTokenContract(cfg.TokenContract).
		SetAmountUsdt(usdt.CanonicalAmount(amount)).
		SetRate(quote.Rate.String()).
		SetBaseRate(quote.BaseRate.String()).
		SetPremiumPercent(quote.PremiumPercent.String()).
		SetRateSource(quote.Source).
		SetRateQuotedAt(quote.QuotedAt).
		SetStatus(USDTIntentStatusPending).
		SetExpiresAt(expiresAt).
		Save(ctx)
}

// GetIntentByOrderID returns the intent for an order, or nil when there is none.
func (s *USDTIntentService) GetIntentByOrderID(ctx context.Context, orderID int64) (*dbent.USDTPaymentIntent, error) {
	intent, err := s.entClient.USDTPaymentIntent.Query().
		Where(usdtpaymentintent.OrderIDEQ(orderID)).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return intent, nil
}

// CloseExpiredIntents releases the amount slots of intents that aged out
// without a matching deposit, and reports how many were closed.
func (s *USDTIntentService) CloseExpiredIntents(ctx context.Context, now time.Time) (int, error) {
	return s.entClient.USDTPaymentIntent.Update().
		Where(
			usdtpaymentintent.StatusEQ(USDTIntentStatusPending),
			usdtpaymentintent.ExpiresAtLTE(now),
		).
		SetStatus(USDTIntentStatusClosed).
		Save(ctx)
}

// MarkIntentMatched claims an intent for a deposit. The status guard makes this
// a compare-and-swap: only the first caller wins, so two reconciliation passes
// racing on one deposit cannot both settle the order.
func (s *USDTIntentService) MarkIntentMatched(ctx context.Context, intentID int64, txHash string) (bool, error) {
	updated, err := s.entClient.USDTPaymentIntent.Update().
		Where(
			usdtpaymentintent.IDEQ(intentID),
			usdtpaymentintent.StatusEQ(USDTIntentStatusPending),
		).
		SetStatus(USDTIntentStatusMatched).
		SetMatchedTxHash(txHash).
		Save(ctx)
	if err != nil {
		return false, fmt.Errorf("mark usdt intent matched: %w", err)
	}
	return updated == 1, nil
}

// SetUSDTIntentService attaches the intent service without widening
// NewPaymentService's signature.
func (s *PaymentService) SetUSDTIntentService(intents *USDTIntentService) {
	s.usdtIntents = intents
}

// attachUSDTPaymentIntent allocates the USDT side of a freshly created order
// and fills in the payment-page payload. It is a no-op for every other channel.
//
// This runs after the order row exists because the unique transfer amount is
// reserved by a database constraint keyed on the order. A failure here fails
// the whole checkout — an unpriced USDT order is one the customer could never
// pay correctly, so surfacing the error beats leaving a dead order behind.
func (s *PaymentService) attachUSDTPaymentIntent(
	ctx context.Context,
	order *dbent.PaymentOrder,
	resp *CreateOrderResponse,
	sel *payment.InstanceSelection,
) error {
	if order == nil || resp == nil || sel == nil {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(sel.ProviderKey), payment.TypeUSDT) {
		return nil
	}
	if s.usdtIntents == nil {
		return infraerrors.ServiceUnavailable("USDT_CHANNEL_UNAVAILABLE",
			"usdt payments are not available on this deployment")
	}

	intent, err := s.usdtIntents.AllocateIntent(ctx, USDTAllocateIntentInput{
		OrderID:            order.ID,
		OutTradeNo:         order.OutTradeNo,
		ProviderInstanceID: strings.TrimSpace(sel.InstanceID),
		PayAmountCNY:       order.PayAmount,
		OrderExpiresAt:     order.ExpiresAt,
		Config:             sel.Config,
	})
	if err != nil {
		return err
	}
	resp.USDT = NewUSDTPaymentInfo(intent)
	return nil
}
