package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// The HYPE accumulator is a β + carry bot (taxonomy §3): its return is
// the asset's own price plus, once staking lands, the staking yield. The
// only execution question worth asking of it is whether it accumulated
// more cheaply than the naive alternative — buying the same budget's
// worth every day and not thinking about it (bot-strategy#956).
//
// DCAConfig is the operator-verified window that comparison runs over.
// The bot does not report when it started buying, and guessing that date
// from a status snapshot would silently move the benchmark.
type DCAConfig struct {
	// WindowStart is the first purchase date, UTC, YYYY-MM-DD.
	WindowStart string `yaml:"window_start" json:"window_start"`
	// Coin is the Hyperliquid market whose daily closes stand in for the
	// naive schedule's prices. Defaults to HYPE.
	Coin string `yaml:"coin" json:"coin"`
}

type AccumulatorConfig struct {
	DCA *DCAConfig `yaml:"dca"`
}

func (c DCAConfig) validate() error {
	start, err := subsidyDate(c.WindowStart)
	if err != nil {
		return fmt.Errorf("accumulator.dca.window_start: %w", err)
	}
	if start.After(time.Now().UTC()) {
		return errors.New("accumulator.dca.window_start is in the future")
	}
	return nil
}

func (c DCAConfig) coin() string {
	if c.Coin != "" {
		return c.Coin
	}
	return "HYPE"
}

// DCABenchmark is the dashboard-derived comparison: what one unit would
// have cost on a naive equal-USD daily schedule over the same window,
// against what the bot actually paid.
type DCABenchmark struct {
	WindowStart string `json:"window_start"`
	Coin        string `json:"coin"`
	Days        int    `json:"days"`
	// DCAPriceUSD is the average unit cost of spending the same amount
	// every day: the harmonic mean of the daily closes, not their
	// arithmetic mean. A fixed budget buys more units when the price is
	// low, which is the whole reason DCA is the benchmark; averaging the
	// prices instead would hand the bot an edge it did not earn.
	DCAPriceUSD float64 `json:"dca_price_usd"`
	// CostBasisUSD is what the bot actually paid per unit: authoritative
	// USDC debited on fills, divided by the units held.
	CostBasisUSD *float64 `json:"cost_basis_usd,omitempty"`
	// EdgeBps is positive when the bot accumulated below the naive
	// schedule's average cost.
	EdgeBps *float64 `json:"edge_bps,omitempty"`
	AsOf    int64    `json:"as_of"`
}

const hypeDailyCloseTTL = 30 * time.Minute

// dailyCloseCache keeps the public candle read off the poll loop: daily
// closes change once a day, and the dashboard polls every 20 seconds.
type dailyCloseCache struct {
	mu      sync.Mutex
	entries map[string]dailyCloseEntry
}

type dailyCloseEntry struct {
	closes    []float64
	fetchedAt time.Time
	err       string
}

var dailyCloses = &dailyCloseCache{entries: map[string]dailyCloseEntry{}}

func (c *dailyCloseCache) get(ctx context.Context, client *http.Client, coin string, start time.Time, now time.Time) ([]float64, string) {
	key := coin + "|" + start.Format(subsidyDateLayout)
	c.mu.Lock()
	entry, ok := c.entries[key]
	c.mu.Unlock()
	if ok && now.Sub(entry.fetchedAt) < hypeDailyCloseTTL {
		return entry.closes, entry.err
	}
	closes, err := fetchDailyCloses(ctx, client, coin, start, now)
	entry = dailyCloseEntry{closes: closes, fetchedAt: now}
	if err != nil {
		entry.err = err.Error()
	}
	c.mu.Lock()
	c.entries[key] = entry
	c.mu.Unlock()
	return entry.closes, entry.err
}

