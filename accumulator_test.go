package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	cfg := &DCAConfig{WindowStart: "2026-09-01", Symbol: "HYPE"}
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
	if b.Days != 2 || b.Symbol != "HYPE" || b.Market != marketSpot || b.WindowStart != "2026-09-01" {
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
	cfg := &DCAConfig{WindowStart: "2026-09-01", Symbol: "HYPE"}
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
	if err := (DCAConfig{WindowStart: "2026-09-01", Symbol: "HYPE"}).validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := (DCAConfig{WindowStart: "2026-9-1", Symbol: "HYPE"}).validate(); err == nil {
		t.Fatal("malformed window start accepted")
	}
	future := time.Now().UTC().AddDate(0, 0, 2).Format(subsidyDateLayout)
	if err := (DCAConfig{WindowStart: future, Symbol: "HYPE"}).validate(); err == nil {
		t.Fatal("future window start accepted")
	}
	if err := (DCAConfig{WindowStart: "2026-09-01"}).validate(); err == nil {
		t.Fatal("missing symbol accepted")
	}
	// The cost basis comes from the accumulator's HYPE balance and HYPE
	// spend, so another symbol would compare HYPE purchases against an
	// unrelated market and report the difference as an execution edge.
	if err := (DCAConfig{WindowStart: "2026-09-01", Symbol: "SOL"}).validate(); err == nil {
		t.Fatal("a symbol the cost basis cannot describe was accepted")
	}
	// Spot and perp are not interchangeable, so a typo must not silently
	// pick one of them.
	if err := (DCAConfig{WindowStart: "2026-09-01", Symbol: "HYPE", Market: "Spot"}).validate(); err == nil {
		t.Fatal("unknown market accepted")
	}
	if err := (DCAConfig{WindowStart: "2026-09-01", Symbol: "HYPE", Market: marketPerp}).validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
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
	cfg := &AccumulatorConfig{DCA: &DCAConfig{WindowStart: "2026-09-01", Symbol: "HYPE", Market: marketPerp}}
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

func TestCandleMarketResolvesSpotRatherThanAskingForThePerp(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: holderTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: 200,
			Body: io.NopCloser(strings.NewReader(
				`[{"tokens":[{"name":"USDC","index":0},{"name":"HYPE","index":107}],` +
					`"universe":[{"tokens":[107,0],"name":"@107"}]},[]]`)),
			Header: make(http.Header),
		}, nil
	})}
	now := time.Now()
	spotPairs.fetchedAt = time.Time{} // this cache is process-wide

	// candleSnapshot reads a bare symbol as the perpetual, so a spot
	// accumulator asked for "HYPE" would be benchmarked against the perp
	// and its execution edge would measure the basis.
	market, msg := candleMarket(context.Background(), client, DCAConfig{Symbol: "HYPE"}, now)
	if msg != "" || market != "@107" {
		t.Fatalf("market = %q, msg = %q; want @107", market, msg)
	}
	// A perp benchmark is the bare symbol and needs no lookup.
	market, msg = candleMarket(context.Background(), client, DCAConfig{Symbol: "HYPE", Market: marketPerp}, now)
	if msg != "" || market != "HYPE" {
		t.Fatalf("market = %q, msg = %q; want HYPE", market, msg)
	}
	// An asset with no spot market says so rather than falling back.
	if _, msg = candleMarket(context.Background(), client, DCAConfig{Symbol: "NOPE"}, now); msg != "No spot market for NOPE" {
		t.Fatalf("msg = %q", msg)
	}
	if calls != 1 {
		t.Fatalf("spot metadata read %d times, want 1 (cached)", calls)
	}
	spotPairs.fetchedAt = time.Time{}
}

func TestDailyClosesDropTheStillOpenCandleAndRetryAfterAFailure(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	day := int64(86400000)
	closed := now.Truncate(24 * time.Hour).UnixMilli() // today's candle ends in the future
	body := fmt.Sprintf(
		`[{"c":"60","T":%d},{"c":"120","T":%d},{"c":"999","T":%d}]`,
		closed-day-1, closed-1, closed+day-1,
	)
	client := &http.Client{Transport: holderTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	closes, err := fetchDailyCloses(context.Background(), client, "@107", now.AddDate(0, 0, -3), now)
	if err != nil {
		t.Fatal(err)
	}
	// The open candle's close is the latest intraday price, not a daily
	// close; counting it would price today's purchase at a tick.
	if len(closes) != 2 || closes[0] != 60 || closes[1] != 120 {
		t.Fatalf("closes = %v, want the two completed days", closes)
	}

	// A transient failure must not blank the benchmark for the full
	// 30-minute TTL.
	failing := &http.Client{Transport: holderTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	cache := &dailyCloseCache{entries: map[string]dailyCloseEntry{}}
	start := now.AddDate(0, 0, -3)
	if _, msg := cache.get(context.Background(), failing, "@107", start, now); msg == "" {
		t.Fatal("HTTP 429 accepted")
	}
	if _, msg := cache.get(context.Background(), client, "@107", start, now.Add(30*time.Second)); msg == "" {
		t.Fatal("a cached failure should still be served inside the error TTL")
	}
	if closes, msg := cache.get(context.Background(), client, "@107", start, now.Add(2*time.Minute)); msg != "" || len(closes) != 2 {
		t.Fatalf("failure held past its TTL: %q %v", msg, closes)
	}
}
