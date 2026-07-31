//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

// The cashback plugin (cashback/) is a side-car that reads orders through
// sub2api's admin API. It grants a bonus for any order where:
//
//	order_type   == "balance"
//	status       == "COMPLETED"
//	payment_type != "cashback"          (an exclusion, not an allow-list)
//	bonus base   == amount              (the credited CNY, not pay_amount)
//
// That is the entire contract, and it is why USDT needed no cashback changes.
// These tests exist so a future refactor cannot quietly break it — for example
// by denominating USDT orders in USDT, or by giving them their own order type.
// The plugin lives in a separate repository and would not fail this build.

func seedCompletedUSDTOrder(t *testing.T, client *dbent.Client, creditedCNY float64) *dbent.PaymentOrder {
	t.Helper()
	ctx := context.Background()

	user, err := client.User.Create().
		SetEmail("usdt-cashback@example.com").
		SetPasswordHash("hash").
		SetUsername("usdt-cashback-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(creditedCNY).
		SetPayAmount(creditedCNY).
		SetFeeRate(0).
		SetRechargeCode("USDT-CASHBACK-ORDER").
		SetOutTradeNo("sub2_usdt_cashback_order").
		SetPaymentType(payment.TypeUSDT).
		SetPaymentTradeNo("tx-deposit-abc").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetCompletedAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	return order
}

func TestSettledUSDTOrderMeetsTheCashbackContract(t *testing.T) {
	ctx := context.Background()
	client := newUSDTTestClient(t)
	seedCompletedUSDTOrder(t, client, 100)
	svc := &PaymentService{entClient: client}

	orders, total, err := svc.AdminListOrders(ctx, 0, OrderListParams{})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, orders, 1)

	order := orders[0]
	require.Equal(t, payment.OrderTypeBalance, order.OrderType,
		"cashback only grants on balance orders")
	require.Equal(t, OrderStatusCompleted, order.Status,
		"cashback only grants on completed orders")
	require.NotEqual(t, payment.TypeCashback, order.PaymentType,
		"a real top-up must never look like the plugin's own cashback ledger row")
	require.Equal(t, payment.TypeUSDT, order.PaymentType)
	require.InDelta(t, 100.0, order.Amount, 0.001,
		"amount is the cashback base and must stay in CNY, never USDT")
}

// USDT orders must remain CNY-denominated. If they ever carried a USDT currency
// the cashback plugin would compute a bonus roughly 7x too small, and revenue
// reporting would sum incompatible currencies.
func TestUSDTOrdersStayDenominatedInCNY(t *testing.T) {
	client := newUSDTTestClient(t)
	order := seedCompletedUSDTOrder(t, client, 100)

	require.Equal(t, payment.DefaultPaymentCurrency, PaymentOrderCurrency(order))
	require.Equal(t, "CNY", PaymentOrderCurrency(order))
}

// The plugin filters by payment type through this same query path.
func TestUSDTOrdersSurviveThePaymentTypeFilter(t *testing.T) {
	ctx := context.Background()
	client := newUSDTTestClient(t)
	seedCompletedUSDTOrder(t, client, 100)
	svc := &PaymentService{entClient: client}

	matched, total, err := svc.AdminListOrders(ctx, 0, OrderListParams{PaymentType: payment.TypeUSDT})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, matched, 1)

	excluded, total, err := svc.AdminListOrders(ctx, 0, OrderListParams{PaymentType: payment.TypeCashback})
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, excluded)
}

// Cashback ledger rows are synthetic and must not be refundable. USDT orders
// are real payments and must not accidentally inherit that exclusion.
func TestUSDTOrdersAreNotTreatedAsCashbackLedgerRows(t *testing.T) {
	client := newUSDTTestClient(t)
	order := seedCompletedUSDTOrder(t, client, 100)

	require.NotEqual(t, payment.TypeCashback, order.PaymentType)
	require.True(t, isUSDTOrder(order))
}

// Settlement records the chain facts and the CNY figure it credited, so the
// books can always be tied back to a specific transaction.
//
// The order is already COMPLETED here, which makes toPaid a no-op — that is
// deliberate: it exercises the audit write and the idempotency guard without
// dragging the whole redeem stack into this test.
func TestSettleUSDTOrderAuditsChainFactsAndTheCNYAmount(t *testing.T) {
	ctx := context.Background()
	client := newUSDTTestClient(t)
	order := seedCompletedUSDTOrder(t, client, 100)
	svc := &PaymentService{entClient: client}

	intent, err := client.USDTPaymentIntent.Create().
		SetOrderID(order.ID).
		SetOutTradeNo(order.OutTradeNo).
		SetProviderInstanceID("7").
		SetAddress(testUSDTWallet).
		SetNetwork("TRC20").
		SetTokenContract(testUSDTContract).
		SetAmountUsdt("13.483700").
		SetRate("7.4213").SetBaseRate("7.2050").SetPremiumPercent("3").
		SetRateSource("coingecko").
		SetRateQuotedAt(time.Now()).
		SetStatus(USDTIntentStatusMatched).
		SetExpiresAt(time.Now().Add(time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	deposit, err := client.USDTDeposit.Create().
		SetTxHash("tx-deposit-abc").
		SetAddress(testUSDTWallet).
		SetFromAddress(testUSDTContract).
		SetTokenContract(testUSDTContract).
		SetAmountUsdt("13.483700").
		SetBlockTimestamp(time.Now()).
		SetStatus(USDTDepositStatusMatched).
		SetMatchedOrderID(order.ID).
		Save(ctx)
	require.NoError(t, err)

	require.NoError(t, svc.settleUSDTOrder(ctx, intent, deposit))

	logEntry, err := client.PaymentAuditLog.Query().
		Where(
			paymentauditlog.OrderIDEQ("1"),
			paymentauditlog.ActionEQ("USDT_DEPOSIT_MATCHED"),
		).
		Only(ctx)
	require.NoError(t, err)
	require.Contains(t, logEntry.Detail, "tx-deposit-abc", "the settling transaction must be recorded")
	require.Contains(t, logEntry.Detail, "13.483700", "the USDT amount actually received must be recorded")
	require.Contains(t, logEntry.Detail, "7.4213", "the rate the order was priced at must be recorded")

	// Re-settling an already completed order must not change it.
	reloaded := client.PaymentOrder.GetX(ctx, order.ID)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
	require.InDelta(t, 100.0, reloaded.Amount, 0.001)
}
