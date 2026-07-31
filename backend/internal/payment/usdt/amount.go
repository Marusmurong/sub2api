package usdt

import (
	"fmt"
	"math/rand/v2"

	"github.com/shopspring/decimal"
)

const (
	// baseAmountDecimals is the precision of the converted amount before the
	// uniqueness tag is appended.
	baseAmountDecimals = 2
	// AmountDecimals is the precision the payer must transfer exactly. The last
	// two digits carry the uniqueness tag used to reconcile the deposit.
	AmountDecimals = 4
	// ChainDecimals is USDT-TRC20's own precision. Amounts are persisted and
	// compared at this scale so reconciliation is exact rather than rounded.
	ChainDecimals = 6

	// MaxSuffix and SuffixCount bound the uniqueness tag (00–99).
	MaxSuffix   = 99
	SuffixCount = MaxSuffix + 1
)

// suffixUnit is the decimal weight of one uniqueness tag step (0.0001 USDT).
var suffixUnit = decimal.New(1, -AmountDecimals)

// BaseAmount converts a CNY payable amount into USDT at the given rate,
// rounded UP to two decimals.
//
// Rounding up is deliberate and directional: it can only ever collect slightly
// more than the invoice, never less. At a ~7.4 rate the worst-case overcharge
// is under one fen, which is invisible to the payer but removes any chance of
// a rounding shortfall accumulating against us.
func BaseAmount(payCNY, rateCNYPerUSDT decimal.Decimal) (decimal.Decimal, error) {
	if payCNY.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("usdt conversion: pay amount must be positive, got %s", payCNY)
	}
	if rateCNYPerUSDT.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("usdt conversion: rate must be positive, got %s", rateCNYPerUSDT)
	}
	return payCNY.DivRound(rateCNYPerUSDT, baseAmountDecimals+1).RoundCeil(baseAmountDecimals), nil
}

// SuffixedAmount appends the two-digit uniqueness tag to a base amount,
// producing the exact figure the payer must transfer.
//
// The tag only ever adds value (at most 0.0099 USDT), so it never reduces what
// we collect.
func SuffixedAmount(base decimal.Decimal, suffix int) (decimal.Decimal, error) {
	if suffix < 0 || suffix > MaxSuffix {
		return decimal.Zero, fmt.Errorf("usdt suffix must be between 0 and %d, got %d", MaxSuffix, suffix)
	}
	return base.Add(suffixUnit.Mul(decimal.NewFromInt(int64(suffix)))), nil
}

// FormatAmount renders an amount with exactly AmountDecimals decimal places.
// The payer has to match this string exactly, so trailing zeros are kept.
func FormatAmount(amount decimal.Decimal) string {
	return amount.StringFixed(AmountDecimals)
}

// CanonicalAmount renders an amount at USDT's native on-chain precision.
//
// This is the form persisted and compared during reconciliation. Both sides —
// the amount we asked for and the amount that arrived — must be canonicalised
// identically, otherwise "exact match" degenerates into string trivia:
// 14.2937 and 14.293700 are the same number but different strings.
//
// Crucially this never rounds. A value carrying more precision than USDT can
// hold on-chain is returned verbatim rather than trimmed to fit, so it compares
// unequal to every intent and lands in the operator queue. Rounding here would
// quietly turn "an amount we did not ask for" into "an exact match", which is
// the one failure this whole scheme exists to prevent.
func CanonicalAmount(amount decimal.Decimal) string {
	// Trailing zeros are not extra precision — 14.29370000 is the same money as
	// 14.2937 — so the test is whether truncating actually changes the value,
	// not how the decimal happens to be scaled.
	if !amount.Truncate(ChainDecimals).Equal(amount) {
		return amount.String()
	}
	return amount.StringFixed(ChainDecimals)
}

// SuffixCandidates returns every uniqueness tag in shuffled order.
//
// Callers walk the slice until one tag survives the database's partial unique
// index. Shuffling spreads concurrent allocations across the range instead of
// making every request contend on tag 0 first.
func SuffixCandidates() []int {
	candidates := make([]int, SuffixCount)
	for i := range candidates {
		candidates[i] = i
	}
	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})
	return candidates
}
