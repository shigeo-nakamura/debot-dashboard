package main

import (
	"errors"
	"math"
	"regexp"
)

// Operator-verified startup settings, NOT controls for the trading bot.
// Bind the snapshot to the producer's fingerprint to suppress stale budgets.
type HolderInvestment struct {
	ConfigFP     string   `yaml:"config_fp" json:"config_fp"`
	EquityUSD    float64  `yaml:"equity_usd" json:"equity_usd"`
	SpotFraction float64  `yaml:"spot_fraction" json:"spot_fraction"`
	PerpFraction *float64 `yaml:"perp_fraction" json:"perp_fraction"`
	// Anchor pins the buy & hold benchmark: what the same capital would
	// be worth today had it been spent on spot at anchor time and left
	// alone. Optional — without it the benchmark rows render "-" rather
	// than a made-up baseline. bot-strategy#955.
	Anchor *HolderAnchor `yaml:"anchor" json:"anchor,omitempty"`
}

// HolderAnchor is the benchmark's starting point: when the capital was
// deployed and what each asset cost then. Both are operator-verified
// against the bot's startup log, exactly like the rest of the investment
// snapshot, and share its config_fp freshness binding.
type HolderAnchor struct {
	TS int64 `yaml:"ts" json:"ts"`
	// FundedUSD is the total account equity at TS — what was actually
	// put behind the bot, which is not the same number as the strategy's
	// declared EquityUSD. Bull-holder declares $1000 while its accounts
	// hold $1301 (the perp leg's margin buffer), and sizing the benchmark
	// from the declared figure while comparing it against live account
	// equity reported the $301 difference as a 31% outperformance
	// (bot-strategy#963). Optional: an account funded at exactly the
	// declared capital needs nothing here.
	FundedUSD *float64            `yaml:"funded_usd" json:"funded_usd,omitempty"`
	Assets    []HolderAnchorAsset `yaml:"assets" json:"assets"`
}

// HolderAnchorAsset is one leg of the benchmark book. Symbol is the leg
// name the bot uses (BTC, ETH); SpotSymbol is the Hyperliquid spot token
// the current price is read from, which differs for wrapped assets
// (UBTC, UETH) and defaults to Symbol.
type HolderAnchorAsset struct {
	Symbol     string  `yaml:"symbol" json:"symbol"`
	SpotSymbol string  `yaml:"spot_symbol" json:"spot_symbol,omitempty"`
	PriceUSD   float64 `yaml:"price_usd" json:"price_usd"`
	// Units is the quantity of this leg the benchmark holds. Optional,
	// and all-or-nothing across the anchor's assets.
	//
	// Without it the benchmark derives quantities by splitting the
	// declared spot allocation equally and dividing by PriceUSD, which
	// is only the bot's book when the bot bought everything at PriceUSD.
	// It does not, when BULL_HOLDER_ENTRY_TRANCHES > 1: the bot's
	// quantity is the sum of each tranche's USD at its own fill price,
	// so a ladder that ran through moving prices leaves the two sides
	// holding different amounts of the same asset. Every later move then
	// shows up as "excess" even though it is a size difference, not the
	// leverage and hedge the comparison is about (Codex, PR #51).
	//
	// Set it from the bot's actual per-leg spot quantity at the cutover
	// and the benchmark holds the same exposure the bot does, with the
	// perp leg and its margin left out, which is the whole point.
	Units *float64 `yaml:"units" json:"units,omitempty"`
}

func (a HolderAnchorAsset) priceSymbol() string {
	if a.SpotSymbol != "" {
		return a.SpotSymbol
	}
	return a.Symbol
}

