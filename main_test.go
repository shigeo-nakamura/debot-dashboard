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

// v2 adds the venue-solvency fields (bot-strategy#919). v1 is kept as a
// separate fixture on purpose: a bot that predates #919 must still
// decode, with the new fields nil rather than zero.
const hanBridgeV2FixturePath = "tests/fixtures/han-bridge-status-v2.json"

func TestHanBridgeVenueSolvencyDecodes(t *testing.T) {
	payload, err := os.ReadFile(hanBridgeV2FixturePath)
	if err != nil {
		t.Fatalf("read han_bridge v2 fixture: %v", err)
	}
	status, err := decodeStatusPayload(payload)
	if err != nil {
		t.Fatalf("decode status: %v", err)
	}
	hb := status.HanBridge
	if hb == nil {
		t.Fatal("han_bridge status missing")
	}
	if hb.VenueEquityUsd == nil || *hb.VenueEquityUsd != 5002.31 {
		t.Fatalf("venue_equity_usd = %v, want 5002.31", hb.VenueEquityUsd)
	}
	if hb.VenueAvailableUsd == nil || *hb.VenueAvailableUsd != 4952.06 {
		t.Fatalf("venue_available_usd = %v, want 4952.06", hb.VenueAvailableUsd)
	}
	if hb.VenueEquityAgeSecs == nil || *hb.VenueEquityAgeSecs != 12 {
		t.Fatalf("venue_equity_age_secs = %v, want 12", hb.VenueEquityAgeSecs)
	}
	if hb.UnrealizedPnlUsdMidEstimate == nil || *hb.UnrealizedPnlUsdMidEstimate != 2.31 {
		t.Fatalf("unrealized = %v, want 2.31", hb.UnrealizedPnlUsdMidEstimate)
	}
	if hb.VenueEquityStale {
		t.Fatal("venue_equity_stale decoded as true, want false")
	}
	if !hb.VenueSolvencyReported {
		t.Fatal("venue_solvency_reported decoded as false, want true")
	}
	// The session schedule reaches the frontend as a top-level field,
	// not inside han_bridge: it is emitted by the binary's generic
	// status block. Without it the card cannot say when the exit is due,
	// nor that it is late (bot-strategy#919 follow-up).
	if len(status.Window) != 3 {
		t.Fatalf("window = %v, want three boundaries", status.Window)
	}
	if status.Window[2] != 1789047000000000 {
		t.Fatalf("t2 = %d, want 1789047000000000", status.Window[2])
	}
	if hb.ExitDeadlineUs == nil || *hb.ExitDeadlineUs != 1789047900000000 {
		t.Fatalf("exit_deadline_us = %v, want 1789047900000000", hb.ExitDeadlineUs)
	}
	// A producer reporting that its last read failed must survive the
	// round trip: this is the exact signal the card warns on, and the
	// age is only an approximation beside it.
	failing, err := decodeStatusPayload([]byte(`{"id":"engine-b-live","han_bridge":{"kr_primary_symbol":"SKHY","us_primary_symbol":"SNDK","ineligible_reasons":[],"venue_equity_usd":5000,"venue_equity_stale":true}}`))
	if err != nil {
		t.Fatalf("decode failing payload: %v", err)
	}
	if !failing.HanBridge.VenueEquityStale {
		t.Fatal("venue_equity_stale decoded as false, want true")
	}
}

