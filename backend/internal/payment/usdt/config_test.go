package usdt

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func validConfigMap() map[string]string {
	return map[string]string{
		ConfigKeyWalletAddress: validAccountAddr,
		ConfigKeyTronAPIKey:    "tron-key",
	}
}

func TestParseConfigAppliesDefaults(t *testing.T) {
	cfg, err := ParseConfig(validConfigMap())
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}

	if cfg.WalletAddress != validAccountAddr {
		t.Fatalf("WalletAddress = %q, want %q", cfg.WalletAddress, validAccountAddr)
	}
	if cfg.Network != NetworkTRC20 {
		t.Fatalf("Network = %q, want %q", cfg.Network, NetworkTRC20)
	}
	if cfg.TokenContract != DefaultUSDTContract {
		t.Fatalf("TokenContract = %q, want %q", cfg.TokenContract, DefaultUSDTContract)
	}
	if cfg.TronAPIBase != TronMainnetAPIBase {
		t.Fatalf("TronAPIBase = %q, want %q", cfg.TronAPIBase, TronMainnetAPIBase)
	}
	if cfg.RateAPIBase != DefaultRateAPIBase {
		t.Fatalf("RateAPIBase = %q, want %q", cfg.RateAPIBase, DefaultRateAPIBase)
	}
	if want := decimal.NewFromInt(DefaultPremiumPercent); !cfg.PremiumPercent.Equal(want) {
		t.Fatalf("PremiumPercent = %s, want %s", cfg.PremiumPercent, want)
	}
	if cfg.RateMaxStaleness != DefaultRateMaxStaleness {
		t.Fatalf("RateMaxStaleness = %s, want %s", cfg.RateMaxStaleness, DefaultRateMaxStaleness)
	}
}

func TestParseConfigReadsExplicitValues(t *testing.T) {
	raw := validConfigMap()
	raw[ConfigKeyTokenContract] = validUSDTContract
	raw[ConfigKeyTronAPIBase] = TronShastaAPIBase
	raw[ConfigKeyCoinGeckoAPIKey] = "cg-key"
	raw[ConfigKeyRatePremiumPercent] = "4.5"
	raw[ConfigKeyRateMaxStalenessSec] = "600"

	cfg, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	if cfg.TokenContract != validUSDTContract {
		t.Fatalf("TokenContract = %q, want %q", cfg.TokenContract, validUSDTContract)
	}
	if cfg.TronAPIBase != TronShastaAPIBase {
		t.Fatalf("TronAPIBase = %q, want %q", cfg.TronAPIBase, TronShastaAPIBase)
	}
	if cfg.RateAPIKey != "cg-key" {
		t.Fatalf("RateAPIKey = %q, want cg-key", cfg.RateAPIKey)
	}
	if want := decimal.RequireFromString("4.5"); !cfg.PremiumPercent.Equal(want) {
		t.Fatalf("PremiumPercent = %s, want %s", cfg.PremiumPercent, want)
	}
	if cfg.RateMaxStaleness != 10*time.Minute {
		t.Fatalf("RateMaxStaleness = %s, want 10m", cfg.RateMaxStaleness)
	}
}

func TestParseConfigTrimsWhitespace(t *testing.T) {
	raw := map[string]string{
		ConfigKeyWalletAddress: "  " + validAccountAddr + "  ",
		ConfigKeyTronAPIKey:    "  tron-key\n",
	}
	cfg, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	if cfg.WalletAddress != validAccountAddr {
		t.Fatalf("WalletAddress = %q, want trimmed %q", cfg.WalletAddress, validAccountAddr)
	}
	if cfg.TronAPIKey != "tron-key" {
		t.Fatalf("TronAPIKey = %q, want trimmed", cfg.TronAPIKey)
	}
}

