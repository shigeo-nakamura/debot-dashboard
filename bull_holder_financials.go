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
	TS     int64               `yaml:"ts" json:"ts"`
	Assets []HolderAnchorAsset `yaml:"assets" json:"assets"`
}

// HolderAnchorAsset is one leg of the benchmark book. Symbol is the leg
// name the bot uses (BTC, ETH); SpotSymbol is the Hyperliquid spot token
// the current price is read from, which differs for wrapped assets
// (UBTC, UETH) and defaults to Symbol.
type HolderAnchorAsset struct {
	Symbol     string  `yaml:"symbol" json:"symbol"`
	SpotSymbol string  `yaml:"spot_symbol" json:"spot_symbol,omitempty"`
	PriceUSD   float64 `yaml:"price_usd" json:"price_usd"`
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
	seen := map[string]bool{}
	for _, asset := range a.Assets {
		if asset.Symbol == "" || seen[asset.Symbol] ||
			finiteHolderValue(asset.PriceUSD) == nil || asset.PriceUSD <= 0 {
			return errors.New("invalid bull_holder.investment.anchor asset")
		}
		seen[asset.Symbol] = true
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
	AnchorTS  int64                  `json:"anchor_ts"`
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
// deploys a tranche (the same tranche_spot_usd into every leg). A book
// whose legs are not equal-weight would need explicit weights here
// rather than a silently wrong benchmark.
func holderBenchmarkFrom(investment *HolderInvestment, marks map[string]float64) (*HolderBenchmark, string) {
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
	cost := investment.EquityUSD * investment.SpotFraction
	perLeg := cost / float64(len(anchor.Assets))
	b := HolderBenchmark{
		AnchorTS: anchor.TS,
		CostUSD:  cost,
		CashUSD:  investment.EquityUSD - cost,
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