func (v HolderInvestment) validate() error {
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(v.ConfigFP) ||
		finiteHolderValue(v.EquityUSD) == nil || v.EquityUSD <= 0 ||
		finiteHolderValue(v.SpotFraction) == nil || v.SpotFraction <= 0 || v.SpotFraction > 1 ||
		v.PerpFraction == nil || finiteHolderValue(*v.PerpFraction) == nil || *v.PerpFraction < 0 || *v.PerpFraction > 1 {
		return errors.New("invalid bull_holder.investment startup snapshot")
	}
	if v.Anchor != nil {
		if err := v.Anchor.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (a HolderAnchor) validate() error {
	if a.TS <= 0 || len(a.Assets) == 0 {
		return errors.New("invalid bull_holder.investment.anchor")
	}
	if a.FundedUSD != nil {
		if finiteHolderValue(*a.FundedUSD) == nil || *a.FundedUSD <= 0 {
			return errors.New("invalid bull_holder.investment.anchor.funded_usd")
		}
	}
	seen := map[string]bool{}
	withUnits := 0
	for _, asset := range a.Assets {
		if asset.Symbol == "" || seen[asset.Symbol] ||
			finiteHolderValue(asset.PriceUSD) == nil || asset.PriceUSD <= 0 {
			return errors.New("invalid bull_holder.investment.anchor asset")
		}
		if asset.Units != nil {
			if finiteHolderValue(*asset.Units) == nil || *asset.Units <= 0 {
				return errors.New("invalid bull_holder.investment.anchor asset units")
			}
			withUnits++
		}
		seen[asset.Symbol] = true
	}
	// All or nothing: a partial set would size some legs from the bot's
	// real book and the rest from the declared allocation, which is two
	// different benchmarks added together.
	if withUnits != 0 && withUnits != len(a.Assets) {
		return errors.New("bull_holder.investment.anchor: units must be set on every asset or none")
	}
	return nil
}

func verifiedHolderInvestment(v *HolderInvestment, fp string, ts int64) (*HolderInvestment, string) {
	if v == nil {
		return nil, "Verified startup investment settings not configured"
	}
	if v.validate() != nil || ts <= 0 || fp != v.ConfigFP {
		return nil, "Startup investment snapshot does not match producer configuration"
	}
	copy := *v
	return &copy, ""
}

func finiteHolderValue(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

func setHolderAccountPnL(a *HolderAccount) {
	a.UnrealizedPnL = nil
	if a.ObservedAt == nil || a.Error != "" {
		return
	}
	total := 0.0
	for _, h := range a.Holdings {
		if h.UnrealizedPnL == nil {
			return
		}
		total += *h.UnrealizedPnL
	}
	a.UnrealizedPnL = finiteHolderValue(total)
}

func sumHolderPnL(accounts ...HolderAccount) *float64 {
	total := 0.0
	for _, a := range accounts {
		if a.UnrealizedPnL == nil {
			return nil
		}
		total += *a.UnrealizedPnL
	}
	return finiteHolderValue(total)
}

// HolderBenchmark is the β benchmark for the bull-holder: what the same
// verified capital would be worth now if, at the anchor, the spot
// allocation had simply been bought and held and the rest left in cash
// (bot-strategy#954 §4.1, #955). It is derived by the dashboard from the
// operator-verified investment snapshot plus public spot marks — never
// taken from the producer, which does not compute it.
//
// Both sides of the comparison therefore start at the same equity_usd:
// the bot's own leverage and hedging are what the comparison is about,
// so the benchmark takes none of it (no perp leg, no rebalancing).
type HolderBenchmark struct {
	AnchorTS int64 `json:"anchor_ts"`
	// FundedUSD is the capital both sides start from: the account equity
	// at the anchor, which the strategy's declared capital need not equal.
	FundedUSD float64                `json:"funded_usd"`
	CostUSD   float64                `json:"cost_usd"`
	CashUSD   float64                `json:"cash_usd"`
	EquityUSD float64                `json:"equity_usd"`
	Assets    []HolderBenchmarkAsset `json:"assets"`
}

type HolderBenchmarkAsset struct {
	Symbol      string  `json:"symbol"`
	Units       float64 `json:"units"`
	AnchorPrice float64 `json:"anchor_price_usd"`
	Price       float64 `json:"price_usd"`
	ValueUSD    float64 `json:"value_usd"`
}

// holderBenchmarkFrom values the benchmark book at the supplied marks.
// `investment` must already have passed verifiedHolderInvestment, so the
// snapshot is fingerprint-bound to the running producer config.
//
// Legs are equal-weight by USD at the anchor, matching how the bot
// deploys a tranche (the same tranche_spot_usd into every leg) -- unless
// the anchor carries explicit per-leg `units`, which describe the book
// the bot really ended up with and take over both the weights and the
// cost. A book that is neither equal-weight nor unit-anchored would need
// explicit weights here rather than a silently wrong benchmark.
// holderBook is the set of legs the anchor has to cover. The producer
// only fills `legs` once a tranche has, so before ARM the configured
// universe is the only description of the book there is — and before ARM
// is exactly when a misconfigured anchor sits unnoticed
// (bot-strategy#963). Falls back to the open legs for a producer that
// does not report the universe yet.
func holderBook(b *BullHolderStatus) map[string]struct{} {
	book := map[string]struct{}{}
	if b == nil {
		return book
	}
	for _, symbol := range b.ConfiguredSymbols {
		if symbol != "" {
			book[symbol] = struct{}{}
		}
	}
	if len(book) > 0 {
		return book
	}
	for symbol := range b.Legs {
		book[symbol] = struct{}{}
	}
	return book
}

func holderBenchmarkFrom(investment *HolderInvestment, marks map[string]float64, legs map[string]struct{}, spot []HolderAsset) (*HolderBenchmark, string) {
	if investment == nil {
		return nil, "Verified startup investment settings not configured"
	}
	anchor := investment.Anchor
	if anchor == nil {
		return nil, "Buy & hold anchor not configured"
	}
	if len(marks) == 0 {
		return nil, "Benchmark prices unavailable"
	}
	// The whole spot allocation is split across the anchor's legs, so an
	// anchor that lists fewer legs than the bot trades buys the missing
	// leg's budget of the remaining ones. That is not a partial
	// benchmark, it is a different portfolio, and it moves the comparison
	// by the spread between the legs (Codex, PR #41). Compare against the
	// book the producer reports whenever it reports one.
	if len(legs) > 0 {
		if len(legs) != len(anchor.Assets) {
			return nil, "Benchmark anchor does not match the book's legs"
		}
		for _, asset := range anchor.Assets {
			if _, ok := legs[asset.Symbol]; !ok {
				return nil, "Benchmark anchor does not match the book's legs"
			}
		}
	}
	// Everything in the accounts that is not the benchmark's spot book has
	// to behave like cash, because cash is what the benchmark holds beside
	// its legs. A non-USDC Hyperliquid holding outside the book -- an
	// airdrop, a residual from some earlier strategy -- does not: its value
	// was inside the account equity the operator captured as funded_usd, so
	// the benchmark carries it as a constant while the bot's side marks it
	// to market, and the difference is published as excess. The first
	// reading still looks right, which is what makes it worth suppressing
	// rather than annotating (Codex, PR #51). Holdings already excludes
	// USDC and zero balances, so anything here is a real position.
	held := map[string]bool{}
	for _, asset := range anchor.Assets {
		held[asset.priceSymbol()] = true
		held[asset.Symbol] = true
	}
	for _, asset := range spot {
		if asset.Symbol != "" && !held[asset.Symbol] {
			return nil, "Hyperliquid account holds " + asset.Symbol + " outside the benchmark book"
		}
	}
	// The spot the strategy deploys comes from its declared capital; the
	// cash beside it is whatever else was funded. Both sides of the
	// comparison then start at the funded total, which is what the card
	// reads off the live accounts (bot-strategy#963).
	// Explicit per-leg quantities win: they describe the book the bot
	// actually ended the entry ladder holding, and what that book cost at
	// the anchor is then a consequence, not the declared allocation. The
	// derived path below is only correct for a book bought in one go at
	// PriceUSD (Codex, PR #51).
	explicitUnits := len(anchor.Assets) > 0 && anchor.Assets[0].Units != nil
	cost := investment.EquityUSD * investment.SpotFraction
	if explicitUnits {
		cost = 0
		for _, asset := range anchor.Assets {
			cost += *asset.Units * asset.PriceUSD
		}
		if finiteHolderValue(cost) == nil || cost <= 0 {
			return nil, "Benchmark anchor units do not value"
		}
	}
	funded := investment.EquityUSD
	if anchor.FundedUSD != nil {
		funded = *anchor.FundedUSD
	}
	if funded < cost {
		// Less in the accounts than the strategy says it deploys as spot.
		// Filling the gap would make the benchmark levered, which is the
		// one thing it must never be.
		return nil, "Funded capital is below the anchored spot allocation"
	}
	perLeg := cost / float64(len(anchor.Assets))
	b := HolderBenchmark{
		AnchorTS:  anchor.TS,
		FundedUSD: funded,
		CostUSD:   cost,
		CashUSD:   funded - cost,
	}
	total := b.CashUSD
	for _, asset := range anchor.Assets {
		price, ok := marks[asset.priceSymbol()]
		if !ok || finiteHolderValue(price) == nil || price <= 0 {
			// A partial benchmark would understate the comparison by a
			// whole leg, which is worse than showing nothing.
			return nil, "Benchmark price unavailable for " + asset.Symbol
		}
		units := perLeg / asset.PriceUSD
		if asset.Units != nil {
			units = *asset.Units
		}
		value := units * price
		if finiteHolderValue(units) == nil || finiteHolderValue(value) == nil {
			return nil, "Benchmark valuation unavailable for " + asset.Symbol
		}
		total += value
		b.Assets = append(b.Assets, HolderBenchmarkAsset{
			Symbol:      asset.Symbol,
			Units:       units,
			AnchorPrice: asset.PriceUSD,
			Price:       price,
			ValueUSD:    value,
		})
	}
	if finiteHolderValue(total) == nil {
		return nil, "Benchmark valuation unavailable"
	}
	b.EquityUSD = total
	return &b, ""
}
