package main

import (
	"context"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The naive DCA benchmark answers "did the accumulator beat spending the
// same budget every day from window_start". That number also carries
// every schedule decision: a ramp from a small probe size to the full
// daily budget, and days the bot did not buy at all. On 2026-09-29 the
// naive edge read -547 bps, of which -232 were no-buy days and -300 the
// $25 -> $95 ramp; execution itself was about zero. The execution edge
// isolates that last part: for every fill, the benchmark spends the
// same dollars at the open of the hour the fill landed in.

var hlAddressPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// ExecutionEdge is the same-dollars-same-hour comparison.
type ExecutionEdge struct {
	// BenchmarkPriceUSD is Σusd / Σ(usd / hourly open) over the fills.
	BenchmarkPriceUSD float64 `json:"benchmark_price_usd"`
	// CostBasisUSD is what those same fills cost per unit received:
	// notional plus any USDC fee, over size minus any fee taken in the
	// asset. Deliberately not the lifetime ledger basis, which covers
	// purchases outside the fills this benchmark prices.
	CostBasisUSD float64 `json:"cost_basis_usd"`
	// EdgeBps is positive when the fills beat the hour's open.
	EdgeBps  float64 `json:"edge_bps"`
	Fills    int     `json:"fills"`
	USDTotal float64 `json:"usd_total"`
	AsOf     int64   `json:"as_of"`
}

// hlFill is the subset of a Hyperliquid userFills entry the edge needs.
type hlFill struct {
	Coin     string `json:"coin"`
	Side     string `json:"side"`
	Px       string `json:"px"`
	Sz       string `json:"sz"`
	Fee      string `json:"fee"`
	FeeToken string `json:"feeToken"`
	Time     int64  `json:"time"`
}

const hourMs = int64(time.Hour / time.Millisecond)

// executionEdge prices the buys of coin (the resolved market id, e.g.
// "@107") against the open of the hour each landed in. opens is keyed by
// the hour's start in unix milliseconds. symbol is the asset's own token
// name (HYPE): a fee charged in it reduces the units received, a fee
// charged in USDC adds to the cost.
func executionEdge(fills []hlFill, opens map[int64]float64, coin, symbol string, now time.Time) (*ExecutionEdge, string) {
	var usdTotal, benchUnits, cost, units float64
	count := 0
	for _, f := range fills {
		if f.Coin != coin || f.Side != "B" {
			continue
		}
		px, err1 := strconv.ParseFloat(f.Px, 64)
		sz, err2 := strconv.ParseFloat(f.Sz, 64)
		fee, err3 := strconv.ParseFloat(f.Fee, 64)
		if err1 != nil || err2 != nil || err3 != nil || !(px > 0) || !(sz > 0) || fee < 0 || math.IsInf(px*sz, 0) {
			return nil, "Invalid fill in account history"
		}
		open, ok := opens[f.Time-f.Time%hourMs]
		if !ok || !(open > 0) {
			return nil, "Hourly price missing for a fill"
		}
		usd := px * sz
		received := sz
		switch f.FeeToken {
		case symbol:
			received -= fee
		case "USDC":
			cost += fee
		default:
			return nil, "Fill fee in an unexpected token"
		}
		usdTotal += usd
		cost += usd
		units += received
		benchUnits += usd / open
		count++
	}
	if count == 0 {
		return nil, "No buys in the window yet"
	}
	if !(units > 0) || !(benchUnits > 0) {
		return nil, "Fills do not add up to a position"
	}
	bench := usdTotal / benchUnits
	basis := cost / units
	edge := (bench - basis) / bench * 10000
	if math.IsNaN(edge) || math.IsInf(edge, 0) {
		return nil, "Fills do not add up to a position"
	}
	return &ExecutionEdge{
		BenchmarkPriceUSD: bench,
		CostBasisUSD:      basis,
		EdgeBps:           edge,
		Fills:             count,
		USDTotal:          usdTotal,
		AsOf:              now.Unix(),
	}, ""
}

// Fills change once a day; the dashboard polls every 20 seconds.
const executionEdgeTTL = 10 * time.Minute

type executionEdgeCache struct {
	mu      sync.Mutex
	entries map[string]executionEdgeEntry
}

type executionEdgeEntry struct {
	edge      *ExecutionEdge
	err       string
	fetchedAt time.Time
}

var executionEdges = &executionEdgeCache{entries: map[string]executionEdgeEntry{}}

func (c *executionEdgeCache) get(ctx context.Context, client *http.Client, address, coin, symbol string, start, now time.Time) (*ExecutionEdge, string) {
	key := strings.ToLower(address) + "|" + coin + "|" + start.Format(subsidyDateLayout)
	c.mu.Lock()
	entry, ok := c.entries[key]
	c.mu.Unlock()
	ttl := executionEdgeTTL
	if ok && entry.err != "" {
		ttl = priceErrorTTL
	}
	if ok && now.Sub(entry.fetchedAt) < ttl {
		return entry.edge, entry.err
	}
	entry = executionEdgeEntry{fetchedAt: now}
	fills, err := fetchFills(ctx, client, address, start, now)
	if err != nil {
		entry.err = err.Error()
	} else {
		opens, oerr := fetchHourlyOpens(ctx, client, coin, fills, now)
		if oerr != nil {
			entry.err = oerr.Error()
		} else {
			entry.edge, entry.err = executionEdge(fills, opens, coin, symbol, now)
		}
	}
	c.mu.Lock()
	c.entries[key] = entry
	c.mu.Unlock()
	return entry.edge, entry.err
}

// userFillsByTime returns at most 2000 fills per call, oldest first;
// page forward from the newest one seen.
const (
	fillsPageLimit = 2000
	fillsMaxPages  = 10
)

func fetchFills(ctx context.Context, client *http.Client, address string, start, now time.Time) ([]hlFill, error) {
	var all []hlFill
	seen := map[string]bool{}
	from := start.UnixMilli()
	for page := 0; page < fillsMaxPages; page++ {
		var batch []struct {
			hlFill
			Tid int64 `json:"tid"`
		}
		body := map[string]any{"type": "userFillsByTime", "user": address, "startTime": from, "endTime": now.UnixMilli()}
		if err := holderJSON(ctx, client, hlInfoURL, body, &batch); err != nil {
			return nil, errors.New("account fills unavailable")
		}
		newest := from
		for _, f := range batch {
			// A page boundary can split one millisecond's fills; the
			// overlap is re-read and dropped by trade id.
			id := strconv.FormatInt(f.Tid, 10) + "|" + strconv.FormatInt(f.Time, 10)
			if seen[id] {
				continue
			}
			seen[id] = true
			all = append(all, f.hlFill)
			if f.Time > newest {
				newest = f.Time
			}
		}
		if len(batch) < fillsPageLimit {
			return all, nil
		}
		from = newest
	}
	return nil, errors.New("account fills history too long")
}

// fetchHourlyOpens reads the 1h candles covering the fills. Candle
// requests are capped at 5000 bars, so they are chunked.
const hourlyChunk = 4000 * time.Hour

func fetchHourlyOpens(ctx context.Context, client *http.Client, coin string, fills []hlFill, now time.Time) (map[int64]float64, error) {
	var first, last int64
	for _, f := range fills {
		if f.Coin != coin || f.Side != "B" {
			continue
		}
		hour := f.Time - f.Time%hourMs
		if first == 0 || hour < first {
			first = hour
		}
		if hour > last {
			last = hour
		}
	}
	opens := map[int64]float64{}
	if first == 0 {
		return opens, nil
	}
	for from := first; from <= last; from += int64(hourlyChunk / time.Millisecond) {
		to := from + int64(hourlyChunk/time.Millisecond) - 1
		if to > now.UnixMilli() {
			to = now.UnixMilli()
		}
		// "T" (close time) is declared even though it is unused:
		// encoding/json matches keys case-insensitively, so without its
		// own field "T" would overwrite "t" and every open would be keyed
		// on the hour's last millisecond.
		var candles []struct {
			Open  string `json:"o"`
			Start int64  `json:"t"`
			End   int64  `json:"T"`
		}
		body := map[string]any{"type": "candleSnapshot", "req": map[string]any{"coin": coin, "interval": "1h", "startTime": from, "endTime": to}}
		if err := holderJSON(ctx, client, hlInfoURL, body, &candles); err != nil {
			return nil, errors.New("hourly price history unavailable")
		}
		for _, candle := range candles {
			value, err := strconv.ParseFloat(candle.Open, 64)
			if err != nil || !(value > 0) || math.IsInf(value, 0) {
				return nil, errors.New("invalid hourly price history")
			}
			opens[candle.Start] = value
		}
	}
	return opens, nil
}
