package usdt

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

type tronStub struct {
	server   *httptest.Server
	mu       sync.Mutex
	pages    []string
	status   int
	requests []url.Values
	headers  []http.Header
}

func newTronStub(t *testing.T, pages ...string) *tronStub {
	t.Helper()
	stub := &tronStub{pages: pages, status: http.StatusOK}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		stub.requests = append(stub.requests, r.URL.Query())
		stub.headers = append(stub.headers, r.Header.Clone())
		body := ""
		if len(stub.pages) > 0 {
			body = stub.pages[0]
			stub.pages = stub.pages[1:]
		}
		status := stub.status
		stub.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *tronStub) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *tronStub) request(i int) url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[i]
}

func (s *tronStub) header(i int) http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.headers[i]
}

func tronPage(fingerprint string, entries ...string) string {
	meta := `"meta":{"page_size":` + fmt.Sprint(len(entries)) + `}`
	if fingerprint != "" {
		meta = `"meta":{"fingerprint":"` + fingerprint + `","page_size":` + fmt.Sprint(len(entries)) + `}`
	}
	return `{"success":true,"data":[` + strings.Join(entries, ",") + `],` + meta + `}`
}

func trc20Entry(txID, from, to, value, contract string, blockMillis int64) string {
	return fmt.Sprintf(`{
		"transaction_id":"%s",
		"token_info":{"symbol":"USDT","address":"%s","decimals":6,"name":"Tether USD"},
		"block_timestamp":%d,
		"from":"%s","to":"%s","type":"Transfer","value":"%s"
	}`, txID, contract, blockMillis, from, to, value)
}

func testTronClient(t *testing.T, stub *tronStub) *TronClient {
	t.Helper()
	client, err := NewTronClient(TronOptions{
		APIBase:       stub.server.URL,
		APIKey:        "tron-key",
		TokenContract: validUSDTContract,
		HTTPClient:    stub.server.Client(),
	})
	if err != nil {
		t.Fatalf("NewTronClient() error = %v", err)
	}
	return client
}

func TestNewTronClientRejectsBadConfig(t *testing.T) {
	tests := []struct {
		name string
		opts TronOptions
	}{
		{"missing api key", TronOptions{APIBase: "https://api.trongrid.io", TokenContract: validUSDTContract}},
		{"invalid token contract", TronOptions{APIBase: "https://api.trongrid.io", APIKey: "k", TokenContract: "nope"}},
		{"non-https api base", TronOptions{APIBase: "http://api.trongrid.io", APIKey: "k", TokenContract: validUSDTContract}},
		{"unknown api base host", TronOptions{APIBase: "https://evil.example.com", APIKey: "k", TokenContract: validUSDTContract}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewTronClient(tc.opts); err == nil {
				t.Fatalf("NewTronClient(%+v) = nil error, want error", tc.opts)
			}
		})
	}
}

func TestNewTronClientAcceptsOfficialHosts(t *testing.T) {
	for _, base := range []string{"https://api.trongrid.io", "https://api.shasta.trongrid.io", "https://api.trongrid.io/"} {
		if _, err := NewTronClient(TronOptions{APIBase: base, APIKey: "k", TokenContract: validUSDTContract}); err != nil {
			t.Fatalf("NewTronClient(%q) error = %v", base, err)
		}
	}
}

func TestListIncomingTransfersParsesAmountsExactly(t *testing.T) {
	// 14293700 raw units at 6 decimals is exactly 14.2937 USDT. Going through
	// float64 here would be enough to break exact-amount reconciliation.
	stub := newTronStub(t, tronPage("",
		trc20Entry("tx1", validAccountAddr, validUSDTContract, "14293700", validUSDTContract, 1753939200000),
	))
	client := testTronClient(t, stub)

	transfers, err := client.ListIncomingTransfers(context.Background(), validUSDTContract, time.UnixMilli(0))
	if err != nil {
		t.Fatalf("ListIncomingTransfers() error = %v", err)
	}
	if len(transfers) != 1 {
		t.Fatalf("got %d transfers, want 1", len(transfers))
	}

	got := transfers[0]
	if want := decimal.RequireFromString("14.2937"); !got.Amount.Equal(want) {
		t.Fatalf("Amount = %s, want %s", got.Amount, want)
	}
	if got.TxHash != "tx1" {
		t.Fatalf("TxHash = %q, want tx1", got.TxHash)
	}
	if got.From != validAccountAddr {
		t.Fatalf("From = %q, want %q", got.From, validAccountAddr)
	}
	if want := time.UnixMilli(1753939200000).UTC(); !got.BlockTimestamp.Equal(want) {
		t.Fatalf("BlockTimestamp = %s, want %s", got.BlockTimestamp, want)
	}
}

