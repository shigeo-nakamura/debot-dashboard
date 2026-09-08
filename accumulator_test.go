package main

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDCAPriceIsTheCostOfSpendingEquallyEveryDay(t *testing.T) {
	// A fixed daily budget buys more units when the price is low, so the
	// achieved average is the harmonic mean (here 80) and not the
	// arithmetic mean of the closes (here 90). Using the arithmetic mean
	// would hand the bot ~12% of edge it never earned.
	price, err := dcaPrice([]float64{60, 120})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(price-80) > 1e-9 {
		t.Fatalf("dca price = %v, want 80", price)
	}
	if _, err := dcaPrice(nil); err == nil {
		t.Fatal("empty window accepted")
	}
	if _, err := dcaPrice([]float64{100, 0}); err == nil {
		t.Fatal("non-positive close accepted")
	}
}

func TestAccumulatorDCABenchmarkSeparatesCarryFromExecution(t *testing.T) {
	now := time.Unix(1788600000, 0).UTC()
	cfg := &DCAConfig{WindowStart: "2026-09-01"}
	closes := []float64{60, 120} // DCA price 80, arithmetic mean 90

	// 10 HYPE held for 700 USDC is a 70 basis: 1250 bps better than the
	// naive schedule's 80.
	b, msg := accumulatorDCABenchmark(
		&AccumulatorStatus{HYPEBalance: 10},
		&AccumulatorOperations{SpentUSDC: 700},
		cfg, closes, "", now,
	)
	if msg != "" || b == nil || b.CostBasisUSD == nil || b.EdgeBps == nil {
		t.Fatalf("benchmark unavailable: %q %+v", msg, b)
	}
	if math.Abs(*b.CostBasisUSD-70) > 1e-9 || math.Abs(*b.EdgeBps-1250) > 1e-9 {
		t.Fatalf("basis = %v, edge = %v bps; want 70 / 1250", *b.CostBasisUSD, *b.EdgeBps)
	}
	if b.Days != 2 || b.Coin != "HYPE" || b.WindowStart != "2026-09-01" {
		t.Fatalf("wrong window: %+v", b)
	}

	// Two of the twelve held HYPE are staking rewards. They are carry,
	// not a cheap purchase: counting them as purchased units would show
	// a 58.3 basis and flatter the execution number.
	rewards := 2.0
	staked, msg := accumulatorDCABenchmark(
		&AccumulatorStatus{HYPEBalance: 12, StakingRewardsHYPE: &rewards},
		&AccumulatorOperations{SpentUSDC: 700},
		cfg, closes, "", now,
	)
	if msg != "" || staked.CostBasisUSD == nil || math.Abs(*staked.CostBasisUSD-70) > 1e-9 {
		t.Fatalf("staking rewards leaked into the cost basis: %+v", staked.CostBasisUSD)
	}
}

func TestAccumulatorDCABenchmarkWithholdsWhatItCannotSource(t *testing.T) {
	now := time.Unix(1788600000, 0).UTC()
	cfg := &DCAConfig{WindowStart: "2026-09-01"}
	closes := []float64{60, 120}

	if _, msg := accumulatorDCABenchmark(&AccumulatorStatus{HYPEBalance: 1}, nil, nil, closes, "", now); msg != "DCA window not configured" {
		t.Fatalf("msg = %q", msg)
	}
	if _, msg := accumulatorDCABenchmark(nil, nil, cfg, closes, "", now); msg != "Accumulator balances unavailable" {
		t.Fatalf("msg = %q", msg)
	}
	if _, msg := accumulatorDCABenchmark(&AccumulatorStatus{HYPEBalance: 1}, nil, cfg, nil, "price history unavailable", now); msg != "price history unavailable" {
		t.Fatalf("msg = %q", msg)
	}
	// The benchmark price stands on its own; without the durable
	// ledger's authoritative spend there is simply no cost basis to
	// compare, and the current mark is not a substitute for one.
	b, msg := accumulatorDCABenchmark(&AccumulatorStatus{HYPEBalance: 10, HYPEPriceUSDC: 90}, nil, cfg, closes, "", now)
	if msg != "" || b == nil || b.CostBasisUSD != nil || b.EdgeBps != nil {
		t.Fatalf("cost basis invented: %+v", b)
	}
	// Nothing bought yet: no division by zero, no basis.
	b, msg = accumulatorDCABenchmark(&AccumulatorStatus{HYPEBalance: 0}, &AccumulatorOperations{SpentUSDC: 0}, cfg, closes, "", now)
	if msg != "" || b.CostBasisUSD != nil {
		t.Fatalf("basis from an empty book: %+v", b)
	}
}

func TestDCAWindowValidation(t *testing.T) {
	if err := (DCAConfig{WindowStart: "2026-09-01"}).validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := (DCAConfig{WindowStart: "2026-9-1"}).validate(); err == nil {
		t.Fatal("malformed window start accepted")
	}
	future := time.Now().UTC().AddDate(0, 0, 2).Format(subsidyDateLayout)
	if err := (DCAConfig{WindowStart: future}).validate(); err == nil {
		t.Fatal("future window start accepted")
	}
}

func TestApplyAccumulatorDCADiscardsProducerSuppliedBenchmarks(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: holderTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req["type"] != "candleSnapshot" {
			t.Fatalf("unexpected request: %v", req)
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`[{"c":"60"},{"c":"120"}]`)),
			Header:     make(http.Header),
		}, nil
	})}
	// Distinct coin so this test does not share the process-wide cache
	// with any other.
	cfg := &AccumulatorConfig{DCA: &DCAConfig{WindowStart: "2026-09-01", Coin: "TESTCOIN"}}
	fake := 99999.0
	status := &StatusData{
		Accumulator:         &AccumulatorStatus{HYPEBalance: 10},
		AccumulatorOps:      &AccumulatorOperations{SpentUSDC: 700},
		AccumulatorDCA:      &DCABenchmark{DCAPriceUSD: fake},
		AccumulatorDCAError: "producer says so",
	}
	now := time.Now()
	applyAccumulatorDCA(context.Background(), status, cfg, client, now)
	if status.AccumulatorDCAError != "" || status.AccumulatorDCA == nil {
		t.Fatalf("benchmark missing: %q", status.AccumulatorDCAError)
	}
	if math.Abs(status.AccumulatorDCA.DCAPriceUSD-80) > 1e-9 {
		t.Fatalf("producer value survived: %+v", status.AccumulatorDCA)
	}

	// Daily closes change once a day and the dashboard polls every 20
	// seconds, so a second poll inside the TTL must not re-read them.
	applyAccumulatorDCA(context.Background(), status, cfg, client, now.Add(time.Minute))
	if calls != 1 {
		t.Fatalf("candle endpoint called %d times, want 1", calls)
	}

	// A target that is not an accumulator gets no benchmark and no error.
	plain := &StatusData{PnlTotal: 100}
	applyAccumulatorDCA(context.Background(), plain, cfg, client, now)
	if plain.AccumulatorDCA != nil || plain.AccumulatorDCAError != "" {
		t.Fatal("benchmark attached to a non-accumulator target")
	}
}
