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
}

func (v HolderInvestment) validate() error {
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(v.ConfigFP) ||
		finiteHolderValue(v.EquityUSD) == nil || v.EquityUSD <= 0 ||
		finiteHolderValue(v.SpotFraction) == nil || v.SpotFraction <= 0 || v.SpotFraction > 1 ||
		v.PerpFraction == nil || finiteHolderValue(*v.PerpFraction) == nil || *v.PerpFraction < 0 || *v.PerpFraction > 1 {
		return errors.New("invalid bull_holder.investment startup snapshot")
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
