package main

import (
	"encoding/json"
	"testing"
)

func TestAccumulatorStatusDecodesWithoutTradingFields(t *testing.T) {
	payload := []byte(`{
		"schema_version": 1,
		"ts": 1787558400,
		"updated_at": "2026-08-24T08:00:00Z",
		"process_started_at": 1787544000,
		"dex": "hyperliquid",
		"dry_run": true,
		"accumulator": {
			"total_equity_usdc": 125.0,
			"usdc_balance": 25.0,
			"hype_balance": 2.5,
			"hype_price_usdc": 40.0,
			"balance_observed_at": "2026-08-24T08:00:00Z",
			"last_trade_at": "2026-08-23T08:00:00Z",
			"trade_cadence": "Mon/Wed/Fri at 12:00 UTC",
			"healthy": true
		}
	}`)
	var status StatusData
	if err := json.Unmarshal(payload, &status); err != nil {
		t.Fatalf("decode status: %v", err)
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
}
