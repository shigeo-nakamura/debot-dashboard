package main

import (
	"strings"
	"testing"
	"time"
)

func gateConfig() GateConfig {
	return GateConfig{SpecHash: "a1b2c3d4e5f6", RequiredSamples: 60, ReadoutOn: "2026-10-02"}
}

func TestGateConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*GateConfig)
		wantErr string
	}{
		{"valid", func(*GateConfig) {}, ""},
		{"no spec hash", func(g *GateConfig) { g.SpecHash = "" }, "spec_hash"},
		{"short spec hash", func(g *GateConfig) { g.SpecHash = "a1b2c3" }, "spec_hash"},
		{"uppercase spec hash", func(g *GateConfig) { g.SpecHash = "A1B2C3D4E5F6" }, "spec_hash"},
		{"no sample requirement", func(g *GateConfig) { g.RequiredSamples = 0 }, "required_samples"},
		{"negative sample requirement", func(g *GateConfig) { g.RequiredSamples = -1 }, "required_samples"},
		{"malformed readout date", func(g *GateConfig) { g.ReadoutOn = "2026/10/02" }, "readout_on"},
		{"missing readout date", func(g *GateConfig) { g.ReadoutOn = "" }, "readout_on"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := gateConfig()
			tc.mutate(&cfg)
			err := cfg.validate()
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

func TestGateProgressCountsDownToTheReadoutAndFlipsOnTheDay(t *testing.T) {
	cfg := gateConfig()
	for _, tc := range []struct {
		now  string
		days int
		due  bool
	}{
		{"2026-09-08T23:59:59Z", 24, false},
		{"2026-10-01T00:00:00Z", 1, false},
		// The readout is due for the whole of its own day, not from the
		// instant after midnight passes.
		{"2026-10-02T00:00:00Z", 0, true},
		{"2026-10-02T23:00:00Z", 0, true},
		{"2026-10-05T12:00:00Z", -3, true},
	} {
		t.Run(tc.now, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, tc.now)
			if err != nil {
				t.Fatal(err)
			}
			progress := resolveGate(&cfg, nil, now)
			if progress.DaysToReadout == nil || *progress.DaysToReadout != tc.days || progress.ReadoutDue != tc.due {
				t.Fatalf("days = %v, due = %v, want %d/%v", progress.DaysToReadout, progress.ReadoutDue, tc.days, tc.due)
			}
			// Without a bot-reported gate there is no sample count, and
			// the dashboard does not estimate one.
			if progress.ValidSamples != nil {
				t.Fatal("sample count invented")
			}
		})
	}
	if resolveGate(nil, &GateStatus{}, time.Now()) != nil {
		t.Fatal("gate invented for a target without a pre-registration")
	}
}

func TestGateSamplesAreWithheldWhenTheFrozenSpecMoved(t *testing.T) {
	cfg := gateConfig()
	samples := 42
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)

	matched := resolveGate(&cfg, &GateStatus{SpecHash: "a1b2c3d4e5f6", ValidSamples: &samples}, now)
	if matched.SpecDrift || matched.ValidSamples == nil || *matched.ValidSamples != 42 {
		t.Fatalf("matching spec rejected: %+v", matched)
	}

	// A different spec hash means the study running is not the study
	// that was registered; counting its samples against the frozen gate
	// would launder a changed hypothesis into a pre-registered result.
	drifted := resolveGate(&cfg, &GateStatus{SpecHash: "ffffffffffff", ValidSamples: &samples}, now)
	if !drifted.SpecDrift || drifted.ValidSamples != nil {
		t.Fatalf("drifted spec accepted: %+v", drifted)
	}
}

