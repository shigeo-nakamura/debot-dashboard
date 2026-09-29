package main

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const tHour = int64(1790150400000) // an hour boundary, unix ms

func fill(coin, side, px, sz, fee, feeToken string, t int64) hlFill {
	return hlFill{Coin: coin, Side: side, Px: px, Sz: sz, Fee: fee, FeeToken: feeToken, Time: t}
}

func TestExecutionEdgeSameDollarsSameHour(t *testing.T) {
	now := time.UnixMilli(tHour + 5*hourMs)
	opens := map[int64]float64{tHour: 100, tHour + hourMs: 50}
	fills := []hlFill{
		// $100 at 101 in the first hour, 1 unit, fee 0.01 HYPE.
		fill("@107", "B", "100", "1", "0.01", "HYPE", tHour+30_000),
		// $100 at 50 in the second hour, 2 units, fee $0.10 USDC.
		fill("@107", "B", "50", "2", "0.1", "USDC", tHour+hourMs+30_000),
		// Ignored: a sell, another market.
		fill("@107", "A", "200", "1", "0", "USDC", tHour+60_000),
		fill("HYPE", "B", "1", "1000", "0", "USDC", tHour+60_000),
	}
	edge, msg := executionEdge(fills, opens, "@107", "HYPE", now)
	if msg != "" {
		t.Fatal(msg)
	}
	// Benchmark: $200 / (100/100 + 100/50) = 66.667.
	wantBench := 200.0 / 3
	// Basis: ($200 + $0.10 USDC fee) / (3 - 0.01 HYPE fee).
	wantBasis := 200.1 / 2.99
	if math.Abs(edge.BenchmarkPriceUSD-wantBench) > 1e-9 || math.Abs(edge.CostBasisUSD-wantBasis) > 1e-9 {
		t.Fatalf("bench=%v basis=%v, want %v %v", edge.BenchmarkPriceUSD, edge.CostBasisUSD, wantBench, wantBasis)
	}
	if want := (wantBench - wantBasis) / wantBench * 10000; math.Abs(edge.EdgeBps-want) > 1e-6 {
		t.Fatalf("edge=%v want %v", edge.EdgeBps, want)
	}
	if edge.Fills != 2 || edge.USDTotal != 200 || edge.AsOf != now.Unix() {
		t.Fatalf("unexpected %+v", edge)
	}
}

func TestExecutionEdgeFeeInAssetReducesUnits(t *testing.T) {
	opens := map[int64]float64{tHour: 100}
	now := time.UnixMilli(tHour + 2*hourMs)
	noFee, _ := executionEdge([]hlFill{fill("@107", "B", "100", "1", "0", "HYPE", tHour)}, opens, "@107", "HYPE", now)
	withFee, _ := executionEdge([]hlFill{fill("@107", "B", "100", "1", "0.001", "HYPE", tHour)}, opens, "@107", "HYPE", now)
	if noFee.EdgeBps != 0 {
		t.Fatalf("fill at the open with no fee should be 0 bps, got %v", noFee.EdgeBps)
	}
	// A 0.1% fee taken in HYPE costs ~10 bps.
	if math.Abs(withFee.EdgeBps-(-10.01001)) > 1e-3 {
		t.Fatalf("edge with 0.001 HYPE fee = %v, want ≈ -10.01", withFee.EdgeBps)
	}
	withUSDC, _ := executionEdge([]hlFill{fill("@107", "B", "100", "1", "0.1", "USDC", tHour)}, opens, "@107", "HYPE", now)
	if math.Abs(withUSDC.EdgeBps-(-10)) > 1e-9 {
		t.Fatalf("edge with $0.10 USDC fee = %v, want -10", withUSDC.EdgeBps)
	}
}

