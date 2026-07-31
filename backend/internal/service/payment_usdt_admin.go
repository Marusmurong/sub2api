package service

import (
	"context"
	"fmt"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/usdtdeposit"
	"github.com/Wei-Shaw/sub2api/internal/payment/usdt"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// USDTDepositListParams filters the deposit ledger for the admin view.
type USDTDepositListParams struct {
	Status   string
	Page     int
	PageSize int
}

// USDTRatePreview is what the admin rate-calibration panel shows.
//
// Both the raw market rate and the marked-up rate are exposed because
// calibrating the premium against live OTC quotes is a recurring operational
// task, not a one-time setup step.
type USDTRatePreview struct {
	Rate           string    `json:"rate"`
	BaseRate       string    `json:"base_rate"`
	PremiumPercent string    `json:"premium_percent"`
	Source         string    `json:"source"`
	QuotedAt       time.Time `json:"quoted_at"`
	Stale          bool      `json:"stale"`
}

// ListUSDTDeposits returns the on-chain deposit ledger, newest first.
func (s *PaymentService) ListUSDTDeposits(ctx context.Context, p USDTDepositListParams) ([]*dbent.USDTDeposit, int, error) {
	query := s.entClient.USDTDeposit.Query()
	if p.Status != "" {
		query = query.Where(usdtdeposit.StatusEQ(p.Status))
	}
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count usdt deposits: %w", err)
	}
	pageSize, page := applyPagination(p.PageSize, p.Page)
	deposits, err := query.
		Order(dbent.Desc(usdtdeposit.FieldBlockTimestamp)).
		Limit(pageSize).
		Offset((page - 1) * pageSize).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("query usdt deposits: %w", err)
	}
	return deposits, total, nil
}

// BindUSDTDepositToOrder settles an unmatched deposit against an order by hand.
func (s *PaymentService) BindUSDTDepositToOrder(ctx context.Context, depositID, orderID int64, force bool) error {
	if s.usdtReconcile == nil {
		return infraerrors.ServiceUnavailable("USDT_CHANNEL_UNAVAILABLE", "usdt reconciliation is not enabled")
	}
	return s.usdtReconcile.BindDepositToOrder(ctx, depositID, orderID, force)
}

// IgnoreUSDTDeposit marks a deposit as reviewed and needing no action.
func (s *PaymentService) IgnoreUSDTDeposit(ctx context.Context, depositID int64, notes string) error {
	if s.usdtReconcile == nil {
		return infraerrors.ServiceUnavailable("USDT_CHANNEL_UNAVAILABLE", "usdt reconciliation is not enabled")
	}
	return s.usdtReconcile.IgnoreDeposit(ctx, depositID, notes)
}

// PreviewUSDTRate quotes the current rate for a channel so an operator can
// compare it against real OTC prices and adjust the premium.
func (s *PaymentService) PreviewUSDTRate(ctx context.Context, providerInstanceID int64) (*USDTRatePreview, error) {
	if s.usdtIntents == nil {
		return nil, infraerrors.ServiceUnavailable("USDT_CHANNEL_UNAVAILABLE", "usdt payments are not enabled")
	}
	cfg, err := s.loadUSDTChannelConfig(ctx, usdtInstanceIDString(providerInstanceID))
	if err != nil {
		return nil, infraerrors.BadRequest("USDT_CHANNEL_MISCONFIGURED", err.Error())
	}
	quote, err := s.usdtIntents.rates.Quote(ctx, cfg.RateOptions())
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("USDT_RATE_UNAVAILABLE", err.Error())
	}
	return &USDTRatePreview{
		Rate:           quote.Rate.String(),
		BaseRate:       quote.BaseRate.String(),
		PremiumPercent: quote.PremiumPercent.String(),
		Source:         quote.Source,
		QuotedAt:       quote.QuotedAt,
		Stale:          quote.Stale,
	}, nil
}

// GetUSDTPaymentInfo returns the payment-page payload for one of the caller's
// own orders, so a refreshed or reopened page can show the address and amount
// again.
func (s *PaymentService) GetUSDTPaymentInfo(ctx context.Context, orderID, userID int64) (*USDTPaymentInfo, error) {
	order, err := s.GetOrder(ctx, orderID, userID)
	if err != nil {
		return nil, err
	}
	if !isUSDTOrder(order) {
		return nil, infraerrors.BadRequest("NOT_USDT_ORDER", "this order was not paid with USDT")
	}
	if s.usdtIntents == nil {
		return nil, infraerrors.ServiceUnavailable("USDT_CHANNEL_UNAVAILABLE", "usdt payments are not enabled")
	}
	intent, err := s.usdtIntents.GetIntentByOrderID(ctx, order.ID)
	if err != nil {
		return nil, fmt.Errorf("load usdt intent: %w", err)
	}
	if intent == nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "no usdt payment details for this order")
	}
	return NewUSDTPaymentInfo(intent), nil
}

func usdtInstanceIDString(id int64) string {
	return fmt.Sprintf("%d", id)
}

// USDTDepositView is the admin-facing projection of a ledger row.
type USDTDepositView struct {
	ID             int64     `json:"id"`
	TxHash         string    `json:"tx_hash"`
	Address        string    `json:"address"`
	FromAddress    string    `json:"from_address"`
	AmountUSDT     string    `json:"amount_usdt"`
	BlockTimestamp time.Time `json:"block_timestamp"`
	Status         string    `json:"status"`
	MatchedOrderID *int64    `json:"matched_order_id,omitempty"`
	Notes          string    `json:"notes,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// NewUSDTDepositView projects a ledger row for the admin API.
func NewUSDTDepositView(deposit *dbent.USDTDeposit) USDTDepositView {
	view := USDTDepositView{
		ID:             deposit.ID,
		TxHash:         deposit.TxHash,
		Address:        deposit.Address,
		FromAddress:    deposit.FromAddress,
		AmountUSDT:     deposit.AmountUsdt,
		BlockTimestamp: deposit.BlockTimestamp,
		Status:         deposit.Status,
		MatchedOrderID: deposit.MatchedOrderID,
		CreatedAt:      deposit.CreatedAt,
	}
	if deposit.Notes != nil {
		view.Notes = *deposit.Notes
	}
	return view
}

// USDTNetworkLabel names the chain a deposit arrived on, for display.
func USDTNetworkLabel() string { return usdt.NetworkTRC20 }