func TestNormalizeConfigRejectsGateOutsideTheAlphaCandidateBucket(t *testing.T) {
	cfg := gateConfig()
	bad := Config{
		Region:  "eu-central-1",
		Targets: []TargetConfig{{Service: "arcus-spot-live-tick", Gate: &cfg, S3Bucket: "b", S3Key: "k"}},
	}
	if err := normalizeConfig(&bad); err == nil || !strings.Contains(err.Error(), "gate configured for a subsidy target") {
		t.Fatalf("err = %v, want a bucket mismatch", err)
	}

	ok := Config{
		Region:  "eu-central-1",
		Targets: []TargetConfig{{Service: "book-runtime-xsmom-695", Gate: &cfg, S3Bucket: "b", S3Key: "k"}},
	}
	if err := normalizeConfig(&ok); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	invalid := gateConfig()
	invalid.RequiredSamples = 0
	rejected := Config{
		Region:  "eu-central-1",
		Targets: []TargetConfig{{Service: "book-runtime-xsmom-695", Gate: &invalid, S3Bucket: "b", S3Key: "k"}},
	}
	if err := normalizeConfig(&rejected); err == nil {
		t.Fatal("invalid gate accepted at startup")
	}
}

func TestGateProgressFlagsAnOverdueSample(t *testing.T) {
	cfg := gateConfig()
	samples := 61
	due := time.Date(2026, 9, 8, 1, 30, 0, 0, time.UTC).Unix()
	status := func() *GateStatus {
		return &GateStatus{
			SpecHash:          cfg.SpecHash,
			ValidSamples:      &samples,
			NextSampleDueAt:   due,
			SampleCadenceSecs: 86400,
		}
	}
	at := func(s string) time.Time {
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("bad time %q: %v", s, err)
		}
		return ts
	}

	// On the producer's own deadline the sample is still on time.
	if got := resolveGate(&cfg, status(), at("2026-09-08T01:30:00Z")); got.SampleOverdue {
		t.Fatalf("sample_overdue at the deadline itself")
	}
	overdue := resolveGate(&cfg, status(), at("2026-09-08T01:30:01Z"))
	if !overdue.SampleOverdue {
		t.Fatalf("sample_overdue = false past the deadline")
	}
	// The deadline is echoed so the card can say when it was expected,
	// and the sample count is not withheld: an overdue mark is a gap in
	// the series, not a reason to hide what the study did collect.
	if overdue.NextSampleDueAt != due || overdue.SampleCadenceSecs != 86400 {
		t.Fatalf("deadline not echoed: %+v", overdue)
	}
	if overdue.ValidSamples == nil || *overdue.ValidSamples != samples {
		t.Fatalf("valid samples withheld: %+v", overdue)
	}

	// Past the readout the study is done accumulating; a deadline that
	// keeps sliding by is not a problem to report -- and is not echoed
	// either, or the card would go on saying "next sample due" beside a
	// health state that is deliberately not overdue, forever.
	after := resolveGate(&cfg, status(), at("2026-10-02T12:00:00Z"))
	if after.SampleOverdue {
		t.Fatalf("sample_overdue after the readout")
	}
	if after.NextSampleDueAt != 0 || after.SampleCadenceSecs != 0 {
		t.Fatalf("deadline still echoed after the readout: %+v", after)
	}
	// What the study did collect is still reported: the readout is the
	// point of the count, not a reason to hide it.
	if after.ValidSamples == nil || *after.ValidSamples != samples {
		t.Fatalf("valid samples withheld after the readout: %+v", after)
	}

	// A producer that declares no cadence gets no verdict either way.
	silent := status()
	silent.NextSampleDueAt = 0
	if got := resolveGate(&cfg, silent, at("2027-01-01T00:00:00Z")); got.SampleOverdue {
		t.Fatalf("sample_overdue without a declared deadline")
	}

	// A drifted spec withholds everything, including the deadline.
	drifted := status()
	drifted.SpecHash = "ffffffffffff"
	got := resolveGate(&cfg, drifted, at("2026-09-08T02:00:00Z"))
	if !got.SpecDrift || got.SampleOverdue || got.NextSampleDueAt != 0 || got.ValidSamples != nil {
		t.Fatalf("drifted gate leaked state: %+v", got)
	}
}
