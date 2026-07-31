package service

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/usdt"
)

// USDTReconcileTimeout bounds one reconciliation pass, including the TronGrid
// round-trip.
const USDTReconcileTimeout = 30 * time.Second

// ReconcileUSDTDeposits runs one on-chain reconciliation pass and then releases
// the amount slots of intents that aged out.
//
// Driven by PaymentOrderExpiryService's existing ticker rather than a dedicated
// one: it already holds a leader lock and already runs reconcile-before-expire,
// which is exactly the ordering USDT needs. A confirmed TRON transfer is not
// visible to us for ~57s anyway (only_confirmed waits for block
// solidification), so a separate faster loop would buy very little.
func (s *PaymentService) ReconcileUSDTDeposits(ctx context.Context) error {
	if s == nil || s.entClient == nil {
		return nil
	}
	if s.usdtReconcile == nil {
		return nil
	}
	reconcileErr := s.usdtReconcile.ReconcileOnce(ctx)

	if s.usdtIntents != nil {
		if closed, err := s.usdtIntents.CloseExpiredIntents(ctx, time.Now()); err != nil {
			slog.Warn("[USDTReconcile] failed to close expired intents", "error", err)
		} else if closed > 0 {
			slog.Info("[USDTReconcile] closed expired intents", "count", closed)
		}
	}
	return reconcileErr
}

// EnableUSDTReconciliation wires on-chain settlement. Called from the wire
// provider so NewPaymentService's signature stays untouched.
func (s *PaymentService) EnableUSDTReconciliation() {
	reconciler := NewUSDTReconcileService(s.entClient, s.settleUSDTOrder)
	reconciler.SetConfigLoader(s.loadUSDTChannelConfig)
	s.usdtReconcile = reconciler
}

// loadUSDTChannelConfig reads and validates a USDT channel's admin-entered
// configuration.
func (s *PaymentService) loadUSDTChannelConfig(ctx context.Context, providerInstanceID string) (usdt.Config, error) {
	instanceID, err := strconv.ParseInt(providerInstanceID, 10, 64)
	if err != nil {
		return usdt.Config{}, fmt.Errorf("usdt provider instance id %q is not numeric", providerInstanceID)
	}
	if s.loadBalancer == nil {
		return usdt.Config{}, fmt.Errorf("usdt reconcile: no load balancer wired")
	}
	raw, err := s.loadBalancer.GetInstanceConfig(ctx, instanceID)
	if err != nil {
		return usdt.Config{}, fmt.Errorf("load usdt instance %d config: %w", instanceID, err)
	}
	return usdt.ParseConfig(raw)
}

// settleUSDTOrder credits an order from a matched on-chain deposit.
//
// The amount handed to toPaid is the order's own CNY payable, not the USDT
// figure: the order is CNY-denominated and that number is the agreed price,
// fixed at checkout by the rate snapshot. What was actually verified is the
// exact USDT amount, which the matcher already established. The audit entry
// below records the chain facts so the books can always be tied back to a
// specific transaction.
func (s *PaymentService) settleUSDTOrder(ctx context.Context, intent *dbent.USDTPaymentIntent, deposit *dbent.USDTDeposit) error {
	order, err := s.entClient.PaymentOrder.Get(ctx, intent.OrderID)
	if err != nil {
		return fmt.Errorf("load usdt order %d: %w", intent.OrderID, err)
	}

	s.writeAuditLog(ctx, order.ID, "USDT_DEPOSIT_MATCHED", payment.TypeUSDT, map[string]any{
		"txHash":         deposit.TxHash,
		"depositID":      deposit.ID,
		"amountUSDT":     deposit.AmountUsdt,
		"fromAddress":    deposit.FromAddress,
		"toAddress":      deposit.Address,
		"blockTimestamp": deposit.BlockTimestamp,
		"rate":           intent.Rate,
		"baseRate":       intent.BaseRate,
		"ratePremium":    intent.PremiumPercent,
		"rateSource":     intent.RateSource,
		"rateQuotedAt":   intent.RateQuotedAt,
		"payAmountCNY":   order.PayAmount,
	})

	return s.toPaid(ctx, order, deposit.TxHash, order.PayAmount, payment.TypeUSDT)
}
