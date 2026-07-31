// Package usdt provides the building blocks for USDT (TRC20) payments:
// address validation, CNY↔USDT conversion, exchange-rate quoting, and the
// TronGrid client used to reconcile on-chain deposits.
package usdt

import (
	"crypto/sha256"
	"fmt"
	"math/big"
	"strings"
)

// TRON base58check address layout: 0x41 version byte + 20-byte account +
// 4-byte checksum, which always encodes to 34 base58 characters starting "T".
const (
	tronAddressLen       = 34
	tronDecodedLen       = 25
	tronPayloadLen       = 21
	tronVersionByte byte = 0x41
	tronChecksumLen      = 4
)

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var base58Index = buildBase58Index()

func buildBase58Index() map[byte]int64 {
	index := make(map[byte]int64, len(base58Alphabet))
	for i := 0; i < len(base58Alphabet); i++ {
		index[base58Alphabet[i]] = int64(i)
	}
	return index
}

// NormalizeTronAddress trims and validates a TRC20 address, returning the
// canonical form callers should persist.
func NormalizeTronAddress(raw string) (string, error) {
	address := strings.TrimSpace(raw)
	if err := ValidateTronAddress(address); err != nil {
		return "", err
	}
	return address, nil
}

// ValidateTronAddress reports whether raw is a well-formed TRC20 address.
//
// The base58check checksum is verified in full rather than only checking the
// "T" prefix and length: a single mistyped character in a receiving address
// sends funds somewhere unrecoverable, and the checksum is the only thing that
// catches it before money moves.
func ValidateTronAddress(raw string) error {
	address := strings.TrimSpace(raw)
	if address == "" {
		return fmt.Errorf("tron address is required")
	}
	if len(address) != tronAddressLen {
		return fmt.Errorf("tron address must be %d characters, got %d", tronAddressLen, len(address))
	}
	if address[0] != 'T' {
		return fmt.Errorf("tron address must start with T")
	}

	decoded, err := decodeBase58(address)
	if err != nil {
		return err
	}
	if len(decoded) != tronDecodedLen {
		return fmt.Errorf("tron address has an invalid payload length")
	}
	if decoded[0] != tronVersionByte {
		return fmt.Errorf("tron address has an invalid version byte")
	}

	payload := decoded[:tronPayloadLen]
	want := decoded[tronPayloadLen:]
	if got := base58Checksum(payload); !bytesEqual(got, want) {
		return fmt.Errorf("tron address checksum mismatch")
	}
	return nil
}

func base58Checksum(payload []byte) []byte {
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	return second[:tronChecksumLen]
}

// decodeBase58 decodes a base58 string into its byte payload, preserving
// leading-zero bytes encoded as '1' characters.
func decodeBase58(encoded string) ([]byte, error) {
	value := big.NewInt(0)
	radix := big.NewInt(int64(len(base58Alphabet)))
	for i := 0; i < len(encoded); i++ {
		digit, ok := base58Index[encoded[i]]
		if !ok {
			return nil, fmt.Errorf("tron address contains a non-base58 character %q", encoded[i])
		}
		value.Mul(value, radix)
		value.Add(value, big.NewInt(digit))
	}

	decoded := value.Bytes()
	leadingZeros := 0
	for leadingZeros < len(encoded) && encoded[leadingZeros] == base58Alphabet[0] {
		leadingZeros++
	}
	return append(make([]byte, leadingZeros), decoded...), nil
}

func bytesEqual(a, b []byte) bool {
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
