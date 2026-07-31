//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// usdtRefundUserRepo records balance deductions. It embeds the shared stub so
// only the one method under test needs overriding.
type usdtRefundUserRepo struct {
	*userRepoStub
	deducted float64
	calls    int
}

func (r *usdtRefundUserRepo) DeductBalance(_ context.Context, _ int64, amount float64) error {
	r.deducted += amount
	r.calls++
	return nil
}

type usdtRefundFixture struct {
	client   *dbent.Client
	svc      *PaymentService
	userRepo *usdtRefundUserRepo
	order    *dbent.PaymentOrder
}

func newUSDTRefundFixture(t *testing.T, status string, paymentType string) *usdtRefundFixture {
	t.Helper()
	ctx := context.Background()
	client := newUSDTTestClient(t)

	user, err := client.User.Create().
		SetEmail("usdt-refund@example.com").
		SetPasswordHash("hash").
		SetUsername("usdt-refund-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(100).
		SetPayAmount(100).
		SetFeeRate(0).
		SetRechargeCode("USDT-REFUND-ORDER").
		SetOutTradeNo("sub2_usdt_refund_order").
		SetPaymentType(paymentType).
		SetPaymentTradeNo("tx-original-deposit").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(status).
		SetRefundAmount(100).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	repo := &usdtRefundUserRepo{userRepoStub: &userRepoStub{}}
	return &usdtRefundFixture{
		client:   client,
		svc:      &PaymentService{entClient: client, userRepo: repo},
		userRepo: repo,
		order:    order,
	}
}

func TestSettleUSDTRefundManuallyClosesTheRefund(t *testing.T) {
	ctx := context.Background()
	f := newUSDTRefundFixture(t, OrderStatusRefundPending, payment.TypeUSDT)

	result, err := f.svc.SettleUSDTRefundManually(ctx, USDTManualRefundSettlement{
		OrderID:    f.order.ID,
		TxHash:     "tx-payout-abc",
		AmountUSDT: "13.4837",
		Operator:   "admin:1",
	})
	require.NoError(t, err)
	require.True(t, result.Success)

	reloaded := f.client.PaymentOrder.GetX(ctx, f.order.ID)
	require.Equal(t, OrderStatusRefunded, reloaded.Status)
	require.NotNil(t, reloaded.RefundAt)

	// The balance deduction was rolled back when the refund went pending, so
	// closing it out has to deduct again — otherwise the customer keeps both
	// the balance and the USDT.
	require.Equal(t, 1, f.userRepo.calls)
	require.InDelta(t, 100.0, f.userRepo.deducted, 0.001)
}

// The payout transaction is the only evidence the money actually moved, so it
// is recorded in the audit trail rather than just implied by the status.
func TestSettleUSDTRefundManuallyRecordsThePayoutTransaction(t *testing.T) {
	ctx := context.Background()
	f := newUSDTRefundFixture(t, OrderStatusRefundPending, payment.TypeUSDT)

	_, err := f.svc.SettleUSDTRefundManually(ctx, USDTManualRefundSettlement{
		OrderID:    f.order.ID,
		TxHash:     "tx-payout-abc",
		AmountUSDT: "13.4837",
		Operator:   "admin:7",
	})
	require.NoError(t, err)

	logEntry, err := f.client.PaymentAuditLog.Query().
		Where(
			paymentauditlog.OrderIDEQ("1"),
			paymentauditlog.ActionEQ("USDT_REFUND_SETTLED_MANUAL"),
		).
		Only(ctx)
	require.NoError(t, err)
	require.Contains(t, logEntry.Detail, "tx-payout-abc")
	require.Contains(t, logEntry.Detail, "13.4837")
	require.Equal(t, "admin:7", logEntry.Operator)
}

// Without a transaction hash there is no evidence a payout happened, so the
// refund must not be closed.
func TestSettleUSDTRefundManuallyRequiresATransactionHash(t *testing.T) {
	ctx := context.Background()
	f := newUSDTRefundFixture(t, OrderStatusRefundPending, payment.TypeUSDT)

	_, err := f.svc.SettleUSDTRefundManually(ctx, USDTManualRefundSettlement{
		OrderID:  f.order.ID,
		TxHash:   "   ",
		Operator: "admin:1",
	})
	require.Error(t, err)
	require.Equal(t, "MISSING_TX_HASH", infraerrors.Reason(err))

	reloaded := f.client.PaymentOrder.GetX(ctx, f.order.ID)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status)
	require.Zero(t, f.userRepo.calls)
}

