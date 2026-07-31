package usdt

import (
	"testing"

	"github.com/shopspring/decimal"
)

func dec(t *testing.T, s string) decimal.Decimal {
	t.Helper()
	d, err := decimal.NewFromString(s)
	if err != nil {
		t.Fatalf("decimal.NewFromString(%q) error = %v", s, err)
	}
	return d
}

func TestBaseAmountRoundsUp(t *testing.T) {
	tests := []struct {
		name   string
		payCNY string
		rate   string
		want   string
	}{
		// 100 / 7.42 = 13.47708... → rounds UP to 13.48, never 13.47.
		{"rounds up a repeating quotient", "100", "7.42", "13.48"},
		{"exact quotient stays exact", "74.2", "7.42", "10"},
		// 0.001 above an exact hundredth still bumps the whole cent.
		{"tiny remainder still rounds up", "74.21", "7.42", "10.01"},
		{"small amount", "1", "7.42", "0.14"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BaseAmount(dec(t, tc.payCNY), dec(t, tc.rate))
			if err != nil {
				t.Fatalf("BaseAmount() error = %v", err)
			}
			if !got.Equal(dec(t, tc.want)) {
				t.Fatalf("BaseAmount() = %s, want %s", got, tc.want)
			}
		})
	}
}

// Rounding up must hold for every input, not just the hand-picked ones:
// the merchant may never be undercharged by the conversion.
func TestBaseAmountNeverUnderchargesTheMerchant(t *testing.T) {
	rate := dec(t, "7.4213")
	for _, payCNY := range []string{"0.01", "1", "9.99", "100", "137.77", "5000", "99999.99"} {
		pay := dec(t, payCNY)
		got, err := BaseAmount(pay, rate)
		if err != nil {
			t.Fatalf("BaseAmount(%s) error = %v", payCNY, err)
		}
		if got.Mul(rate).LessThan(pay) {
			t.Fatalf("BaseAmount(%s) = %s converts back to %s, less than %s",
				payCNY, got, got.Mul(rate), pay)
		}
		if got.Exponent() < -baseAmountDecimals {
			t.Fatalf("BaseAmount(%s) = %s has more than %d decimals", payCNY, got, baseAmountDecimals)
		}
	}
}

func TestBaseAmountRejectsBadInput(t *testing.T) {
	tests := []struct {
		name   string
		payCNY string
		rate   string
	}{
		{"zero rate", "100", "0"},
		{"negative rate", "100", "-7.42"},
		{"zero pay amount", "0", "7.42"},
		{"negative pay amount", "-1", "7.42"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := BaseAmount(dec(t, tc.payCNY), dec(t, tc.rate)); err == nil {
				t.Fatalf("BaseAmount(%s, %s) = nil error, want error", tc.payCNY, tc.rate)
			}
		})
	}
}

func TestSuffixedAmountAppendsTwoDigitTag(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		suffix int
		want   string
	}{
		{"mid-range suffix", "13.48", 37, "13.4837"},
		{"zero suffix keeps value", "13.48", 0, "13.48"},
		{"max suffix", "13.48", 99, "13.4899"},
		{"single digit suffix is a hundredth of a cent", "13.48", 7, "13.4807"},
		{"whole number base", "10", 42, "10.0042"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SuffixedAmount(dec(t, tc.base), tc.suffix)
			if err != nil {
				t.Fatalf("SuffixedAmount() error = %v", err)
			}
			if !got.Equal(dec(t, tc.want)) {
				t.Fatalf("SuffixedAmount() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSuffixedAmountRejectsOutOfRangeSuffix(t *testing.T) {
	base := dec(t, "13.48")
	for _, suffix := range []int{-1, 100, 1000} {
		if _, err := SuffixedAmount(base, suffix); err == nil {
			t.Fatalf("SuffixedAmount(_, %d) = nil error, want error", suffix)
		}
	}
}

func TestFormatAmountAlwaysShowsFourDecimals(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"13.4837", "13.4837"},
		{"13.48", "13.4800"},
		{"10", "10.0000"},
		{"0.0099", "0.0099"},
	}
	for _, tc := range tests {
		if got := FormatAmount(dec(t, tc.in)); got != tc.want {
			t.Fatalf("FormatAmount(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Reconciliation compares canonical strings, so equal numbers written
// differently must canonicalise identically — otherwise a real deposit would
// fail to match its own order.
func TestCanonicalAmountIsStableAcrossEquivalentWritings(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"14.2937", "14.293700"},
		{"14.293700", "14.293700"},
		{"14.29370000", "14.293700"},
		{"10", "10.000000"},
		{"0.000001", "0.000001"},
	}
	for _, tc := range tests {
		if got := CanonicalAmount(dec(t, tc.in)); got != tc.want {
			t.Fatalf("CanonicalAmount(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A payer who sends a different number must not be rounded into someone
// else's order.
func TestCanonicalAmountKeepsDistinctAmountsDistinct(t *testing.T) {
	if CanonicalAmount(dec(t, "14.2937")) == CanonicalAmount(dec(t, "14.293701")) {
		t.Fatal("CanonicalAmount collapsed two different on-chain amounts into one")
	}
}

// Values carrying more precision than USDT can hold must never be trimmed to
// fit — rounding them would turn an amount we never asked for into an exact
// match against a real order.
func TestCanonicalAmountNeverRoundsExcessPrecisionIntoAMatch(t *testing.T) {
	for _, excess := range []string{"14.2937001", "14.293700001", "14.29370049"} {
		if got := CanonicalAmount(dec(t, excess)); got == "14.293700" {
			t.Fatalf("CanonicalAmount(%s) = %q, want it to stay distinct from 14.293700", excess, got)
		}
	}
}

func TestSuffixCandidatesCoverEveryTagExactlyOnce(t *testing.T) {
	got := SuffixCandidates()
	if len(got) != SuffixCount {
		t.Fatalf("len(SuffixCandidates()) = %d, want %d", len(got), SuffixCount)
	}
	seen := make(map[int]bool, SuffixCount)
	for _, suffix := range got {
		if suffix < 0 || suffix > MaxSuffix {
			t.Fatalf("SuffixCandidates() returned out-of-range tag %d", suffix)
		}
		if seen[suffix] {
			t.Fatalf("SuffixCandidates() returned duplicate tag %d", suffix)
		}
		seen[suffix] = true
	}
}

// The tag exists to disambiguate concurrent orders, so the order it is handed
// out in must not be predictable enough for two callers to collide repeatedly.
func TestSuffixCandidatesAreShuffled(t *testing.T) {
	first := SuffixCandidates()
	for range 20 {
		if !equalIntSlice(first, SuffixCandidates()) {
			return
		}
	}
	t.Fatal("SuffixCandidates() returned the same order 21 times, want shuffled output")
}

func equalIntSlice(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
