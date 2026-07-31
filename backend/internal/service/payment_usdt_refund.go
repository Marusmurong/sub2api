package service

import (
	"context"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// USDTManualRefundSettlement records an operator's off-chain payout.
type USDTManualRefundSettlement struct {
	OrderID int64
	// TxHash is the on-chain transaction that paid the customer back. It is the
	// only evidence the money actually moved, so it is required.
	TxHash string
	// AmountUSDT is what the operator actually sent, recorded verbatim for the
	// audit trail. The customer-facing accounting is driven by the order's CNY
	// refund amount, not by this figure.
	AmountUSDT string
	Operator   string
}

// SettleUSDTRefundManually closes out a USDT refund after an operator has paid
// the customer back on-chain.
//
// USDT refunds cannot be automated: transfers are irreversible and there is no
// upstream to instruct. The provider therefore reports "pending", parking the
// order in REFUND_PENDING, and this call is how it leaves that state.
//
// The accounting deliberately reuses the same finalisation path as a gateway
// refund (refundFinalizePlan → applyRefundFinalDeduction → markRefundOk) rather
// than reimplementing it. That matters because markRefundPending rolls the
// balance deduction back when a refund goes pending: the deduction has to be
// reapplied here, and getting that subtly different from the gateway path is
// how a customer ends up keeping both the balance and the USDT.
func (s *PaymentService) SettleUSDTRefundManually(ctx context.Context, in USDTManualRefundSettlement) (*RefundResult, error) {
	txHash := strings.TrimSpace(in.TxHash)
	if txHash == "" {
		return nil, infraerrors.BadRequest("MISSING_TX_HASH",
			"the on-chain payout transaction hash is required to settle a USDT refund")
	}

	order, err := s.entClient.PaymentOrder.Get(ctx, in.OrderID)
	if err != nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if !isUSDTOrder(order) {
		return nil, infraerrors.BadRequest("NOT_USDT_ORDER",
			"this endpoint only settles USDT refunds; use the gateway refund flow instead")
	}
	if order.Status != OrderStatusRefundPending {
		return nil, infraerrors.BadRequest("INVALID_STATUS",
			"only refund-pending orders can be settled manually").
			WithMetadata(map[string]string{"status": order.Status})
	}

	plan := s.refundFinalizePlan(order)
	// Mirrors QueryAndFinalizeRefund: if the pending transition failed to
	// restore the balance, it was never given back, so deducting again here
	// would charge the customer twice.
	if pendingDetail := s.latestRefundPendingDetail(ctx, order.ID); !pendingDetail.DeductionRollbackOK {
		plan.BalanceToDeduct = 0
		plan.SubDaysToDeduct = 0
	}

	operator := strings.TrimSpace(in.Operator)
	if operator == "" {
		operator = "admin"
	}
	s.writeAuditLog(ctx, order.ID, "USDT_REFUND_SETTLED_MANUAL", operator, map[string]any{
		"txHash":          txHash,
		"amountUSDT":      strings.TrimSpace(in.AmountUSDT),
		"refundAmountCNY": plan.RefundAmount,
		"balanceDeducted": plan.BalanceToDeduct,
	})

	if err := s.applyRefundFinalDeduction(ctx, plan); err != nil {
		return nil, err
	}
	return s.markRefundOk(ctx, plan)
}

// isUSDTOrder reports whether an order was paid through the USDT channel,
// tolerating orders that only recorded one of the two provider fields.
func isUSDTOrder(order *dbent.PaymentOrder) bool {
	if order == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(order.PaymentType), payment.TypeUSDT) {
		return true
	}
	if snapshot := psOrderProviderSnapshot(order); snapshot != nil &&
		strings.EqualFold(strings.TrimSpace(snapshot.ProviderKey), payment.TypeUSDT) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(psStringValue(order.ProviderKey)), payment.TypeUSDT)
}