func TestListIncomingTransfersSendsRequiredQueryAndAuth(t *testing.T) {
	stub := newTronStub(t, tronPage(""))
	client := testTronClient(t, stub)
	since := time.UnixMilli(1753939200000)

	if _, err := client.ListIncomingTransfers(context.Background(), validUSDTContract, since); err != nil {
		t.Fatalf("ListIncomingTransfers() error = %v", err)
	}

	query := stub.request(0)
	// only_confirmed is non-negotiable: unconfirmed TRON transfers can still be
	// orphaned, and crediting an order from one would hand out balance for money
	// that never settled.
	if query.Get("only_confirmed") != "true" {
		t.Fatalf("only_confirmed = %q, want true", query.Get("only_confirmed"))
	}
	if query.Get("only_to") != "true" {
		t.Fatalf("only_to = %q, want true", query.Get("only_to"))
	}
	if query.Get("contract_address") != validUSDTContract {
		t.Fatalf("contract_address = %q, want %q", query.Get("contract_address"), validUSDTContract)
	}
	if query.Get("min_timestamp") != "1753939200000" {
		t.Fatalf("min_timestamp = %q, want epoch millis 1753939200000", query.Get("min_timestamp"))
	}
	if got := stub.header(0).Get("TRON-PRO-API-KEY"); got != "tron-key" {
		t.Fatalf("TRON-PRO-API-KEY = %q, want tron-key", got)
	}
}

func TestListIncomingTransfersFollowsFingerprintPagination(t *testing.T) {
	stub := newTronStub(t,
		tronPage("fp-1", trc20Entry("tx1", validAccountAddr, validUSDTContract, "1000000", validUSDTContract, 1753939200000)),
		tronPage("", trc20Entry("tx2", validAccountAddr, validUSDTContract, "2000000", validUSDTContract, 1753939260000)),
	)
	client := testTronClient(t, stub)

	transfers, err := client.ListIncomingTransfers(context.Background(), validUSDTContract, time.UnixMilli(0))
	if err != nil {
		t.Fatalf("ListIncomingTransfers() error = %v", err)
	}
	if len(transfers) != 2 {
		t.Fatalf("got %d transfers, want 2 across pages", len(transfers))
	}
	if stub.requestCount() != 2 {
		t.Fatalf("made %d requests, want 2", stub.requestCount())
	}
	if got := stub.request(1).Get("fingerprint"); got != "fp-1" {
		t.Fatalf("second page fingerprint = %q, want fp-1", got)
	}
}

func TestListIncomingTransfersStopsAtPageCap(t *testing.T) {
	pages := make([]string, 0, tronMaxPages+2)
	for i := range tronMaxPages + 2 {
		pages = append(pages, tronPage(fmt.Sprintf("fp-%d", i),
			trc20Entry(fmt.Sprintf("tx%d", i), validAccountAddr, validUSDTContract, "1000000", validUSDTContract, 1753939200000)))
	}
	stub := newTronStub(t, pages...)
	client := testTronClient(t, stub)

	if _, err := client.ListIncomingTransfers(context.Background(), validUSDTContract, time.UnixMilli(0)); err != nil {
		t.Fatalf("ListIncomingTransfers() error = %v", err)
	}
	if stub.requestCount() > tronMaxPages {
		t.Fatalf("made %d requests, want at most %d", stub.requestCount(), tronMaxPages)
	}
}

