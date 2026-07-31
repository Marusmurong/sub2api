package usdt

import "testing"

// Real mainnet addresses. TR7NH... is the official USDT-TRC20 contract.
const (
	validUSDTContract = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	validAccountAddr  = "TJmmqjb1DK9TTZbQXzRQ2AuA94z4gKAPFh"
)

func TestValidateTronAddressAcceptsRealAddresses(t *testing.T) {
	for _, addr := range []string{validUSDTContract, validAccountAddr} {
		if err := ValidateTronAddress(addr); err != nil {
			t.Fatalf("ValidateTronAddress(%q) = %v, want nil", addr, err)
		}
	}
}

func TestValidateTronAddressTrimsSurroundingSpace(t *testing.T) {
	if err := ValidateTronAddress("  " + validAccountAddr + "\n"); err != nil {
		t.Fatalf("ValidateTronAddress with padding = %v, want nil", err)
	}
}

func TestValidateTronAddressRejectsBadInput(t *testing.T) {
	// A single flipped character breaks the base58check checksum. Catching this
	// is the whole point: a mistyped receiving address means funds are gone.
	corrupted := "TJmmqjb1DK9TTZbQXzRQ2AuA94z4gKAPFa"

	tests := []struct {
		name string
		addr string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"corrupted checksum", corrupted},
		{"wrong prefix", "AJmmqjb1DK9TTZbQXzRQ2AuA94z4gKAPFh"},
		{"too short", "TJmmqjb1DK9TTZbQXzRQ2AuA94z4gKAP"},
		{"too long", validAccountAddr + "xx"},
		{"non base58 char (0)", "TJmmqjb1DK9TTZbQXzRQ2AuA94z4gKAPF0"},
		{"non base58 char (l)", "TJmmqjb1DK9TTZbQXzRQ2AuA94z4gKAPFl"},
		{"ethereum address", "0x742d35Cc6634C0532925a3b844Bc454e4438f44e"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateTronAddress(tc.addr); err == nil {
				t.Fatalf("ValidateTronAddress(%q) = nil, want error", tc.addr)
			}
		})
	}
}

func TestNormalizeTronAddressReturnsTrimmedValue(t *testing.T) {
	got, err := NormalizeTronAddress(" " + validAccountAddr + " ")
	if err != nil {
		t.Fatalf("NormalizeTronAddress() error = %v", err)
	}
	if got != validAccountAddr {
		t.Fatalf("NormalizeTronAddress() = %q, want %q", got, validAccountAddr)
	}
}

func TestNormalizeTronAddressRejectsInvalid(t *testing.T) {
	if _, err := NormalizeTronAddress("not-an-address"); err == nil {
		t.Fatal("NormalizeTronAddress() = nil error, want error")
	}
}
