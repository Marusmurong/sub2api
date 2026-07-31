package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The partial unique index is the only thing preventing two pending USDT orders
// on one address from expecting the same transfer amount. If that happens,
// reconciliation cannot tell which order a deposit belongs to and either
// settles the wrong one or refuses both. Dropping the WHERE clause would look
// like a harmless cleanup, so pin it here.
func TestUSDTIntentMigrationKeepsPendingAmountUniqueness(t *testing.T) {
	content, err := FS.ReadFile("192_usdt_payment_intents.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS usdt_payment_intents")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS usdtpaymentintent_address_amount_usdt")
	require.Contains(t, sql, "ON usdt_payment_intents(address, amount_usdt)")
	require.Contains(t, sql, "WHERE status = 'PENDING'")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS usdtpaymentintent_order_id")
}

// Amounts are compared for exact equality during reconciliation. Storing them
// as a float type would reintroduce rounding and silently break matching, so
// the column type is load-bearing.
func TestUSDTMigrationsStoreAmountsAsExactText(t *testing.T) {
	for _, file := range []string{"192_usdt_payment_intents.sql", "193_usdt_deposits.sql"} {
		content, err := FS.ReadFile(file)
		require.NoError(t, err)

		columnType := amountColumnType(t, string(content))
		require.Equal(t, "VARCHAR(32)", columnType,
			"%s must store amount_usdt as exact text; a float type reintroduces rounding "+
				"and silently breaks exact-amount reconciliation", file)
	}
}

// amountColumnType extracts the declared type of the amount_usdt column,
// independent of how the DDL happens to be aligned.
func amountColumnType(t *testing.T, sql string) string {
	t.Helper()
	for _, line := range strings.Split(sql, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "amount_usdt" {
			return fields[1]
		}
	}
	t.Fatal("no amount_usdt column declaration found")
	return ""
}

// A single on-chain transfer must never be able to settle two orders.
func TestUSDTDepositMigrationKeepsReplayGuard(t *testing.T) {
	content, err := FS.ReadFile("193_usdt_deposits.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS usdt_deposits")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS usdtdeposit_tx_hash_address_amount_usdt")
	require.Contains(t, sql, "ON usdt_deposits(tx_hash, address, amount_usdt)")
}

func TestUSDTMigrationsAreIdempotent(t *testing.T) {
	for _, file := range []string{"192_usdt_payment_intents.sql", "193_usdt_deposits.sql"} {
		content, err := FS.ReadFile(file)
		require.NoError(t, err)

		sql := string(content)
		require.NotContains(t, sql, "CREATE TABLE usdt", "%s must use CREATE TABLE IF NOT EXISTS", file)
		require.NotContains(t, sql, "CREATE INDEX usdt", "%s must use CREATE INDEX IF NOT EXISTS", file)
		require.NotContains(t, sql, "CREATE UNIQUE INDEX usdt", "%s must use CREATE UNIQUE INDEX IF NOT EXISTS", file)
	}
}
