package main

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func TestHolderInvestmentSnapshot(t *testing.T) {
	v := &HolderInvestment{ConfigFP: "4c67969bf10a", EquityUSD: 1000, SpotFraction: .9, PerpFraction: finiteHolderValue(.45)}
	if err := v.validate(); err != nil {
		t.Fatal(err)
	}
	got, err := verifiedHolderInvestment(v, v.ConfigFP, 1)
	if err != "" || got == nil || got.EquityUSD*got.SpotFraction != 900 || got.EquityUSD**got.PerpFraction != 450 {
		t.Fatal("wrong verified investment")
	}
	for _, tc := range []struct {
		v  *HolderInvestment
		fp string
		ts int64
	}{{nil, v.ConfigFP, 1}, {v, "000000000000", 1}, {v, v.ConfigFP, 0}} {
		if got, err := verifiedHolderInvestment(tc.v, tc.fp, tc.ts); got != nil || err == "" {
			t.Fatal("unverified budget exposed")
		}
	}
	for _, bad := range []HolderInvestment{
		{ConfigFP: v.ConfigFP, EquityUSD: 1000, SpotFraction: .9},
		{ConfigFP: v.ConfigFP, EquityUSD: math.Inf(1), SpotFraction: .9, PerpFraction: finiteHolderValue(.45)},
		{ConfigFP: v.ConfigFP, EquityUSD: 1000, SpotFraction: 1.1, PerpFraction: finiteHolderValue(.45)},
	} {
		if bad.validate() == nil {
			t.Fatal("invalid budget accepted")
		}
	}
	v.PerpFraction = finiteHolderValue(0)
	if v.validate() != nil {
		t.Fatal("explicit zero perp allocation rejected")
	}
}

func TestHolderSpotPnLBasisAndMissingValues(t *testing.T) {
	for _, tc := range []struct {
		basis string
		want  *float64
	}{
		{`"800"`, finiteHolderValue(200)}, {`"1200"`, finiteHolderValue(-200)}, {`"0"`, nil}, {`""`, nil}, {`null`, nil}, {`"NaN"`, nil}, {`"-1"`, nil},
	} {
		balances := strings.Replace(holderBalances, `"coin":"UBTC"`, `"entryNtl":`+tc.basis+`,"coin":"UBTC"`, 1)
		a, _ := fetchHLSpot(context.Background(), holderClient(t, balances, holderLighter), "x")
		if a.Equity == nil || *a.Equity != 1100 {
			t.Fatal("PnL affected equity")
		}
		if tc.want == nil {
			if a.UnrealizedPnL != nil {
				t.Fatal("unknown basis became PnL")
			}
		} else if a.UnrealizedPnL == nil || *a.UnrealizedPnL != *tc.want {
			t.Fatalf("wrong PnL for basis %s", tc.basis)
		}
	}
	a, _ := fetchHLSpot(context.Background(), holderClient(t, `{"balances":[]}`, holderLighter), "x")
	if a.UnrealizedPnL == nil || *a.UnrealizedPnL != 0 {
		t.Fatal("verified empty account should have zero PnL")
	}
	a, _ = fetchHLSpot(context.Background(), holderClient(t, `{}`, holderLighter), "x")
	if a.UnrealizedPnL != nil {
		t.Fatal("missing balances should have unknown PnL")
	}
}

func TestHolderCombinedPnLAndBudgetCannotComeFromSimulation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	writeHolder(t, path, `{"bot":"bull_holder","ts":1,"mode":"On","dry_run":true,"config_fp":"4c67969bf10a","unrealized_pnl_usdc":9999,"investment":{"equity_usd":9999},"legs":{"BTC":{"spot_size":99,"spot_cost_usd":1}}}`)
	v := &HolderInvestment{ConfigFP: "4c67969bf10a", EquityUSD: 1000, SpotFraction: .9, PerpFraction: finiteHolderValue(.45)}
	cfg := TargetConfig{BullHolder: &BullHolderConfig{StatusPath: path, HLAddress: "x", LighterIndex: "42", Investment: v}}
	balances := strings.Replace(holderBalances, `"coin":"UBTC"`, `"entryNtl":"800","coin":"UBTC"`, 1)
	b := fetchBullHolder(context.Background(), cfg, holderClient(t, balances, holderLighter)).Status.BullHolder
	if b.UnrealizedPnL == nil || *b.UnrealizedPnL != 210 || *b.TotalEquity != 1310 {
		t.Fatal("PnL/equity double count or simulation leaked")
	}
	if b.Investment == nil || b.Investment.EquityUSD != 1000 {
		t.Fatal("unverified producer budget leaked")
	}
	cfg.BullHolder.Investment = nil
	b = fetchBullHolder(context.Background(), cfg, holderClient(t, balances, strings.Replace(holderLighter, `"unrealized_pnl":"10"`, `"unrealized_pnl":""`, 1))).Status.BullHolder
	if b.Investment != nil || b.UnrealizedPnL != nil || b.Lighter.UnrealizedPnL != nil {
		t.Fatal("partial financial values exposed as complete")
	}
	lt := strings.Replace(holderLighter, `"position":"0.25"`, `"position":"0"`, 1)
	a := fetchLighterHolder(context.Background(), holderClient(t, balances, lt), "42")
	if a.UnrealizedPnL == nil || *a.UnrealizedPnL != 0 {
		t.Fatal("flat Lighter PnL should be zero")
	}
}

func anchoredInvestment() *HolderInvestment {
	return &HolderInvestment{
		ConfigFP:     "4c67969bf10a",
		EquityUSD:    1000,
		SpotFraction: .9,
		PerpFraction: finiteHolderValue(.45),
		Anchor: &HolderAnchor{
			TS: 1788000000,
			Assets: []HolderAnchorAsset{
				{Symbol: "BTC", SpotSymbol: "UBTC", PriceUSD: 50000},
				{Symbol: "ETH", SpotSymbol: "UETH", PriceUSD: 1000},
			},
		},
	}
}

