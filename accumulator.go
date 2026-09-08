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
	"strings"
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
	// Symbol is the asset being accumulated, as the venue names it
	// (HYPE). Required.
	Symbol string `yaml:"symbol" json:"symbol"`
	// Market selects which of the venue's two markets prices the naive
	// schedule: "spot" (the default, and what the accumulator actually
	// buys) or "perp". They are not interchangeable — candleSnapshot
	// reads a bare symbol as the perpetual, while a spot market is
	// addressed by its own pair id, so pricing spot accumulation against
	// perp closes measures the execution edge against the basis (Codex,
	// PR #40).
	Market string `yaml:"market" json:"market"`
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
	// The cost basis is read from the accumulator's HYPE balance and its
	// HYPE spend, so benchmarking any other symbol would compare HYPE
	// purchases against an unrelated market and call the difference an
	// execution edge (Codex, PR #40). The field exists so the market
	// resolution below is explicit, not so the asset can be changed:
	// generalising it needs the status and ledger fields to name their
	// asset first.
	if strings.TrimSpace(c.Symbol) != "HYPE" {
		return errors.New(`accumulator.dca.symbol must be "HYPE" (the cost basis is read from the HYPE balance and spend)`)
	}
	switch c.market() {
	case marketSpot, marketPerp:
	default:
		return fmt.Errorf("accumulator.dca.market %q (want spot or perp)", c.Market)
	}
	return nil
}

const (
	marketSpot = "spot"
	marketPerp = "perp"
)

func (c DCAConfig) market() string {
	if c.Market == "" {
		return marketSpot
	}
	return c.Market
}

func (c DCAConfig) symbol() string {
	return strings.TrimSpace(c.Symbol)
}

// DCABenchmark is the dashboard-derived comparison: what one unit would
// have cost on a naive equal-USD daily schedule over the same window,
// against what the bot actually paid.
type DCABenchmark struct {
	WindowStart string `json:"window_start"`
	Symbol      string `json:"symbol"`
	Market      string `json:"market"`
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

const (
	hypeDailyCloseTTL = 30 * time.Minute
	// A failed read is cached far more briefly than a good one: closes
	// change once a day, but a timeout or a rate limit is transient and
	// holding it for the full TTL blanks the benchmark for half an hour
	// over one bad request (Codex, PR #40).
	priceErrorTTL = time.Minute
)

// spotPairNames maps a spot token name to the market id candleSnapshot
// wants for it (HYPE → "@107"). Read from the same public metadata the
// bull-holder card uses.
type spotPairCache struct {
	mu        sync.Mutex
	names     map[string]string
	fetchedAt time.Time
	err       string
}

var spotPairs = &spotPairCache{}

func (c *spotPairCache) get(ctx context.Context, client *http.Client, now time.Time) (map[string]string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ttl := hypeDailyCloseTTL
	if c.err != "" {
		ttl = priceErrorTTL
	}
	if !c.fetchedAt.IsZero() && now.Sub(c.fetchedAt) < ttl {
		return c.names, c.err
	}
	names, err := fetchSpotPairNames(ctx, client)
	c.fetchedAt = now
	c.names, c.err = names, ""
	if err != nil {
		c.err = err.Error()
	}
	return c.names, c.err
}

func fetchSpotPairNames(ctx context.Context, client *http.Client) (map[string]string, error) {
	var raw []json.RawMessage
	if err := holderJSON(ctx, client, hlInfoURL, map[string]string{"type": "spotMetaAndAssetCtxs"}, &raw); err != nil {
		return nil, errors.New("spot market metadata unavailable")
	}
	var meta struct {
		Tokens []struct {
			Index int    `json:"index"`
			Name  string `json:"name"`
		} `json:"tokens"`
		Universe []struct {
			Tokens []int  `json:"tokens"`
			Name   string `json:"name"`
		} `json:"universe"`
	}
	if len(raw) == 0 || json.Unmarshal(raw[0], &meta) != nil {
		return nil, errors.New("invalid spot market metadata")
	}
	usdcID := -1
	byIndex := map[int]string{}
	for _, token := range meta.Tokens {
		byIndex[token.Index] = token.Name
		if token.Name == "USDC" {
			usdcID = token.Index
		}
	}
	if usdcID < 0 {
		return nil, errors.New("invalid spot market metadata")
	}
	names := map[string]string{}
	for _, pair := range meta.Universe {
		if len(pair.Tokens) == 2 && pair.Tokens[1] == usdcID {
			if name, ok := byIndex[pair.Tokens[0]]; ok {
				names[name] = pair.Name
			}
		}
	}
	if len(names) == 0 {
		return nil, errors.New("invalid spot market metadata")
	}
	return names, nil
}

// candleMarket resolves the market id candleSnapshot should be asked
// for. A perp is its bare symbol; a spot market is its pair id, which
// has to be looked up — asking for the bare symbol would silently return
// the perpetual's candles instead.
func candleMarket(ctx context.Context, client *http.Client, cfg DCAConfig, now time.Time) (string, string) {
	if cfg.market() == marketPerp {
		return cfg.symbol(), ""
	}
	names, err := spotPairs.get(ctx, client, now)
	if err != "" {
		return "", err
	}
	pair, ok := names[cfg.symbol()]
	if !ok {
		return "", "No spot market for " + cfg.symbol()
	}
	return pair, ""
}

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
	ttl := hypeDailyCloseTTL
	if ok && entry.err != "" {
		ttl = priceErrorTTL
	}
	if ok && now.Sub(entry.fetchedAt) < ttl {
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
		End   int64  `json:"T"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&candles); err != nil {
		return nil, errors.New("invalid price history response")
	}
	closes := make([]float64, 0, len(candles))
	for _, candle := range candles {
		// The current UTC day's candle is still open: its `c` is the
		// latest intraday price, not a daily close. Counting it as a full
		// day of the naive schedule prices today's purchase at a tick
		// (Codex, PR #40).
		if candle.End >= now.UnixMilli() {
			continue
		}
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
		Symbol:      cfg.symbol(),
		Market:      cfg.market(),
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
	market, marketErr := candleMarket(fetchCtx, client, *cfg.DCA, now)
	if marketErr != "" {
		status.AccumulatorDCAError = marketErr
		return
	}
	closes, closesErr := dailyCloses.get(fetchCtx, client, market, start, now)
	status.AccumulatorDCA, status.AccumulatorDCAError = accumulatorDCABenchmark(
		status.Accumulator, status.AccumulatorOps, cfg.DCA, closes, closesErr, now,
	)
}