// The whole point of the pointer types: "not reported" and "reported as
// zero" must stay distinguishable end to end. A bot that has not yet
// read its account publishes null, and the card renders "-"; only a real
// zero renders "$0.00", which is an alarm.
// The finding this pins is not in the decoder but in what leaves the
// server. /api/status re-encodes this struct, and a nil pointer with
// `omitempty` serialises to nothing -- so a producer's explicit null
// would have reached the browser as an absent field, hiding the row
// instead of flagging unknown solvency (PR #46 Codex review). The
// JavaScript view-model tests cannot see this: they never cross the
// server.
func TestHanBridgeSolvencySurvivesTheApiRoundTrip(t *testing.T) {
	const fromProducer = `{"id":"engine-b-live","han_bridge":{"kr_primary_symbol":"SKHY","us_primary_symbol":"SNDK","ineligible_reasons":[],"venue_solvency_reported":true,"venue_equity_usd":null,"venue_available_usd":null,"venue_equity_age_secs":null,"venue_equity_stale":true,"unrealized_pnl_usd_mid_estimate":null}}`

	status, err := decodeStatusPayload([]byte(fromProducer))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	reencoded, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	var toBrowser struct {
		HanBridge map[string]json.RawMessage `json:"han_bridge"`
	}
	if err := json.Unmarshal(reencoded, &toBrowser); err != nil {
		t.Fatalf("decode what the browser would receive: %v", err)
	}
	// The flag is what the frontend keys the row on, so it has to be
	// there and true.
	if got, ok := toBrowser.HanBridge["venue_solvency_reported"]; !ok || string(got) != "true" {
		t.Fatalf("venue_solvency_reported reached the browser as %q (present=%v), want true", got, ok)
	}
	// And the unknown values must arrive as explicit nulls, not as
	// missing keys -- "-" on the card, never a hidden row.
	for _, field := range []string{
		"venue_equity_usd",
		"venue_available_usd",
		"venue_equity_age_secs",
		"unrealized_pnl_usd_mid_estimate",
	} {
		got, ok := toBrowser.HanBridge[field]
		if !ok {
			t.Fatalf("%s was dropped on the way to the browser; an unknown reading must stay visible", field)
		}
		if string(got) != "null" {
			t.Fatalf("%s reached the browser as %q, want null", field, got)
		}
	}
	if got := toBrowser.HanBridge["venue_equity_stale"]; string(got) != "true" {
		t.Fatalf("venue_equity_stale reached the browser as %q, want true", got)
	}
}

// A non-session day emits no window, and the card must render no
// countdown rather than one anchored on a stale schedule.
func TestStatusWithoutAWindowDecodesAsNoSchedule(t *testing.T) {
	status, err := decodeStatusPayload([]byte(`{"id":"engine-b-live","han_bridge":{"kr_primary_symbol":"SKHY","us_primary_symbol":"SNDK","ineligible_reasons":[]}}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.Window != nil {
		t.Fatalf("window = %v, want nil", status.Window)
	}
	if status.HanBridge.ExitDeadlineUs != nil {
		t.Fatalf("exit_deadline_us = %v, want nil", status.HanBridge.ExitDeadlineUs)
	}
}

func TestHanBridgeVenueSolvencyAbsentAndNullBothDecodeAsUnknown(t *testing.T) {
	for name, payload := range map[string]string{
		"absent (a bot predating #919)": `{"id":"engine-b-live","han_bridge":{"kr_primary_symbol":"SKHY","us_primary_symbol":"SNDK","ineligible_reasons":[]}}`,
		"explicitly null (never read)":  `{"id":"engine-b-live","han_bridge":{"kr_primary_symbol":"SKHY","us_primary_symbol":"SNDK","ineligible_reasons":[],"venue_equity_usd":null,"venue_available_usd":null,"venue_equity_age_secs":null,"unrealized_pnl_usd_mid_estimate":null}}`,
	} {
		status, err := decodeStatusPayload([]byte(payload))
		if err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		hb := status.HanBridge
		if hb == nil {
			t.Fatalf("%s: han_bridge missing", name)
		}
		if hb.VenueEquityUsd != nil {
			t.Fatalf("%s: venue_equity_usd = %v, want nil", name, *hb.VenueEquityUsd)
		}
		if hb.VenueAvailableUsd != nil {
			t.Fatalf("%s: venue_available_usd = %v, want nil", name, *hb.VenueAvailableUsd)
		}
		if hb.VenueEquityAgeSecs != nil {
			t.Fatalf("%s: venue_equity_age_secs = %v, want nil", name, *hb.VenueEquityAgeSecs)
		}
		if hb.UnrealizedPnlUsdMidEstimate != nil {
			t.Fatalf("%s: unrealized = %v, want nil", name, *hb.UnrealizedPnlUsdMidEstimate)
		}
		// Neither payload claims to report solvency, so the card hides
		// the rows in both cases -- including the null one, where the
		// nulls are the API's own doing rather than the producer's.
		if hb.VenueSolvencyReported {
			t.Fatalf("%s: venue_solvency_reported = true, want false", name)
		}
	}
}

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