// fetchDailyCloses reads public daily candles. No account identity, no
// signing material; the same public info endpoint the bull-holder card
// already uses.
func fetchDailyCloses(ctx context.Context, client *http.Client, coin string, start, now time.Time) ([]float64, error) {
	body, err := json.Marshal(map[string]any{
		"type": "candleSnapshot",
		"req": map[string]any{
			"coin":      coin,
			"interval":  "1d",
			"startTime": start.UnixMilli(),
			"endTime":   now.UnixMilli(),
		},
	})
	if err != nil {
		return nil, errors.New("price history request unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hlInfoURL, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("price history request unavailable")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("price history unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("price history HTTP %d", resp.StatusCode)
	}
	var candles []struct {
		Close string `json:"c"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&candles); err != nil {
		return nil, errors.New("invalid price history response")
	}
	closes := make([]float64, 0, len(candles))
	for _, candle := range candles {
		value, err := strconv.ParseFloat(candle.Close, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
			return nil, errors.New("invalid price history close")
		}
		closes = append(closes, value)
	}
	if len(closes) == 0 {
		return nil, errors.New("no price history for the window")
	}
	return closes, nil
}

// dcaPrice is the average unit cost of spending an equal amount on every
// close in the window: total spend / total units, which is the harmonic
// mean of the prices.
func dcaPrice(closes []float64) (float64, error) {
	if len(closes) == 0 {
		return 0, errors.New("no closes")
	}
	units := 0.0
	for _, close := range closes {
		if close <= 0 {
			return 0, errors.New("non-positive close")
		}
		units += 1 / close
	}
	if units <= 0 || math.IsInf(units, 0) || math.IsNaN(units) {
		return 0, errors.New("degenerate closes")
	}
	return float64(len(closes)) / units, nil
}

// accumulatorDCABenchmark compares the bot's realised cost basis with
// the naive schedule. Cost basis uses only units the ledger actually
// paid for: staking rewards are carry, not a cheap purchase, and folding
// them in would flatter the execution number (bot-strategy#956).
func accumulatorDCABenchmark(
	status *AccumulatorStatus,
	ops *AccumulatorOperations,
	cfg *DCAConfig,
	closes []float64,
	closesErr string,
	now time.Time,
) (*DCABenchmark, string) {
	if cfg == nil {
		return nil, "DCA window not configured"
	}
	if status == nil {
		return nil, "Accumulator balances unavailable"
	}
	if closesErr != "" {
		return nil, closesErr
	}
	price, err := dcaPrice(closes)
	if err != nil {
		return nil, "Price history unusable for the window"
	}
	b := &DCABenchmark{
		WindowStart: cfg.WindowStart,
		Coin:        cfg.coin(),
		Days:        len(closes),
		DCAPriceUSD: price,
		AsOf:        now.Unix(),
	}
	if ops == nil {
		// Without the durable ledger's authoritative spend there is no
		// cost basis to compare, and the mark is not a substitute.
		return b, ""
	}
	purchased := status.HYPEBalance - status.StakingRewards()
	if purchased <= 0 || ops.SpentUSDC <= 0 {
		return b, ""
	}
	basis := ops.SpentUSDC / purchased
	if math.IsNaN(basis) || math.IsInf(basis, 0) {
		return b, ""
	}
	edge := (price - basis) / price * 10000
	if math.IsNaN(edge) || math.IsInf(edge, 0) {
		return b, ""
	}
	b.CostBasisUSD = &basis
	b.EdgeBps = &edge
	return b, ""
}

// applyAccumulatorDCA derives the DCA benchmark onto the status, after
// discarding anything the producer supplied under those fields: the
// comparison is the dashboard's, computed from the operator-verified
// window and public closes.
func applyAccumulatorDCA(ctx context.Context, status *StatusData, cfg *AccumulatorConfig, client *http.Client, now time.Time) {
	if status == nil {
		return
	}
	status.AccumulatorDCA, status.AccumulatorDCAError = nil, ""
	if status.Accumulator == nil {
		return
	}
	if cfg == nil || cfg.DCA == nil {
		status.AccumulatorDCAError = "DCA window not configured"
		return
	}
	start, err := subsidyDate(cfg.DCA.WindowStart)
	if err != nil {
		status.AccumulatorDCAError = "DCA window not configured"
		return
	}
	// Bounded like every other outbound read in the poll loop: a hung
	// price endpoint must not stall the target's goroutine.
	fetchCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	closes, closesErr := dailyCloses.get(fetchCtx, client, cfg.DCA.coin(), start, now)
	status.AccumulatorDCA, status.AccumulatorDCAError = accumulatorDCABenchmark(
		status.Accumulator, status.AccumulatorOps, cfg.DCA, closes, closesErr, now,
	)
}