func TestSettleUSDTRefundManuallyRejectsWrongStatus(t *testing.T) {
	for _, status := range []string{OrderStatusCompleted, OrderStatusRefunded, OrderStatusPending, OrderStatusRefundRequested} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			f := newUSDTRefundFixture(t, status, payment.TypeUSDT)

			_, err := f.svc.SettleUSDTRefundManually(ctx, USDTManualRefundSettlement{
				OrderID:  f.order.ID,
				TxHash:   "tx-payout-abc",
				Operator: "admin:1",
			})
			require.Error(t, err)
			require.Equal(t, "INVALID_STATUS", infraerrors.Reason(err))
			require.Zero(t, f.userRepo.calls)
		})
	}
}

// This endpoint bypasses the gateway refund path entirely, so it must refuse
// orders whose money did not go out on-chain.
func TestSettleUSDTRefundManuallyRejectsNonUSDTOrders(t *testing.T) {
	ctx := context.Background()
	f := newUSDTRefundFixture(t, OrderStatusRefundPending, payment.TypeAlipay)

	_, err := f.svc.SettleUSDTRefundManually(ctx, USDTManualRefundSettlement{
		OrderID:  f.order.ID,
		TxHash:   "tx-payout-abc",
		Operator: "admin:1",
	})
	require.Error(t, err)
	require.Equal(t, "NOT_USDT_ORDER", infraerrors.Reason(err))
	require.Zero(t, f.userRepo.calls)
}

func TestSettleUSDTRefundManuallyRejectsUnknownOrder(t *testing.T) {
	ctx := context.Background()
	f := newUSDTRefundFixture(t, OrderStatusRefundPending, payment.TypeUSDT)

	_, err := f.svc.SettleUSDTRefundManually(ctx, USDTManualRefundSettlement{
		OrderID:  99999,
		TxHash:   "tx-payout-abc",
		Operator: "admin:1",
	})
	require.Error(t, err)
	require.Equal(t, "NOT_FOUND", infraerrors.Reason(err))
}

// A double submission must not deduct the customer's balance twice.
func TestSettleUSDTRefundManuallyIsNotDoubleCharged(t *testing.T) {
	ctx := context.Background()
	f := newUSDTRefundFixture(t, OrderStatusRefundPending, payment.TypeUSDT)
	settlement := USDTManualRefundSettlement{
		OrderID:    f.order.ID,
		TxHash:     "tx-payout-abc",
		AmountUSDT: "13.4837",
		Operator:   "admin:1",
	}

	_, err := f.svc.SettleUSDTRefundManually(ctx, settlement)
	require.NoError(t, err)

	_, err = f.svc.SettleUSDTRefundManually(ctx, settlement)
	require.Error(t, err, "a second settlement must be refused, not silently reapplied")
	require.Equal(t, 1, f.userRepo.calls)
}

// A refund that went pending without its deduction being rolled back must not
// be deducted a second time on settlement.
func TestSettleUSDTRefundManuallySkipsDeductionWhenRollbackFailed(t *testing.T) {
	ctx := context.Background()
	f := newUSDTRefundFixture(t, OrderStatusRefundPending, payment.TypeUSDT)

	_, err := f.client.PaymentAuditLog.Create().
		SetOrderID("1").
		SetAction("REFUND_PENDING").
		SetOperator("admin").
		SetDetail(`{"refundID":"manual","deductionRollbackOK":false}`).
		Save(ctx)
	require.NoError(t, err)

	result, err := f.svc.SettleUSDTRefundManually(ctx, USDTManualRefundSettlement{
		OrderID:  f.order.ID,
		TxHash:   "tx-payout-abc",
		Operator: "admin:1",
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Zero(t, f.userRepo.calls, "balance was never restored, so it must not be deducted again")
}
