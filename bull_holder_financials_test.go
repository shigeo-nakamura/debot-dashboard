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
		a := fetchHLSpot(context.Background(), holderClient(t, balances, holderLighter), "x")
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
	a := fetchHLSpot(context.Background(), holderClient(t, `{"balances":[]}`, holderLighter), "x")
	if a.UnrealizedPnL == nil || *a.UnrealizedPnL != 0 {
		t.Fatal("verified empty account should have zero PnL")
	}
	a = fetchHLSpot(context.Background(), holderClient(t, `{}`, holderLighter), "x")
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