func TestHolderBenchmarkValuesTheAnchoredBookAtCurrentMarks(t *testing.T) {
	// $900 of spot split equally: $450 at 50k = 0.009 BTC, $450 at 1k =
	// 0.45 ETH. At 100k / 2k those legs are worth $900 each, and the
	// $100 the bot kept as perp margin stays cash in the benchmark, so
	// both sides start from the same $1000.
	b, msg := holderBenchmarkFrom(anchoredInvestment(), map[string]float64{"UBTC": 100000, "UETH": 2000})
	if msg != "" || b == nil {
		t.Fatalf("benchmark unavailable: %q", msg)
	}
	if b.CostUSD != 900 || b.CashUSD != 100 || math.Abs(b.EquityUSD-1900) > 1e-6 || b.AnchorTS != 1788000000 {
		t.Fatalf("wrong benchmark: %+v", b)
	}
	if len(b.Assets) != 2 || b.Assets[0].Units != 0.009 || b.Assets[1].Units != 0.45 {
		t.Fatalf("wrong benchmark legs: %+v", b.Assets)
	}
	// Leg values go through two divisions, so compare with a tolerance
	// well below the cent the card renders.
	for _, asset := range b.Assets {
		if math.Abs(asset.ValueUSD-900) > 1e-6 {
			t.Fatalf("wrong benchmark leg values: %+v", b.Assets)
		}
	}
}

func TestHolderBenchmarkNeverGuessesAMissingInput(t *testing.T) {
	marks := map[string]float64{"UBTC": 100000, "UETH": 2000}
	noAnchor := anchoredInvestment()
	noAnchor.Anchor = nil
	for _, tc := range []struct {
		name       string
		investment *HolderInvestment
		marks      map[string]float64
		want       string
	}{
		{"no verified snapshot", nil, marks, "Verified startup investment settings not configured"},
		{"no anchor", noAnchor, marks, "Buy & hold anchor not configured"},
		{"no marks at all", anchoredInvestment(), nil, "Benchmark prices unavailable"},
		// A partial benchmark would silently drop a whole leg and read as
		// the bot beating buy & hold by ~50%.
		{"one leg unpriced", anchoredInvestment(), map[string]float64{"UBTC": 100000}, "Benchmark price unavailable for ETH"},
		{"leg priced at zero", anchoredInvestment(), map[string]float64{"UBTC": 100000, "UETH": 0}, "Benchmark price unavailable for ETH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, msg := holderBenchmarkFrom(tc.investment, tc.marks)
			if b != nil || msg != tc.want {
				t.Fatalf("benchmark = %+v, msg = %q, want msg %q", b, msg, tc.want)
			}
		})
	}
}

func TestHolderBenchmarkAnchorValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		anchor HolderAnchor
	}{
		{"no timestamp", HolderAnchor{Assets: []HolderAnchorAsset{{Symbol: "BTC", PriceUSD: 1}}}},
		{"no assets", HolderAnchor{TS: 1}},
		{"unnamed asset", HolderAnchor{TS: 1, Assets: []HolderAnchorAsset{{PriceUSD: 1}}}},
		{"zero anchor price", HolderAnchor{TS: 1, Assets: []HolderAnchorAsset{{Symbol: "BTC", PriceUSD: 0}}}},
		{"duplicate leg", HolderAnchor{TS: 1, Assets: []HolderAnchorAsset{{Symbol: "BTC", PriceUSD: 1}, {Symbol: "BTC", PriceUSD: 2}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := anchoredInvestment()
			v.Anchor = &tc.anchor
			if err := v.validate(); err == nil {
				t.Fatal("invalid anchor accepted")
			}
		})
	}
}

func TestHolderBenchmarkIsDerivedNotForwardedFromTheProducer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	// The producer has no business computing this; a payload claiming a
	// benchmark must be discarded, not rendered.
	writeHolder(t, path, `{"bot":"bull_holder","ts":1,"mode":"On","dry_run":true,"config_fp":"4c67969bf10a",`+
		`"benchmark":{"anchor_ts":1,"cost_usd":1,"cash_usd":1,"equity_usd":999999},"legs":{}}`)
	cfg := TargetConfig{BullHolder: &BullHolderConfig{StatusPath: path, HLAddress: "x", LighterIndex: "42", Investment: anchoredInvestment()}}
	b := fetchBullHolder(context.Background(), cfg, holderClient(t, holderBalances, holderLighter)).Status.BullHolder
	if b.Benchmark == nil || b.BenchmarkError != "" {
		t.Fatalf("benchmark missing: %q", b.BenchmarkError)
	}
	// Fixture marks are UBTC 100000 / UETH 2000, same as the unit test.
	if math.Abs(b.Benchmark.EquityUSD-1900) > 1e-6 {
		t.Fatalf("producer value leaked or math changed: %+v", b.Benchmark)
	}

	// A fingerprint mismatch already hides the investment snapshot; the
	// benchmark is derived from it and must disappear with it.
	writeHolder(t, path, `{"bot":"bull_holder","ts":1,"mode":"On","dry_run":true,"config_fp":"000000000000","legs":{}}`)
	b = fetchBullHolder(context.Background(), cfg, holderClient(t, holderBalances, holderLighter)).Status.BullHolder
	if b.Benchmark != nil || b.BenchmarkError == "" {
		t.Fatalf("benchmark survived an unverified investment snapshot: %+v", b.Benchmark)
	}
}
