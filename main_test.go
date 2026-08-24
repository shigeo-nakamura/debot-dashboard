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
