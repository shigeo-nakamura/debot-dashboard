package main

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// A subsidy bot's PnL is negative by design: it pays fees, slippage and
// adverse selection to earn something a venue is deliberately handing
// out (points, qualifying activity). Judging it on PnL invites tuning a
// signal that has no edge to begin with — 0/362 pairs on Arcus
// (bot-strategy#935) — so the KPI is cost per unit of subsidy earned
// (bot-strategy#938, taxonomy §4.2).
//
// SubsidyConfig is the operator-entered half of that KPI: what a unit is
// called, what one is currently assumed to be worth (with the date that
// assumption was sourced), and whether the venue has changed the program
// since the KPI was last re-evaluated.
type SubsidyConfig struct {
	// Unit names what is being bought, e.g. "points" or "USD activity".
	// When the bot also reports a unit, the two must agree or the units
	// are not rendered: a KPI denominated in the wrong unit is worse
	// than no KPI.
	Unit string `yaml:"unit" json:"unit"`
	// ImputedUnitValueUSD is the operator's current assumption for what
	// one unit is worth. Optional: the KPI's job is to price the cost,
	// and an unknown payout is a legitimate state (bot-strategy#938
	// keeps a $0 / conservative / median sensitivity table).
	ImputedUnitValueUSD *float64 `yaml:"imputed_unit_value_usd" json:"imputed_unit_value_usd,omitempty"`
	// ValueSourceDate is when ImputedUnitValueUSD was sourced. Required
	// with it, and rendered next to it — an imputed value with no date
	// silently ages into a fabrication.
	ValueSourceDate string `yaml:"value_source_date" json:"value_source_date,omitempty"`
	// ProgramChangedOn / KPIReviewedOn are operator-entered events. A
	// program change (points weights, activity rules) invalidates the
	// KPI until someone re-evaluates it, because the edge lives at the
	// venue's discretion and disappears on its schedule.
	ProgramChangedOn string `yaml:"program_changed_on" json:"program_changed_on,omitempty"`
	KPIReviewedOn    string `yaml:"kpi_reviewed_on" json:"kpi_reviewed_on,omitempty"`
}

// SubsidyKPI is what the API exposes per target: the configuration above
// plus the staleness verdict derived from it.
type SubsidyKPI struct {
	SubsidyConfig
	// Stale is true while the program has changed more recently than the
	// KPI was reviewed. StaleSince is the change date.
	Stale      bool   `json:"stale"`
	StaleSince string `json:"stale_since,omitempty"`
}

// SubsidyUnits is the bot-reported half: how many units have been earned
// and what they cost. Emitted by the daily subsidy ledger from
// bot-strategy#938; absent until that lands, in which case the card
// renders "-" for the KPI rather than inventing a denominator.
//
// Costs are positive when money was given up, so "cost per unit" reads
// as a price paid. A bot that came out ahead reports a negative cost.
type SubsidyUnits struct {
	Unit         string   `json:"unit"`
	UnitsTotal   *float64 `json:"units_total"`
	Units7d      *float64 `json:"units_7d,omitempty"`
	CostTotalUSD *float64 `json:"cost_total_usd,omitempty"`
	Cost7dUSD    *float64 `json:"cost_7d_usd,omitempty"`
	AsOfTS       int64    `json:"as_of_ts,omitempty"`
}

const subsidyDateLayout = "2006-01-02"

func subsidyDate(value string) (time.Time, error) {
	parsed, err := time.Parse(subsidyDateLayout, value)
	if err != nil || parsed.Format(subsidyDateLayout) != value {
		return time.Time{}, fmt.Errorf("invalid date %q (want YYYY-MM-DD)", value)
	}
	return parsed, nil
}

func (c SubsidyConfig) validate() error {
	if strings.TrimSpace(c.Unit) == "" {
		return errors.New("subsidy.unit is required")
	}
	if c.ImputedUnitValueUSD != nil {
		v := *c.ImputedUnitValueUSD
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return errors.New("subsidy.imputed_unit_value_usd must be a non-negative number")
		}
		if c.ValueSourceDate == "" {
			return errors.New("subsidy.value_source_date is required with imputed_unit_value_usd")
		}
	}
	for name, value := range map[string]string{
		"value_source_date":  c.ValueSourceDate,
		"program_changed_on": c.ProgramChangedOn,
		"kpi_reviewed_on":    c.KPIReviewedOn,
	} {
		if value == "" {
			continue
		}
		if _, err := subsidyDate(value); err != nil {
			return fmt.Errorf("subsidy.%s: %w", name, err)
		}
	}
	return nil
}

// resolveSubsidyKPI derives the staleness verdict. A program change with
// no later review leaves the KPI stale; that is the default rather than
// the exception, since re-evaluating is an explicit operator act.
func resolveSubsidyKPI(c *SubsidyConfig) *SubsidyKPI {
	if c == nil {
		return nil
	}
	kpi := &SubsidyKPI{SubsidyConfig: *c}
	if c.ProgramChangedOn == "" {
		return kpi
	}
	changed, err := subsidyDate(c.ProgramChangedOn)
	if err != nil {
		return kpi
	}
	reviewed, err := subsidyDate(c.KPIReviewedOn)
	if err != nil || reviewed.Before(changed) {
		kpi.Stale = true
		kpi.StaleSince = c.ProgramChangedOn
	}
	return kpi
}

// usableSubsidyUnits drops a bot-reported ledger the dashboard cannot
// safely denominate: one on a target with no configured KPI, or one
// counting a different unit than the operator declared. A points figure
// rendered under an "activity notional" label, or priced with the wrong
// imputed value, is a wrong number presented as a measurement.
func usableSubsidyUnits(units *SubsidyUnits, cfg *SubsidyConfig) *SubsidyUnits {
	if units == nil {
		return nil
	}
	if cfg == nil {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(units.Unit), strings.TrimSpace(cfg.Unit)) {
		return nil
	}
	return units
}