// Every one of these must fail when the admin clicks Save, not when the first
// customer tries to pay.
func TestParseConfigRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(map[string]string)
		wantSub string
	}{
		{"missing wallet address", func(m map[string]string) { delete(m, ConfigKeyWalletAddress) }, "wallet"},
		{"corrupted wallet address", func(m map[string]string) {
			m[ConfigKeyWalletAddress] = "TJmmqjb1DK9TTZbQXzRQ2AuA94z4gKAPFa"
		}, "checksum"},
		{"missing tron api key", func(m map[string]string) { delete(m, ConfigKeyTronAPIKey) }, "apiKey"},
		{"unsupported network", func(m map[string]string) { m[ConfigKeyNetwork] = "ERC20" }, "network"},
		{"invalid token contract", func(m map[string]string) { m[ConfigKeyTokenContract] = "0xdeadbeef" }, "tokenContract"},
		{"non-trongrid api base", func(m map[string]string) { m[ConfigKeyTronAPIBase] = "https://evil.example.com" }, "apiBase"},
		{"negative premium", func(m map[string]string) { m[ConfigKeyRatePremiumPercent] = "-1" }, "premium"},
		{"premium over cap", func(m map[string]string) { m[ConfigKeyRatePremiumPercent] = "21" }, "premium"},
		{"non-numeric premium", func(m map[string]string) { m[ConfigKeyRatePremiumPercent] = "3%" }, "premium"},
		{"zero staleness", func(m map[string]string) { m[ConfigKeyRateMaxStalenessSec] = "0" }, "staleness"},
		{"negative staleness", func(m map[string]string) { m[ConfigKeyRateMaxStalenessSec] = "-60" }, "staleness"},
		{"non-numeric staleness", func(m map[string]string) { m[ConfigKeyRateMaxStalenessSec] = "abc" }, "staleness"},
		{"non-https rate api base", func(m map[string]string) { m[ConfigKeyCoinGeckoAPIBase] = "http://api.coingecko.com/api/v3" }, "https"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := validConfigMap()
			tc.mutate(raw)

			_, err := ParseConfig(raw)
			if err == nil {
				t.Fatalf("ParseConfig(%v) = nil error, want error", raw)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.wantSub)) {
				t.Fatalf("ParseConfig() error = %q, want it to mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestParseConfigRejectsNilMap(t *testing.T) {
	if _, err := ParseConfig(nil); err == nil {
		t.Fatal("ParseConfig(nil) = nil error, want error")
	}
}

func TestConfigRateOptionsCarriesRateSettings(t *testing.T) {
	raw := validConfigMap()
	raw[ConfigKeyCoinGeckoAPIKey] = "cg-key"
	raw[ConfigKeyRatePremiumPercent] = "2.5"
	raw[ConfigKeyRateMaxStalenessSec] = "900"

	cfg, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	opts := cfg.RateOptions()
	if opts.APIKey != "cg-key" {
		t.Fatalf("RateOptions().APIKey = %q, want cg-key", opts.APIKey)
	}
	if !opts.PremiumPercent.Equal(decimal.RequireFromString("2.5")) {
		t.Fatalf("RateOptions().PremiumPercent = %s, want 2.5", opts.PremiumPercent)
	}
	if opts.MaxStaleness != 15*time.Minute {
		t.Fatalf("RateOptions().MaxStaleness = %s, want 15m", opts.MaxStaleness)
	}
}

func TestConfigTronOptionsCarriesChainSettings(t *testing.T) {
	cfg, err := ParseConfig(validConfigMap())
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	opts := cfg.TronOptions()
	if opts.APIKey != "tron-key" {
		t.Fatalf("TronOptions().APIKey = %q, want tron-key", opts.APIKey)
	}
	if opts.TokenContract != DefaultUSDTContract {
		t.Fatalf("TronOptions().TokenContract = %q, want %q", opts.TokenContract, DefaultUSDTContract)
	}
	if _, err := NewTronClient(opts); err != nil {
		t.Fatalf("NewTronClient(cfg.TronOptions()) error = %v", err)
	}
}
