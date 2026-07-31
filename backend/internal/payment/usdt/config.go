package usdt

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Config keys as stored in the payment provider instance config. These are the
// exact field names the admin UI renders, and must stay in sync with
// frontend/src/components/payment/providerConfig.ts (PROVIDER_CONFIG_FIELDS.usdt).
const (
	ConfigKeyWalletAddress       = "walletAddress"
	ConfigKeyNetwork             = "network"
	ConfigKeyTokenContract       = "tokenContract"
	ConfigKeyTronAPIBase         = "tronApiBase"
	ConfigKeyTronAPIKey          = "tronApiKey"
	ConfigKeyCoinGeckoAPIBase    = "coingeckoApiBase"
	ConfigKeyCoinGeckoAPIKey     = "coingeckoApiKey"
	ConfigKeyRatePremiumPercent  = "ratePremiumPercent"
	ConfigKeyRateMaxStalenessSec = "rateMaxStalenessSec"
)

// NetworkTRC20 is the only supported chain today. The field exists so the
// stored data is explicit about which chain an order was quoted for, which
// matters if ERC20/BEP20 are ever added alongside it.
const NetworkTRC20 = "TRC20"

// DefaultPremiumPercent is the markup applied over the market rate.
//
// CoinGecko's tether/cny tracks the official USD/CNY reference rate, which runs
// roughly 2–4% below what USDT actually costs on OTC desks. Without a markup
// every order would be underpriced against real acquisition cost, so this
// defaults to a non-zero value and must be calibrated against live OTC quotes
// before going live.
const DefaultPremiumPercent = 3

// Config is the parsed, validated USDT channel configuration.
type Config struct {
	WalletAddress    string
	Network          string
	TokenContract    string
	TronAPIBase      string
	TronAPIKey       string
	RateAPIBase      string
	RateAPIKey       string
	PremiumPercent   decimal.Decimal
	RateMaxStaleness time.Duration
}

// ParseConfig validates a provider instance config into a usable Config.
//
// This runs at channel-save time (via the provider constructor), so a mistyped
// receiving address or a missing API key surfaces in the admin dialog rather
// than as a failed checkout.
func ParseConfig(raw map[string]string) (Config, error) {
	if raw == nil {
		return Config{}, fmt.Errorf("usdt config is missing")
	}

	cfg := Config{}
	var err error

	if cfg.WalletAddress, err = parseWalletAddress(raw[ConfigKeyWalletAddress]); err != nil {
		return Config{}, err
	}
	if cfg.Network, err = parseNetwork(raw[ConfigKeyNetwork]); err != nil {
		return Config{}, err
	}
	if cfg.TokenContract, err = parseTokenContract(raw[ConfigKeyTokenContract]); err != nil {
		return Config{}, err
	}
	if cfg.TronAPIBase, err = normalizeTronAPIBase(raw[ConfigKeyTronAPIBase]); err != nil {
		return Config{}, fmt.Errorf("usdt config %s: %w", ConfigKeyTronAPIBase, err)
	}
	if cfg.TronAPIKey = strings.TrimSpace(raw[ConfigKeyTronAPIKey]); cfg.TronAPIKey == "" {
		return Config{}, fmt.Errorf("usdt config %s is required", ConfigKeyTronAPIKey)
	}
	if cfg.RateAPIBase, err = parseRateAPIBase(raw[ConfigKeyCoinGeckoAPIBase]); err != nil {
		return Config{}, err
	}
	cfg.RateAPIKey = strings.TrimSpace(raw[ConfigKeyCoinGeckoAPIKey])
	if cfg.PremiumPercent, err = parsePremiumPercent(raw[ConfigKeyRatePremiumPercent]); err != nil {
		return Config{}, err
	}
	if cfg.RateMaxStaleness, err = parseRateMaxStaleness(raw[ConfigKeyRateMaxStalenessSec]); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// RateOptions projects the rate-related settings for the shared RateProvider.
func (c Config) RateOptions() RateOptions {
	return RateOptions{
		APIBase:        c.RateAPIBase,
		APIKey:         c.RateAPIKey,
		PremiumPercent: c.PremiumPercent,
		MaxStaleness:   c.RateMaxStaleness,
	}
}

// TronOptions projects the chain-related settings for a TronClient.
func (c Config) TronOptions() TronOptions {
	return TronOptions{
		APIBase:       c.TronAPIBase,
		APIKey:        c.TronAPIKey,
		TokenContract: c.TokenContract,
	}
}

func parseWalletAddress(raw string) (string, error) {
	address := strings.TrimSpace(raw)
	if address == "" {
		return "", fmt.Errorf("usdt config %s (receiving wallet address) is required", ConfigKeyWalletAddress)
	}
	normalized, err := NormalizeTronAddress(address)
	if err != nil {
		return "", fmt.Errorf("usdt config %s: %w", ConfigKeyWalletAddress, err)
	}
	return normalized, nil
}

func parseNetwork(raw string) (string, error) {
	network := strings.ToUpper(strings.TrimSpace(raw))
	if network == "" {
		return NetworkTRC20, nil
	}
	if network != NetworkTRC20 {
		return "", fmt.Errorf("usdt config %s must be %s, got %s", ConfigKeyNetwork, NetworkTRC20, network)
	}
	return network, nil
}

func parseTokenContract(raw string) (string, error) {
	contract := strings.TrimSpace(raw)
	if contract == "" {
		return DefaultUSDTContract, nil
	}
	if err := ValidateTronAddress(contract); err != nil {
		return "", fmt.Errorf("usdt config %s: %w", ConfigKeyTokenContract, err)
	}
	return contract, nil
}

func parseRateAPIBase(raw string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return DefaultRateAPIBase, nil
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("usdt config %s must be a valid URL", ConfigKeyCoinGeckoAPIBase)
	}
	if isLoopbackHost(parsed.Host) {
		return base, nil
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("usdt config %s must use https", ConfigKeyCoinGeckoAPIBase)
	}
	return base, nil
}

func parsePremiumPercent(raw string) (decimal.Decimal, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return decimal.NewFromInt(DefaultPremiumPercent), nil
	}
	premium, err := decimal.NewFromString(value)
	if err != nil {
		return decimal.Zero, fmt.Errorf("usdt config %s (rate premium) must be a number, got %q", ConfigKeyRatePremiumPercent, value)
	}
	if premium.IsNegative() || premium.GreaterThan(decimal.NewFromInt(MaxPremiumPercent)) {
		return decimal.Zero, fmt.Errorf("usdt config %s (rate premium) must be between 0 and %d, got %s",
			ConfigKeyRatePremiumPercent, MaxPremiumPercent, premium)
	}
	return premium, nil
}

func parseRateMaxStaleness(raw string) (time.Duration, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return DefaultRateMaxStaleness, nil
	}
	seconds, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("usdt config %s (rate staleness) must be a whole number of seconds, got %q",
			ConfigKeyRateMaxStalenessSec, value)
	}
	if seconds <= 0 {
		return 0, fmt.Errorf("usdt config %s (rate staleness) must be positive, got %d",
			ConfigKeyRateMaxStalenessSec, seconds)
	}
	return time.Duration(seconds) * time.Second, nil
}
