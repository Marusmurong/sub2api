package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/usdt"
)

const (
	testUSDTWallet   = "TJmmqjb1DK9TTZbQXzRQ2AuA94z4gKAPFh"
	testUSDTContract = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
)

func validUSDTConfig() map[string]string {
	return map[string]string{
		usdt.ConfigKeyWalletAddress: testUSDTWallet,
		usdt.ConfigKeyTronAPIKey:    "tron-key",
	}
}

func newTestUSDT(t *testing.T) *USDT {
	t.Helper()
	provider, err := NewUSDT("1", validUSDTConfig())
	if err != nil {
		t.Fatalf("NewUSDT() error = %v", err)
	}
	return provider
}

func TestNewUSDTValidatesConfigAtConstruction(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]string)
	}{
		{"missing wallet", func(m map[string]string) { delete(m, usdt.ConfigKeyWalletAddress) }},
		{"corrupted wallet", func(m map[string]string) {
			m[usdt.ConfigKeyWalletAddress] = "TJmmqjb1DK9TTZbQXzRQ2AuA94z4gKAPFa"
		}},
		{"missing tron key", func(m map[string]string) { delete(m, usdt.ConfigKeyTronAPIKey) }},
		{"bad premium", func(m map[string]string) { m[usdt.ConfigKeyRatePremiumPercent] = "999" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validUSDTConfig()
			tc.mutate(cfg)
			if _, err := NewUSDT("1", cfg); err == nil {
				t.Fatal("NewUSDT() = nil error, want error so the admin dialog rejects the save")
			}
		})
	}
}

func TestUSDTIdentity(t *testing.T) {
	provider := newTestUSDT(t)

	if provider.ProviderKey() != payment.TypeUSDT {
		t.Fatalf("ProviderKey() = %q, want %q", provider.ProviderKey(), payment.TypeUSDT)
	}
	types := provider.SupportedTypes()
	if len(types) != 1 || types[0] != payment.TypeUSDT {
		t.Fatalf("SupportedTypes() = %v, want [%s]", types, payment.TypeUSDT)
	}
	if strings.TrimSpace(provider.Name()) == "" {
		t.Fatal("Name() is empty")
	}
}

func TestUSDTCreatePaymentReturnsReceivingAddressAsQR(t *testing.T) {
	provider := newTestUSDT(t)

	resp, err := provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		OrderID:     "sub2_20260731abcdefgh",
		Amount:      "100.00",
		PaymentType: payment.TypeUSDT,
	})
	if err != nil {
		t.Fatalf("CreatePayment() error = %v", err)
	}
	// Plain address, not a tron: URI — wallet support for payment URIs is
	// inconsistent, and every wallet can scan a bare address.
	if resp.QRCode != testUSDTWallet {
		t.Fatalf("QRCode = %q, want the receiving address %q", resp.QRCode, testUSDTWallet)
	}
	if resp.PayURL != "" {
		t.Fatalf("PayURL = %q, want empty (there is no hosted checkout)", resp.PayURL)
	}
	// The order stays priced in CNY; the USDT figure lives on the payment
	// intent. This is what keeps revenue stats, daily limits and cashback
	// working unchanged.
	if resp.Currency != payment.DefaultPaymentCurrency {
		t.Fatalf("Currency = %q, want %q", resp.Currency, payment.DefaultPaymentCurrency)
	}
	if resp.ResultType != payment.CreatePaymentResultOrderCreated {
		t.Fatalf("ResultType = %q, want %q", resp.ResultType, payment.CreatePaymentResultOrderCreated)
	}
}

// Settlement is decided by on-chain reconciliation, never by this call. Saying
// "paid" here would let the generic cancel/expiry paths credit an order that no
// deposit was ever matched to.
func TestUSDTQueryOrderNeverReportsPaid(t *testing.T) {
	provider := newTestUSDT(t)

	resp, err := provider.QueryOrder(context.Background(), "sub2_20260731abcdefgh")
	if err != nil {
		t.Fatalf("QueryOrder() error = %v", err)
	}
	if resp.Status != payment.ProviderStatusPending {
		t.Fatalf("QueryOrder().Status = %q, want %q", resp.Status, payment.ProviderStatusPending)
	}
}

func TestUSDTVerifyNotificationIsInert(t *testing.T) {
	provider := newTestUSDT(t)

	notification, err := provider.VerifyNotification(context.Background(), `{"anything":"goes"}`, map[string]string{})
	if err != nil {
		t.Fatalf("VerifyNotification() error = %v", err)
	}
	// No webhook route exists for USDT; an inert parser means a forged callback
	// can never settle an order even if one were somehow wired up.
	if notification != nil {
		t.Fatalf("VerifyNotification() = %+v, want nil", notification)
	}
}

// On-chain transfers are irreversible, so a refund is an operator action.
// Reporting "pending" parks the order in REFUND_PENDING until a human records
// the payout transaction.
func TestUSDTRefundIsManual(t *testing.T) {
	provider := newTestUSDT(t)

	resp, err := provider.Refund(context.Background(), payment.RefundRequest{
		TradeNo: "abc123",
		OrderID: "sub2_20260731abcdefgh",
		Amount:  "100.00",
	})
	if err != nil {
		t.Fatalf("Refund() error = %v", err)
	}
	if resp.Status != payment.ProviderStatusPending {
		t.Fatalf("Refund().Status = %q, want %q", resp.Status, payment.ProviderStatusPending)
	}
	if resp.RefundID != USDTManualRefundID {
		t.Fatalf("Refund().RefundID = %q, want %q", resp.RefundID, USDTManualRefundID)
	}
}

func TestUSDTQueryRefundStaysPendingUntilOperatorRecordsPayout(t *testing.T) {
	provider := newTestUSDT(t)

	resp, err := provider.QueryRefund(context.Background(), payment.RefundQueryRequest{
		RefundID: USDTManualRefundID,
		OrderID:  "sub2_20260731abcdefgh",
	})
	if err != nil {
		t.Fatalf("QueryRefund() error = %v", err)
	}
	if resp.Status != payment.ProviderStatusPending {
		t.Fatalf("QueryRefund().Status = %q, want %q", resp.Status, payment.ProviderStatusPending)
	}
}

func TestUSDTExposesParsedConfig(t *testing.T) {
	provider := newTestUSDT(t)

	cfg := provider.Config()
	if cfg.WalletAddress != testUSDTWallet {
		t.Fatalf("Config().WalletAddress = %q, want %q", cfg.WalletAddress, testUSDTWallet)
	}
	if cfg.TokenContract != testUSDTContract {
		t.Fatalf("Config().TokenContract = %q, want default USDT contract", cfg.TokenContract)
	}
}

func TestCreateProviderBuildsUSDT(t *testing.T) {
	provider, err := CreateProvider(payment.TypeUSDT, "7", validUSDTConfig())
	if err != nil {
		t.Fatalf("CreateProvider(usdt) error = %v", err)
	}
	if provider.ProviderKey() != payment.TypeUSDT {
		t.Fatalf("CreateProvider(usdt).ProviderKey() = %q, want %q", provider.ProviderKey(), payment.TypeUSDT)
	}
}

func TestUSDTSatisfiesProviderInterfaces(t *testing.T) {
	var _ payment.Provider = (*USDT)(nil)
	var _ payment.RefundQueryProvider = (*USDT)(nil)
}
