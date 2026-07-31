package provider

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/usdt"
)

// USDTManualRefundID marks a refund that has to be paid out by hand. On-chain
// transfers cannot be reversed or initiated by an API we control, so the refund
// parks in REFUND_PENDING until an operator records the payout transaction.
const USDTManualRefundID = "manual"

// USDT is the USDT (TRC20) payment provider.
//
// Unlike hosted gateways it has no upstream to create or query a payment
// against: the customer transfers to our own wallet and settlement is decided
// by on-chain reconciliation (see service.USDTReconcileService). This type
// therefore does the minimum the Provider contract requires — it validates
// configuration, hands back the receiving address for the QR code, and refuses
// to ever claim an order is paid.
type USDT struct {
	instanceID string
	config     usdt.Config
}

// NewUSDT validates the channel configuration and builds the provider.
//
// All validation happens here so a mistyped receiving address or missing API
// key is rejected when the admin saves the channel, not when a customer is
// standing in front of a checkout page.
func NewUSDT(instanceID string, config map[string]string) (*USDT, error) {
	parsed, err := usdt.ParseConfig(config)
	if err != nil {
		return nil, err
	}
	// Build the chain client once at construction purely to surface a bad
	// TronGrid setting as a save-time error.
	if _, err := usdt.NewTronClient(parsed.TronOptions()); err != nil {
		return nil, err
	}
	return &USDT{instanceID: instanceID, config: parsed}, nil
}

func (u *USDT) Name() string        { return "USDT (TRC20)" }
func (u *USDT) ProviderKey() string { return payment.TypeUSDT }
func (u *USDT) SupportedTypes() []payment.PaymentType {
	return []payment.PaymentType{payment.TypeUSDT}
}

// Config exposes the parsed configuration for the reconciliation service.
func (u *USDT) Config() usdt.Config { return u.config }

// CreatePayment returns the receiving address for the payment page to render.
//
// The QR payload is the bare address rather than a tron: payment URI: wallet
// support for those URIs is inconsistent, while every wallet can scan an
// address. The exact USDT figure the customer must send is allocated separately
// on the payment intent, because picking it needs a database round-trip to
// guarantee uniqueness.
//
// Currency stays CNY: the order is denominated in CNY end-to-end, which is what
// keeps revenue reporting, daily limits and the cashback plugin working without
// any USDT-specific handling.
func (u *USDT) CreatePayment(_ context.Context, _ payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	return &payment.CreatePaymentResponse{
		QRCode:     u.config.WalletAddress,
		Currency:   payment.DefaultPaymentCurrency,
		ResultType: payment.CreatePaymentResultOrderCreated,
	}, nil
}

// QueryOrder always reports pending.
//
// Settlement is owned by on-chain reconciliation, which matches a confirmed
// deposit against the intent's exact amount. Returning "paid" from here would
// let the generic cancel and expiry paths credit an order that no deposit was
// ever matched to.
func (u *USDT) QueryOrder(_ context.Context, tradeNo string) (*payment.QueryOrderResponse, error) {
	return &payment.QueryOrderResponse{
		TradeNo: tradeNo,
		Status:  payment.ProviderStatusPending,
	}, nil
}

// VerifyNotification is inert: there is no USDT webhook, and no route is
// registered for one. Returning (nil, nil) means even a forged callback cannot
// settle an order.
func (u *USDT) VerifyNotification(_ context.Context, _ string, _ map[string]string) (*payment.PaymentNotification, error) {
	return nil, nil
}

// Refund parks the order in REFUND_PENDING for an operator to pay out manually.
//
// The caller has already deducted the customer's balance by this point, so the
// money leaves the account before it leaves the wallet — the correct order for
// an irreversible payout.
func (u *USDT) Refund(_ context.Context, _ payment.RefundRequest) (*payment.RefundResponse, error) {
	return &payment.RefundResponse{
		RefundID: USDTManualRefundID,
		Status:   payment.ProviderStatusPending,
	}, nil
}

// QueryRefund stays pending until an operator records the payout transaction.
// We deliberately do not scan the chain for outgoing transfers and guess which
// one corresponds to which refund; only a recorded txid counts as settled.
func (u *USDT) QueryRefund(_ context.Context, req payment.RefundQueryRequest) (*payment.RefundResponse, error) {
	refundID := req.RefundID
	if refundID == "" {
		refundID = USDTManualRefundID
	}
	return &payment.RefundResponse{RefundID: refundID, Status: payment.ProviderStatusPending}, nil
}

// TronClient builds a chain client from this channel's configuration.
func (u *USDT) TronClient() (*usdt.TronClient, error) {
	client, err := usdt.NewTronClient(u.config.TronOptions())
	if err != nil {
		return nil, fmt.Errorf("build usdt chain client for instance %s: %w", u.instanceID, err)
	}
	return client, nil
}

var (
	_ payment.Provider            = (*USDT)(nil)
	_ payment.RefundQueryProvider = (*USDT)(nil)
)
