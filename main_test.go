package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const accumulatorFixturePath = "tests/fixtures/hype-accumulator-status-v1.json"

func TestAccumulatorStatusFixtureMatchesDashboardContract(t *testing.T) {
	payload := readAccumulatorFixture(t)
	status, err := decodeStatusPayload(payload)
	if err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.SchemaVersion != accumulatorSchemaVersion {
		t.Fatalf("schema version = %d, want %d", status.SchemaVersion, accumulatorSchemaVersion)
	}
	if status.Accumulator == nil {
		t.Fatal("accumulator status missing")
	}
	if got := status.Accumulator.TotalEquityUSDC; got != 125.0 {
		t.Fatalf("total equity = %v, want 125", got)
	}
	if got := status.Accumulator.HYPEBalance; got != 2.5 {
		t.Fatalf("HYPE balance = %v, want 2.5", got)
	}
	if status.Accumulator.LastTradeAt == nil {
		t.Fatal("last trade time missing")
	}
	if !status.Accumulator.Healthy {
		t.Fatal("healthy status decoded as false")
	}

	var raw any
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("decode raw fixture: %v", err)
	}
	assertNoForbiddenFields(t, raw, "$")
}

func TestAccumulatorStatusRejectsUnsupportedSchema(t *testing.T) {
	payload := bytes.Replace(
		readAccumulatorFixture(t),
		[]byte(`"schema_version": 1`),
		[]byte(`"schema_version": 2`),
		1,
	)
	_, err := decodeStatusPayload(payload)
	if err == nil || !strings.Contains(err.Error(), "unsupported accumulator schema_version 2") {
		t.Fatalf("decode error = %v, want unsupported schema", err)
	}
}

func TestTradingStatusWithoutSchemaStillDecodes(t *testing.T) {
	status, err := decodeStatusPayload([]byte(`{"ts":1,"pnl_total":12.5}`))
	if err != nil {
		t.Fatalf("decode legacy trading status: %v", err)
	}
	if status.Accumulator != nil || status.PnlTotal != 12.5 {
		t.Fatalf("unexpected decoded trading status: %+v", status)
	}
}

func readAccumulatorFixture(t *testing.T) []byte {
	t.Helper()
	payload, err := os.ReadFile(accumulatorFixturePath)
	if err != nil {
		t.Fatalf("read accumulator fixture: %v", err)
	}
	return payload
}

const hanBridgeFixturePath = "tests/fixtures/han-bridge-status-v1.json"

func TestHanBridgeStatusFixtureMatchesDashboardContract(t *testing.T) {
	payload, err := os.ReadFile(hanBridgeFixturePath)
	if err != nil {
		t.Fatalf("read han_bridge fixture: %v", err)
	}
	status, err := decodeStatusPayload(payload)
	if err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.HanBridge == nil {
		t.Fatal("han_bridge status missing")
	}
	if got := status.HanBridge.KrPrimarySymbol; got != "SKHY" {
		t.Fatalf("kr_primary_symbol = %q, want SKHY", got)
	}
	if got := status.HanBridge.UsPrimarySymbol; got != "SNDK" {
		t.Fatalf("us_primary_symbol = %q, want SNDK", got)
	}
	if !status.HanBridge.DayEntered {
		t.Fatal("day_entered decoded as false")
	}
	if status.HanBridge.IneligibleReasons == nil {
		t.Fatal("ineligible_reasons decoded as nil, want an empty (but present) slice")
	}
	// This bot has real trading fields alongside han_bridge (unlike
	// hype-accumulator, which has none) -- unmarshal must not lose them.
	if status.TradeStats == nil {
		t.Fatal("trade_stats missing alongside han_bridge")
	}

	// Scoped to the han_bridge sub-object, not the whole fixture: unlike
	// hype-accumulator (a passive holder that legitimately must never
	// carry pnl_total/trade_stats/positions), Han Bridge is a real
	// trading bot and has those fields as siblings of han_bridge --
	// running the forbidden-field walk over the whole document would
	// false-positive on its own trade_stats/positions/pnl_total. The
	// actual risk this guards against is future account/wallet/signing
	// material leaking into the han_bridge block specifically.
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("decode raw fixture: %v", err)
	}
	hanBridgeRaw, ok := raw["han_bridge"].(map[string]any)
	if !ok {
		t.Fatal("han_bridge missing or not an object in raw fixture")
	}
	assertNoForbiddenFields(t, hanBridgeRaw, "$.han_bridge")
}

