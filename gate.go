package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// An α candidate is judged by a gate that was frozen before anyone
// looked at the data (taxonomy §4.3). Showing its running PnL, equity
// curve or t-statistic is peeking, and peeking is how a pre-registered
// study stops being one. What the operator actually needs from the card
// is whether the study is still on track: how many valid samples it has
// against the number the gate requires, when the readout is, which
// frozen spec is being followed, and whether the machinery is producing
// samples at all.
//
// GateConfig is the operator-entered pre-registration: it lives in the
// dashboard config because the dashboard must be able to say "this is
// the spec I am counting against" independently of the bot.
type GateConfig struct {
	// SpecHash identifies the frozen pre-registration. When the bot also
	// reports one and the two disagree, the sample count is withheld:
	// a gate whose spec moved is not the gate that was registered.
	SpecHash string `yaml:"spec_hash" json:"spec_hash"`
	// RequiredSamples is the number of valid samples the gate needs.
	RequiredSamples int `yaml:"required_samples" json:"required_samples"`
	// ReadoutOn is the pre-registered readout date (UTC, YYYY-MM-DD).
	ReadoutOn string `yaml:"readout_on" json:"readout_on"`
	// SampleSource names where the sample count comes from, for the
	// operator reading the card: the book runtime's decision journal
	// (bot-strategy#937) for XSMOM, trading_session rows for Engine B.
	SampleSource string `yaml:"sample_source" json:"sample_source,omitempty"`
}

// GateStatus is the bot-reported half. Absent until the bot emits it, in
// which case the card shows the frozen spec and the readout date and
// says the sample count is not being reported — it never estimates one.
type GateStatus struct {
	SpecHash          string `json:"spec_hash"`
	ValidSamples      *int   `json:"valid_samples"`
	LastSampleAt      int64  `json:"last_sample_at,omitempty"`
	DecisionOnTime    *bool  `json:"decision_on_time,omitempty"`
	SignalHashMatched *bool  `json:"signal_hash_matched,omitempty"`
}

// GateProgress is what the API exposes: the frozen spec plus the derived
// progress. Deliberately carries no PnL, equity or t-statistic.
type GateProgress struct {
	GateConfig
	ValidSamples *int `json:"valid_samples,omitempty"`
	// ReadoutDue is true on and after the readout date. The operator
	// then runs the pre-registered script; the result belongs on the
	// issue, not on the dashboard.
	ReadoutDue    bool `json:"readout_due"`
	DaysToReadout *int `json:"days_to_readout,omitempty"`
	// SpecDrift is true when the bot reports a different spec hash than
	// the frozen one. The sample count is withheld while it is set.
	SpecDrift bool `json:"spec_drift"`
}

var gateSpecHashPattern = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

func (g GateConfig) validate() error {
	if !gateSpecHashPattern.MatchString(g.SpecHash) {
		return errors.New("gate.spec_hash must be 12-64 lowercase hex characters")
	}
	if g.RequiredSamples <= 0 {
		return errors.New("gate.required_samples must be positive")
	}
	if _, err := subsidyDate(g.ReadoutOn); err != nil {
		return fmt.Errorf("gate.readout_on: %w", err)
	}
	return nil
}

// resolveGate combines the frozen spec with whatever the bot reports.
// `now` is injected so the readout flip is testable.
func resolveGate(cfg *GateConfig, status *GateStatus, now time.Time) *GateProgress {
	if cfg == nil {
		return nil
	}
	progress := &GateProgress{GateConfig: *cfg}
	if readout, err := subsidyDate(cfg.ReadoutOn); err == nil {
		today := now.UTC().Truncate(24 * time.Hour)
		days := int(readout.Sub(today).Hours() / 24)
		progress.DaysToReadout = &days
		progress.ReadoutDue = days <= 0
	}
	if status == nil {
		return progress
	}
	if !strings.EqualFold(strings.TrimSpace(status.SpecHash), strings.TrimSpace(cfg.SpecHash)) {
		progress.SpecDrift = true
		return progress
	}
	progress.ValidSamples = status.ValidSamples
	return progress
}
