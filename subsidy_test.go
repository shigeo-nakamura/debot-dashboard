package main

import (
	"strings"
	"testing"
)

func TestSubsidyConfigValidation(t *testing.T) {
	value := func(v float64) *float64 { return &v }
	for _, tc := range []struct {
		name    string
		cfg     SubsidyConfig
		wantErr string
	}{
		{"minimal", SubsidyConfig{Unit: "points"}, ""},
		{"no unit", SubsidyConfig{}, "subsidy.unit is required"},
		{"blank unit", SubsidyConfig{Unit: "   "}, "subsidy.unit is required"},
		{
			// An imputed payout with no date silently ages into a
			// fabrication, so it is not accepted without one.
			"imputed value without its source date",
			SubsidyConfig{Unit: "points", ImputedUnitValueUSD: value(0.05)},
			"value_source_date is required",
		},
		{
			"priced units",
			SubsidyConfig{Unit: "points", ImputedUnitValueUSD: value(0.05), ValueSourceDate: "2026-09-08"},
			"",
		},
		{
			"zero is a legitimate assumption",
			SubsidyConfig{Unit: "points", ImputedUnitValueUSD: value(0), ValueSourceDate: "2026-09-08"},
			"",
		},
		{
			"negative payout",
			SubsidyConfig{Unit: "points", ImputedUnitValueUSD: value(-1), ValueSourceDate: "2026-09-08"},
			"non-negative",
		},
		{"malformed date", SubsidyConfig{Unit: "points", ProgramChangedOn: "2026-9-1"}, "program_changed_on"},
		{"impossible date", SubsidyConfig{Unit: "points", KPIReviewedOn: "2026-02-31"}, "kpi_reviewed_on"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestSubsidyKPIStalenessDefaultsToStaleAfterAProgramChange(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cfg      SubsidyConfig
		stale    bool
		since    string
		nilInput bool
	}{
		{name: "no configured KPI", nilInput: true},
		{name: "no program change", cfg: SubsidyConfig{Unit: "points"}},
		{
			// The venue changed the rules and nobody has re-evaluated:
			// the KPI cannot be trusted until someone does.
			name:  "changed, never reviewed",
			cfg:   SubsidyConfig{Unit: "points", ProgramChangedOn: "2026-09-01"},
			stale: true, since: "2026-09-01",
		},
		{
			name:  "reviewed before the change",
			cfg:   SubsidyConfig{Unit: "points", ProgramChangedOn: "2026-09-01", KPIReviewedOn: "2026-08-20"},
			stale: true, since: "2026-09-01",
		},
		{
			name: "reviewed after the change",
			cfg:  SubsidyConfig{Unit: "points", ProgramChangedOn: "2026-09-01", KPIReviewedOn: "2026-09-05"},
		},
		{
			name: "reviewed the same day",
			cfg:  SubsidyConfig{Unit: "points", ProgramChangedOn: "2026-09-01", KPIReviewedOn: "2026-09-01"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg *SubsidyConfig
			if !tc.nilInput {
				local := tc.cfg
				cfg = &local
			}
			kpi := resolveSubsidyKPI(cfg)
			if tc.nilInput {
				if kpi != nil {
					t.Fatal("KPI invented for a target without one")
				}
				return
			}
			if kpi.Stale != tc.stale || kpi.StaleSince != tc.since {
				t.Fatalf("stale = %v/%q, want %v/%q", kpi.Stale, kpi.StaleSince, tc.stale, tc.since)
			}
		})
	}
}

func TestSubsidyUnitsAreDroppedWhenTheDenominatorCannotBeTrusted(t *testing.T) {
	total := 1234.0
	units := &SubsidyUnits{Unit: "points", UnitsTotal: &total}
	if got := usableSubsidyUnits(units, &SubsidyConfig{Unit: "Points"}); got == nil {
		t.Fatal("case-different but identical unit rejected")
	}
	// A bot counting points on a target whose KPI is denominated in
	// activity notional would price the wrong thing with the operator's
	// conversion rate.
	if got := usableSubsidyUnits(units, &SubsidyConfig{Unit: "USD activity"}); got != nil {
		t.Fatal("mismatched unit accepted")
	}
	if got := usableSubsidyUnits(units, nil); got != nil {
		t.Fatal("units accepted for a target with no configured KPI")
	}
	if got := usableSubsidyUnits(nil, &SubsidyConfig{Unit: "points"}); got != nil {
		t.Fatal("units invented")
	}
}

func TestNormalizeConfigRejectsSubsidyKPIOutsideTheSubsidyBucket(t *testing.T) {
	cfg := Config{
		Region: "eu-central-1",
		Targets: []TargetConfig{{
			Service:  "debot-bull-holder",
			Subsidy:  &SubsidyConfig{Unit: "points"},
			S3Bucket: "b", S3Key: "k",
		}},
	}
	if err := normalizeConfig(&cfg); err == nil || !strings.Contains(err.Error(), "subsidy KPI configured for a beta target") {
		t.Fatalf("err = %v, want a bucket mismatch", err)
	}

	ok := Config{
		Region: "eu-central-1",
		Targets: []TargetConfig{{
			Service:  "arcus-spot-live-tick",
			Subsidy:  &SubsidyConfig{Unit: "USD activity"},
			S3Bucket: "b", S3Key: "k",
		}},
	}
	if err := normalizeConfig(&ok); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	bad := Config{
		Region: "eu-central-1",
		Targets: []TargetConfig{{
			Service:  "arcus-spot-live-tick",
			Subsidy:  &SubsidyConfig{},
			S3Bucket: "b", S3Key: "k",
		}},
	}
	if err := normalizeConfig(&bad); err == nil {
		t.Fatal("invalid subsidy KPI accepted at startup")
	}
}
