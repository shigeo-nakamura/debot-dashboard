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
	b, msg := holderBenchmarkFrom(anchoredInvestment(), map[string]float64{"UBTC": 100000, "UETH": 2000}, nil, nil)
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
			b, msg := holderBenchmarkFrom(tc.investment, tc.marks, nil, nil)
			if b != nil || msg != tc.want {
				t.Fatalf("benchmark = %+v, msg = %q, want msg %q", b, msg, tc.want)
			}
		})
	}
}

func TestHolderBenchmarkRequiresTheAnchorToDescribeTheWholeBook(t *testing.T) {
	marks := map[string]float64{"UBTC": 100000, "UETH": 2000}
	legs := map[string]struct{}{"BTC": {}, "ETH": {}}
	if _, msg := holderBenchmarkFrom(anchoredInvestment(), marks, legs, nil); msg != "" {
		t.Fatalf("matching book rejected: %q", msg)
	}

	// An anchor missing a leg does not produce a partial benchmark: the
	// whole spot allocation is split across the legs it does list, so the
	// missing leg's budget is spent on the others. That is a different
	// portfolio, and it beats or loses to the bot by the spread between
	// the two legs.
	oneLeg := anchoredInvestment()
	oneLeg.Anchor.Assets = oneLeg.Anchor.Assets[:1]
	if b, msg := holderBenchmarkFrom(oneLeg, marks, legs, nil); b != nil || msg != "Benchmark anchor does not match the book's legs" {
		t.Fatalf("short anchor accepted: %+v %q", b, msg)
	}

	// A leg the bot does not trade is the same problem mirrored.
	if b, msg := holderBenchmarkFrom(anchoredInvestment(), marks, map[string]struct{}{"BTC": {}, "SOL": {}}, nil); b != nil || msg == "" {
		t.Fatalf("mismatched book accepted: %+v %q", b, msg)
	}

	// Before the bot arms it reports no legs, so there is nothing to
	// check the anchor against and the benchmark still stands.
	if _, msg := holderBenchmarkFrom(anchoredInvestment(), marks, map[string]struct{}{}, nil); msg != "" {
		t.Fatalf("benchmark suppressed for an unarmed bot: %q", msg)
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
		{"zero units", HolderAnchor{TS: 1, Assets: []HolderAnchorAsset{{Symbol: "BTC", PriceUSD: 1, Units: finiteHolderValue(0)}}}},
		// Half the legs sized from the bot's real book and half from the
		// declared allocation is two benchmarks added together.
		{"units on only one leg", HolderAnchor{TS: 1, Assets: []HolderAnchorAsset{
			{Symbol: "BTC", PriceUSD: 1, Units: finiteHolderValue(1)},
			{Symbol: "ETH", PriceUSD: 1},
		}}},
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

func TestHolderBenchmarkStartsFromTheFundedAccountNotTheDeclaredCapital(t *testing.T) {
	marks := map[string]float64{"UBTC": 100000, "UETH": 2000}

	// The live case that produced this fix: the strategy declares $1000
	// while the accounts hold $1301, and the card compares the benchmark
	// against those live accounts. Sizing the benchmark from the declared
	// capital reported the $301 difference as a 31% outperformance.
	funded := anchoredInvestment()
	funded.Anchor.FundedUSD = finiteHolderValue(1301)
	b, msg := holderBenchmarkFrom(funded, marks, nil, nil)
	if msg != "" {
		t.Fatalf("benchmark unavailable: %q", msg)
	}
	if b.FundedUSD != 1301 || b.CostUSD != 900 || b.CashUSD != 401 {
		t.Fatalf("wrong capital split: %+v", b)
	}
	// $900 of spot doubles to $1800, plus $401 that never left cash.
	if math.Abs(b.EquityUSD-2201) > 1e-6 {
		t.Fatalf("benchmark equity = %v, want 2201", b.EquityUSD)
	}

	// Omitted: an account funded at exactly the declared capital is the
	// case the field exists to distinguish from, and must not change.
	plain, msg := holderBenchmarkFrom(anchoredInvestment(), marks, nil, nil)
	if msg != "" || plain.FundedUSD != 1000 || plain.CashUSD != 100 {
		t.Fatalf("fallback changed: %+v %q", plain, msg)
	}

	// Less funded than the strategy deploys as spot: filling the gap
	// would make the benchmark levered, which is the one thing it must
	// never be.
	short := anchoredInvestment()
	short.Anchor.FundedUSD = finiteHolderValue(500)
	if b, msg := holderBenchmarkFrom(short, marks, nil, nil); b != nil || msg != "Funded capital is below the anchored spot allocation" {
		t.Fatalf("under-funded anchor accepted: %+v %q", b, msg)
	}

	for _, bad := range []float64{0, -1} {
		v := anchoredInvestment()
		v.Anchor.FundedUSD = finiteHolderValue(bad)
		if err := v.validate(); err == nil {
			t.Fatalf("funded_usd %v accepted", bad)
		}
	}
}

func TestHolderBookPrefersTheConfiguredUniverseOverOpenLegs(t *testing.T) {
	// Before ARM the producer reports its configured book but no legs.
	// This is the window a misconfigured anchor sits unnoticed in, so it
	// is exactly where the leg check has to work (bot-strategy#963).
	preArm := &BullHolderStatus{ConfiguredSymbols: []string{"BTC", "ETH"}}
	marks := map[string]float64{"UBTC": 100000, "UETH": 2000}
	if _, msg := holderBenchmarkFrom(anchoredInvestment(), marks, holderBook(preArm), nil); msg != "" {
		t.Fatalf("matching pre-ARM book rejected: %q", msg)
	}
	short := anchoredInvestment()
	short.Anchor.Assets = short.Anchor.Assets[:1]
	if _, msg := holderBenchmarkFrom(short, marks, holderBook(preArm), nil); msg == "" {
		t.Fatal("a one-leg anchor passed against a two-leg configured book")
	}

	// The configured universe wins over the legs that happen to be open,
	// so a half-deployed book does not narrow what the anchor must cover.
	half := &BullHolderStatus{
		ConfiguredSymbols: []string{"BTC", "ETH"},
		Legs:              map[string]BullHolderLeg{"BTC": {}},
	}
	if _, msg := holderBenchmarkFrom(short, marks, holderBook(half), nil); msg == "" {
		t.Fatal("a one-leg anchor passed against a partially deployed two-leg book")
	}

	// A producer that does not report the universe yet keeps the old
	// behaviour: check against whatever legs are open.
	legacy := &BullHolderStatus{Legs: map[string]BullHolderLeg{"BTC": {}, "ETH": {}}}
	if got := len(holderBook(legacy)); got != 2 {
		t.Fatalf("legacy book size = %d, want 2", got)
	}
	if got := len(holderBook(&BullHolderStatus{})); got != 0 {
		t.Fatalf("empty book size = %d, want 0", got)
	}
	// Blank entries are not a leg the anchor has to name.
	if got := len(holderBook(&BullHolderStatus{ConfiguredSymbols: []string{"BTC", ""}})); got != 1 {
		t.Fatalf("book size = %d, want 1", got)
	}
}

// The entry ladder is what makes explicit quantities necessary: the bot
// buys `BULL_HOLDER_ENTRY_TRANCHES` times at whatever each day's price
// is, so the book it ends up holding is not the declared allocation
// divided by any single price (Codex, PR #51).
func TestHolderBenchmarkHoldsTheAnchoredQuantitiesWhenGiven(t *testing.T) {
	v := anchoredInvestment()
	// The legs are deliberately NOT equal-weight at the anchor: $1000 of
	// BTC against $200 of ETH. An equal split of the same $1200 would
	// derive 0.012 BTC / 0.6 ETH, so a benchmark that ignored the
	// explicit quantities would still price out to the same cost and
	// pass every total below — the asymmetry is what makes this test
	// able to fail.
	v.Anchor.Assets[0].Units = finiteHolderValue(0.02) // $1000 at 50k
	v.Anchor.Assets[1].Units = finiteHolderValue(0.2)  // $200 at 1k
	v.Anchor.FundedUSD = finiteHolderValue(1301)

	b, msg := holderBenchmarkFrom(v, map[string]float64{"UBTC": 100000, "UETH": 2000}, nil, nil)
	if msg != "" || b == nil {
		t.Fatalf("benchmark unavailable: %q", msg)
	}
	if b.Assets[0].Units != 0.02 || b.Assets[1].Units != 0.2 {
		t.Fatalf("anchored quantities not used: %+v", b.Assets)
	}
	if math.Abs(b.CostUSD-1200) > 1e-6 || math.Abs(b.CashUSD-101) > 1e-6 {
		t.Fatalf("wrong cost/cash split: %+v", b)
	}
	// Both marks doubled: $2000 of BTC and $400 of ETH, plus $101 cash.
	if math.Abs(b.EquityUSD-(2000+400+101)) > 1e-6 {
		t.Fatalf("wrong benchmark equity: %+v", b)
	}
}

func TestHolderBenchmarkRejectsAnchoredQuantitiesAboveTheFundedAccount(t *testing.T) {
	v := anchoredInvestment()
	// $1200 of spot at the anchor against $1000 funded: paying for the
	// gap would make the benchmark levered, the one thing it must not be.
	v.Anchor.Assets[0].Units = finiteHolderValue(0.02)
	v.Anchor.Assets[1].Units = finiteHolderValue(0.2)
	v.Anchor.FundedUSD = finiteHolderValue(1000)

	if b, msg := holderBenchmarkFrom(v, map[string]float64{"UBTC": 100000, "UETH": 2000}, nil, nil); b != nil || msg == "" {
		t.Fatalf("levered benchmark accepted: %+v", b)
	}
}

// funded_usd is captured from account equity, which sums every priced
// spot holding. Anything in there that is not one of the benchmark's legs
// is carried by the benchmark as cash and marked to market on the bot's
// side, so it is published as excess -- and the first reading still looks
// right (Codex, PR #51).
func TestHolderBenchmarkSuppressedWhenSpotHoldsSomethingOutsideTheBook(t *testing.T) {
	marks := map[string]float64{"UBTC": 100000, "UETH": 2000}
	book := []HolderAsset{{Symbol: "UBTC", Size: 0.009}, {Symbol: "UETH", Size: 0.45}}

	if _, msg := holderBenchmarkFrom(anchoredInvestment(), marks, nil, book); msg != "" {
		t.Fatalf("the book's own legs were rejected: %q", msg)
	}
	// The anchor names its legs BTC/ETH and prices them as UBTC/UETH, so
	// both spellings have to count as known.
	if _, msg := holderBenchmarkFrom(anchoredInvestment(), marks, nil, []HolderAsset{{Symbol: "BTC", Size: 1}}); msg != "" {
		t.Fatalf("leg name rejected: %q", msg)
	}

	strayed := append(append([]HolderAsset{}, book...), HolderAsset{Symbol: "HYPE", Size: 3})
	b, msg := holderBenchmarkFrom(anchoredInvestment(), marks, nil, strayed)
	if b != nil || msg != "Hyperliquid account holds HYPE outside the benchmark book" {
		t.Fatalf("stray holding accepted: %+v %q", b, msg)
	}
}