func TestExecutionEdgeRefusesWhatItCannotPrice(t *testing.T) {
	now := time.UnixMilli(tHour + 2*hourMs)
	opens := map[int64]float64{tHour: 100}
	cases := map[string][]hlFill{
		"No buys":       nil,
		"No buys ":      {fill("@107", "A", "100", "1", "0", "USDC", tHour), fill("@1", "B", "1", "1", "0", "USDC", tHour)},
		"Hourly price":  {fill("@107", "B", "100", "1", "0", "HYPE", tHour+hourMs)},
		"unexpected":    {fill("@107", "B", "100", "1", "0", "PURR", tHour)},
		"Invalid fill":  {fill("@107", "B", "x", "1", "0", "HYPE", tHour)},
		"Invalid fill ": {fill("@107", "B", "100", "1", "NaN", "HYPE", tHour)},
		"add up":        {fill("@107", "B", "100", "1", "1", "HYPE", tHour)},
	}
	for want, fills := range cases {
		edge, msg := executionEdge(fills, opens, "@107", "HYPE", now)
		if edge != nil || !strings.Contains(msg, strings.TrimSpace(want)) {
			t.Errorf("%q: got %+v %q", want, edge, msg)
		}
	}
}

func TestDCAConfigFillsAddress(t *testing.T) {
	base := DCAConfig{WindowStart: "2026-09-10", Symbol: "HYPE"}
	if err := base.validate(); err != nil {
		t.Fatalf("absent fills_address must stay valid: %v", err)
	}
	ok := base
	ok.FillsAddress = "0x1111111111111111111111111111111111111111"
	if err := ok.validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"1111111111111111111111111111111111111111", "0x4d95", "0xZZ11111111111111111111111111111111111111"} {
		c := base
		c.FillsAddress = bad
		if err := c.validate(); err == nil {
			t.Errorf("fills_address %q accepted", bad)
		}
	}
}

func TestFetchHourlyOpensKeysOnCandleStart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Real payload shape: both "t" (open time) and "T" (close time).
		_, _ = w.Write([]byte(`[{"t":1790150400000,"T":1790153999999,"o":"100.5","c":"101"}]`))
	}))
	defer srv.Close()
	client := srv.Client()
	client.Transport = rewriteTransport{target: srv.URL}
	fills := []hlFill{fill("@107", "B", "100", "1", "0", "HYPE", tHour+30_000)}
	opens, err := fetchHourlyOpens(context.Background(), client, "@107", fills, time.UnixMilli(tHour+2*hourMs))
	if err != nil {
		t.Fatal(err)
	}
	if opens[tHour] != 100.5 {
		t.Fatalf("opens=%v, want the open keyed on the hour start %d", opens, tHour)
	}
}

// rewriteTransport sends every request to target, so code that calls the
// hard-coded Hyperliquid URL can be pointed at an httptest server.
type rewriteTransport struct{ target string }

func (rt rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	u, err := url.Parse(rt.target)
	if err != nil {
		return nil, err
	}
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
	return http.DefaultTransport.RoundTrip(r)
}

func TestExecutionWindowStartBoundedByCandleRetention(t *testing.T) {
	now := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	recent := now.Add(-100 * 24 * time.Hour)
	if got := executionWindowStart(recent, now); !got.Equal(recent) {
		t.Fatalf("recent window_start moved to %v", got)
	}
	old := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	got := executionWindowStart(old, now)
	if !got.Equal(now.Add(-4800 * time.Hour)) {
		t.Fatalf("old window_start = %v, want the 4800h bound", got)
	}
	// The bound must stay inside the 5000 hourly bars candleSnapshot keeps.
	if now.Sub(got) >= 5000*time.Hour {
		t.Fatal("bound reaches past candle retention")
	}
}

func TestExecutionEdgeMakerRebate(t *testing.T) {
	opens := map[int64]float64{tHour: 100}
	now := time.UnixMilli(tHour + 2*hourMs)
	edge, msg := executionEdge([]hlFill{fill("@107", "B", "100", "1", "-0.0001", "HYPE", tHour)}, opens, "@107", "HYPE", now)
	if msg != "" || !(edge.EdgeBps > 0) {
		t.Fatalf("a rebate at the open should be a positive edge, got %+v %q", edge, msg)
	}
}