func TestListIncomingTransfersFiltersNonMatchingEntries(t *testing.T) {
	otherContract := validAccountAddr // any valid address that is not our token
	stub := newTronStub(t, tronPage("",
		// Wrong contract: a scam token airdropped to us must never credit an order.
		trc20Entry("tx-other-token", validAccountAddr, validUSDTContract, "14293700", otherContract, 1753939200000),
		// Approval events share the endpoint but move no money.
		`{"transaction_id":"tx-approval","token_info":{"address":"`+validUSDTContract+`","decimals":6},
		  "block_timestamp":1753939200000,"from":"`+validAccountAddr+`","to":"`+validUSDTContract+`",
		  "type":"Approval","value":"99000000"}`,
		// Outgoing transfer (to someone else) despite only_to.
		trc20Entry("tx-outgoing", validUSDTContract, validAccountAddr, "5000000", validUSDTContract, 1753939200000),
		// The one real deposit.
		trc20Entry("tx-good", validAccountAddr, validUSDTContract, "14293700", validUSDTContract, 1753939200000),
	))
	client := testTronClient(t, stub)

	transfers, err := client.ListIncomingTransfers(context.Background(), validUSDTContract, time.UnixMilli(0))
	if err != nil {
		t.Fatalf("ListIncomingTransfers() error = %v", err)
	}
	if len(transfers) != 1 || transfers[0].TxHash != "tx-good" {
		t.Fatalf("got %+v, want only tx-good", transfers)
	}
}

func TestListIncomingTransfersSkipsUnusableEntries(t *testing.T) {
	stub := newTronStub(t, tronPage("",
		`{"transaction_id":"","token_info":{"address":"`+validUSDTContract+`","decimals":6},"block_timestamp":1753939200000,"from":"`+validAccountAddr+`","to":"`+validUSDTContract+`","type":"Transfer","value":"1000000"}`,
		`{"transaction_id":"tx-bad-value","token_info":{"address":"`+validUSDTContract+`","decimals":6},"block_timestamp":1753939200000,"from":"`+validAccountAddr+`","to":"`+validUSDTContract+`","type":"Transfer","value":"abc"}`,
		`{"transaction_id":"tx-zero","token_info":{"address":"`+validUSDTContract+`","decimals":6},"block_timestamp":1753939200000,"from":"`+validAccountAddr+`","to":"`+validUSDTContract+`","type":"Transfer","value":"0"}`,
		// A record with no type at all is not something we can vouch for as a
		// transfer, so it is dropped rather than assumed.
		`{"transaction_id":"tx-no-type","token_info":{"address":"`+validUSDTContract+`","decimals":6},"block_timestamp":1753939200000,"from":"`+validAccountAddr+`","to":"`+validUSDTContract+`","value":"1000000"}`,
		`{"transaction_id":"tx-no-timestamp","token_info":{"address":"`+validUSDTContract+`","decimals":6},"block_timestamp":0,"from":"`+validAccountAddr+`","to":"`+validUSDTContract+`","type":"Transfer","value":"1000000"}`,
		trc20Entry("tx-good", validAccountAddr, validUSDTContract, "1000000", validUSDTContract, 1753939200000),
	))
	client := testTronClient(t, stub)

	transfers, err := client.ListIncomingTransfers(context.Background(), validUSDTContract, time.UnixMilli(0))
	if err != nil {
		t.Fatalf("ListIncomingTransfers() error = %v", err)
	}
	if len(transfers) != 1 || transfers[0].TxHash != "tx-good" {
		t.Fatalf("got %+v, want only tx-good", transfers)
	}
}

func TestListIncomingTransfersFailsOnUpstreamErrors(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
	}{
		{"http 500", `{"error":"boom"}`, http.StatusInternalServerError},
		{"http 429", `{"Error":"rate limited"}`, http.StatusTooManyRequests},
		{"malformed json", `<html>blocked</html>`, http.StatusOK},
		{"success false", `{"success":false,"error":"bad address"}`, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := newTronStub(t, tc.body)
			stub.status = tc.status
			client := testTronClient(t, stub)

			if _, err := client.ListIncomingTransfers(context.Background(), validUSDTContract, time.UnixMilli(0)); err == nil {
				t.Fatal("ListIncomingTransfers() = nil error, want error")
			}
		})
	}
}

func TestListIncomingTransfersRejectsInvalidAddress(t *testing.T) {
	stub := newTronStub(t, tronPage(""))
	client := testTronClient(t, stub)

	if _, err := client.ListIncomingTransfers(context.Background(), "not-an-address", time.UnixMilli(0)); err == nil {
		t.Fatal("ListIncomingTransfers() with bad address = nil error, want error")
	}
	if stub.requestCount() != 0 {
		t.Fatal("invalid address should be rejected before any upstream call")
	}
}