const bookFixturePath = "tests/fixtures/book-runtime-status-v1.json"

// The fixture is produced by the book runtime itself
// (`book-runtime --replay`, bot-strategy#937), so this pins the real
// wire shape rather than a hand-written approximation.
func TestBookStatusFixtureMatchesDashboardContract(t *testing.T) {
	payload, err := os.ReadFile(bookFixturePath)
	if err != nil {
		t.Fatalf("read book fixture: %v", err)
	}
	status, err := decodeStatusPayload(payload)
	if err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Book == nil {
		t.Fatal("book status missing")
	}
	if got := status.Book.InstanceID; got != "xsmom-695" {
		t.Fatalf("instance_id = %q, want xsmom-695", got)
	}
	if got := status.Book.ConfigFp; len(got) != 12 {
		t.Fatalf("config_fp = %q, want the 12-hex fingerprint", got)
	}
	if status.Book.LastDecision == nil {
		t.Fatal("last_decision missing")
	}
	if got := status.Book.LastDecision.Outcome; got != "applied" {
		t.Fatalf("last_decision.outcome = %q, want applied", got)
	}
	if status.Book.LastDecision.SignalSha256 == nil {
		t.Fatal("last_decision.signal_sha256 missing: the decision cannot be traced to its signal")
	}
	if got := status.Book.SignalStatus; got == "" {
		t.Fatal("signal_status empty")
	}
	// A halted-or-blocked book must be distinguishable from a running
	// one; this fixture is the running case.
	if status.Book.SessionHalted || status.Book.DailyHalted || !status.Book.EquityReady {
		t.Fatal("fixture should be the healthy case")
	}
	if status.Book.GrossUsd <= 0 {
		t.Fatalf("gross_usd = %v, want the open book's exposure", status.Book.GrossUsd)
	}
	// The runtime has real positions and PnL alongside the book block --
	// unmarshal must not lose them.
	if status.TradeStats == nil {
		t.Fatal("trade_stats missing alongside book")
	}
	if status.PositionCount != len(status.Positions) {
		t.Fatalf("position_count %d != len(positions) %d", status.PositionCount, len(status.Positions))
	}
}

func TestTradingStatusWithoutBookStillDecodes(t *testing.T) {
	payload, err := os.ReadFile(hanBridgeFixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	status, err := decodeStatusPayload(payload)
	if err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Book != nil {
		t.Fatal("book decoded from a payload that has no book block")
	}
}

func TestTradingStatusWithoutHanBridgeStillDecodes(t *testing.T) {
	status, err := decodeStatusPayload([]byte(`{"ts":1,"pnl_total":12.5}`))
	if err != nil {
		t.Fatalf("decode status without han_bridge: %v", err)
	}
	if status.HanBridge != nil {
		t.Fatalf("unexpected han_bridge on a plain trading status: %+v", status.HanBridge)
	}
}

func assertNoForbiddenFields(t *testing.T, value any, path string) {
	t.Helper()
	forbidden := map[string]struct{}{
		"pnl_total":      {},
		"pnl_today":      {},
		"trade_stats":    {},
		"positions":      {},
		"account":        {},
		"address":        {},
		"wallet":         {},
		"api_key":        {},
		"private_key":    {},
		"ciphertext":     {},
		"signature":      {},
		"signed_payload": {},
	}
	switch typed := value.(type) {
	case map[string]any:
		for field, nested := range typed {
			fieldPath := path + "." + field
			if _, exists := forbidden[field]; exists {
				t.Errorf("dashboard-safe accumulator fixture exposes forbidden field %q", fieldPath)
			}
			assertNoForbiddenFields(t, nested, fieldPath)
		}
	case []any:
		for _, nested := range typed {
			assertNoForbiddenFields(t, nested, path+"[]")
		}
	}
}
