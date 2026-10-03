package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func arcusFixture(t *testing.T) []byte {
	t.Helper()
	payload, err := os.ReadFile("tests/fixtures/arcus-vol-status.json")
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func arcusTarget(t *testing.T, payload []byte, cfg ArcusVolConfig) TargetConfig {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	if payload != nil {
		if err := os.WriteFile(path, payload, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg.StatusPath = path
	return TargetConfig{Name: "Arcus presence", Service: "debot-arcus-vol", Region: "ap-northeast-1", ArcusVol: &cfg}
}

func f64(v float64) *float64 { return &v }

// The fixture is the live shape; every allowlisted field must survive the
// decode and the derived figures must come out of the producer's numbers.
func TestArcusVolFixtureDecodes(t *testing.T) {
	defer arcusVolTrackerForTest()()
	now := time.UnixMilli(1791055800000).Add(2 * time.Second)
	cfg := ArcusVolConfig{DailyStopUSD: f64(5), CumStopUSD: f64(50), APIKeyValidUntil: "2027-03-30T06:32:00Z"}
	r := fetchArcusVol(arcusTarget(t, arcusFixture(t), cfg), now, true)
	if r.Error != "" || r.ServiceStatus != "active" {
		t.Fatalf("error=%q status=%q", r.Error, r.ServiceStatus)
	}
	a := r.Status.ArcusVol
	if a.State != "quoting" || a.Market != "SPY-USD" || a.Mode != "live" || r.Status.DryRun {
		t.Fatalf("header: %+v dry_run=%v", a, r.Status.DryRun)
	}
	if r.Status.TS != 1791055800 || r.Status.UpdatedAt != "2026-10-03T19:30:00Z" || r.Status.Dex != "Arcus Perps" {
		t.Fatalf("ts %d updated %q dex %q", r.Status.TS, r.Status.UpdatedAt, r.Status.Dex)
	}
	if !a.MeasurementsAvailable || a.Quotes == nil || a.Quotes.Bid == nil || a.Quotes.Ask == nil {
		t.Fatal("quotes missing")
	}
	if a.Quotes.Ask.Px != 771.11 || a.Quotes.Ask.Qty != 3.24374 || *a.Quotes.Ask.DistTouchBps != 6.229 {
		t.Fatalf("ask: %+v", a.Quotes.Ask)
	}
	if got := a.Quotes.Ask.NotionalUSD; got < 2501 || got > 2502 {
		t.Fatalf("ask notional %v", got)
	}
	if a.Book == nil || a.Book.Bid != 770.62 || a.Book.Ask != 770.63 {
		t.Fatalf("book: %+v", a.Book)
	}
	if a.QuoteOffsetBps != 5 || a.RepegBandBps != 2 || a.EffectiveCapUSD != 5000 {
		t.Fatalf("config echo: %+v", a)
	}
	if a.PnL.DailyNet != -1.25 || a.PnL.CumNet != -28.8327 || a.PnL.CumFees != 3.1202 {
		t.Fatalf("pnl: %+v", a.PnL)
	}
	// Remaining before the stop: limit + net, so −1.25 of a $5 day leaves 3.75
	// and −28.83 of a $50 cumulative limit leaves 21.17.
	if *a.PnL.RemainingDaily != 3.75 || int(*a.PnL.RemainingCum*100) != 2116 {
		t.Fatalf("remaining: %v %v", *a.PnL.RemainingDaily, *a.PnL.RemainingCum)
	}
	if a.Volume.Fills != 420 || a.Volume.Cum != 201901.61 || a.MakerShareOr(0) < 0.93 || a.MakerShareOr(0) > 0.94 {
		t.Fatalf("volume: %+v", a.Volume)
	}
	if a.CostPer1MUSD == nil || *a.CostPer1MUSD != 142.81 {
		t.Fatalf("cost/1M: %v", a.CostPer1MUSD)
	}
	if a.APIKey == nil || a.APIKey.Warn || a.APIKey.Expired || a.APIKey.DaysLeft < 170 {
		t.Fatalf("api key: %+v", a.APIKey)
	}
	if a.Presence == nil || a.Presence.Samples != 1 || *a.Presence.QuotingFraction != 1 || a.Presence.Requotes != 0 {
		t.Fatalf("presence: %+v", a.Presence)
	}
	if r.KillSwitchActive == nil || *r.KillSwitchActive {
		t.Fatalf("kill switch: %v", r.KillSwitchActive)
	}
	// Order ids never reach the API.
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "87e6ea10b143486d") || strings.Contains(string(out), "order_id") {
		t.Fatal("order id leaked into the API payload")
	}
	var round TargetStatus
	if err := json.Unmarshal(out, &round); err != nil || round.Status.ArcusVol == nil || round.Status.ArcusVol.Quotes.Ask.Px != 771.11 {
		t.Fatalf("round trip: %v %+v", err, round.Status)
	}
}

func (a *ArcusVolStatus) MakerShareOr(d float64) float64 {
	if a.Volume == nil || a.Volume.MakerShare == nil {
		return d
	}
	return *a.Volume.MakerShare
}

func mutate(t *testing.T, payload []byte, edit func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatal(err)
	}
	edit(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestArcusVolStateDerivation(t *testing.T) {
	defer arcusVolTrackerForTest()()
	base := arcusFixture(t)
	now := time.UnixMilli(1791055800000).Add(2 * time.Second)
	cases := []struct {
		name  string
		edit  func(m map[string]any)
		at    time.Time
		state string
		svc   string
		kill  bool
	}{
		{"quoting", func(m map[string]any) {}, now, "quoting", "active", false},
		{"pulled", func(m map[string]any) { m["plan"] = "pull:stale_book" }, now, "pulled", "active", false},
		{"flatten", func(m map[string]any) { m["plan"] = "flatten:MaxHold" }, now, "pulled", "active", false},
		{"wait", func(m map[string]any) { m["plan"] = "wait" }, now, "pulled", "active", false},
		{"daily stop", func(m map[string]any) { m["plan"] = "pull:halt"; m["halt"] = "daily_stop" }, now, "halted", "active", false},
		{"kill switch", func(m map[string]any) { m["plan"] = "pull:halt"; m["halt"] = "kill_switch" }, now, "halted", "active", true},
		{"stale", func(m map[string]any) {}, now.Add(25 * time.Second), "stale", "stale", false},
		{"stale beats quoting", func(m map[string]any) { m["plan"] = "quote" }, now.Add(10 * time.Minute), "stale", "stale", false},
	}
	for _, c := range cases {
		r := fetchArcusVol(arcusTarget(t, mutate(t, base, c.edit), ArcusVolConfig{}), c.at, true)
		a := r.Status.ArcusVol
		if a.State != c.state || r.ServiceStatus != c.svc || r.Error != "" {
			t.Fatalf("%s: state=%q service=%q err=%q", c.name, a.State, r.ServiceStatus, r.Error)
		}
		if (r.KillSwitchActive != nil && *r.KillSwitchActive) != c.kill || r.Status.KillSwitchActive != c.kill {
			t.Fatalf("%s: kill switch %v", c.name, r.KillSwitchActive)
		}
		if c.name == "pulled" && a.PlanReason != "stale_book" {
			t.Fatalf("plan reason %q", a.PlanReason)
		}
	}
	// A configured stale window shorter than the default is honoured.
	r := fetchArcusVol(arcusTarget(t, base, ArcusVolConfig{StaleAfterSecs: 1}), now, true)
	if r.ServiceStatus != "stale" || r.StaleAfterSecs != 1 {
		t.Fatalf("custom stale window: %q %d", r.ServiceStatus, r.StaleAfterSecs)
	}
}

func TestArcusVolUnavailableAndInvalid(t *testing.T) {
	defer arcusVolTrackerForTest()()
	now := time.Now()
	r := fetchArcusVol(arcusTarget(t, nil, ArcusVolConfig{}), now, true)
	if r.Error != "Arcus-vol status unavailable" || r.ServiceStatus != "unknown" || r.Status.ArcusVol.State != "unavailable" {
		t.Fatalf("missing file: %+v", r)
	}
	for _, bad := range []string{
		`not json`,
		`{"bot":"bull_holder","ts_ms":1,"market":"SPY-USD","mode":"live"}`,
		`{"bot":"arcus_vol_runtime","ts_ms":0,"market":"SPY-USD","mode":"live"}`,
		`{"bot":"arcus_vol_runtime","ts_ms":1,"market":"","mode":"live"}`,
		`{"bot":"arcus_vol_runtime","ts_ms":1,"market":"SPY-USD","mode":"paper"}`,
	} {
		r := fetchArcusVol(arcusTarget(t, []byte(bad), ArcusVolConfig{}), now, true)
		if r.Error != "invalid arcus-vol status" || r.ServiceStatus != "unknown" || r.Status.ArcusVol.State != "unavailable" {
			t.Fatalf("%s: %+v", bad, r)
		}
		assertNoMeasurements(t, r)
	}
	assertNoMeasurements(t, fetchArcusVol(arcusTarget(t, nil, ArcusVolConfig{}), now, true))
	// Null quotes and a missing book are a normal pulled state, not an error.
	payload := mutate(t, arcusFixture(t), func(m map[string]any) {
		m["plan"] = "pull:no_book"
		m["book"] = nil
		m["quotes"] = map[string]any{"bid": nil, "ask": nil}
		m["cost_per_1m_usd"] = nil
	})
	r = fetchArcusVol(arcusTarget(t, payload, ArcusVolConfig{}), time.UnixMilli(1791055800000), true)
	a := r.Status.ArcusVol
	if r.Error != "" || a.State != "pulled" || a.Book != nil || a.Quotes == nil || a.Quotes.Bid != nil || a.Quotes.Ask != nil || a.CostPer1MUSD != nil {
		t.Fatalf("pulled with no book: err=%q %+v", r.Error, a)
	}
	if a.PnL.RemainingDaily != nil || a.PnL.DailyStopUSD != nil || a.APIKey != nil {
		t.Fatalf("unconfigured display values must stay nil: %+v %+v", a.PnL, a.APIKey)
	}
}

// A failed read publishes the failure state and a presence sample, and
// nothing that reads as a measurement: a zero PnL would be counted as a
// cost of zero by the subsidy bucket.
func assertNoMeasurements(t *testing.T, r TargetStatus) {
	t.Helper()
	a := r.Status.ArcusVol
	if a.MeasurementsAvailable || a.PnL != nil || a.Volume != nil || a.Inventory != nil || a.Quotes != nil || a.Book != nil || a.Presence == nil {
		t.Fatalf("failure state carries measurements: %+v", a)
	}
	out, err := json.Marshal(r.Status)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	av := m["arcus_vol"].(map[string]any)
	for _, k := range []string{"pnl", "volume", "inventory", "quotes", "book", "cost_per_1m_usd"} {
		if _, present := av[k]; present {
			t.Fatalf("failure JSON carries %q: %s", k, out)
		}
	}
	if av["measurements_available"] != false {
		t.Fatalf("measurements_available not false: %s", out)
	}
}

func TestArcusVolRemainingBeforeStop(t *testing.T) {
	limit := f64(5)
	if *remainingBeforeStop(limit, -1.25) != 3.75 {
		t.Fatal("loss reduces the room")
	}
	if *remainingBeforeStop(limit, 2) != 5 {
		t.Fatal("a profit does not extend the room past the limit")
	}
	if *remainingBeforeStop(limit, -7) != 0 {
		t.Fatal("past the limit floors at zero")
	}
	if remainingBeforeStop(nil, -1) != nil {
		t.Fatal("no limit, no figure")
	}
}

func TestArcusVolPresenceRingBuffer(t *testing.T) {
	tr := &arcusVolTracker{samples: map[string][]arcusVolSample{}}
	t0 := time.Unix(1_800_000_000, 0)
	window := time.Hour
	// 3 quoting samples with one ask re-quote, then a pull (ids empty) and a
	// fresh place: the pull→place transition is not a re-quote.
	seq := []arcusVolSample{
		{t0, true, "b1", "a1"},
		{t0.Add(10 * time.Second), true, "b1", "a1"},
		{t0.Add(20 * time.Second), true, "b1", "a2"},
		{t0.Add(30 * time.Second), false, "", ""},
		{t0.Add(40 * time.Second), true, "b2", "a3"},
	}
	var p ArcusVolPres
	for _, s := range seq {
		p = tr.record("k", s, window, 1000)
	}
	if p.Samples != 5 || p.Requotes != 1 || *p.QuotingFraction != 0.8 || p.CoveredSecs != 40 || p.WindowSecs != 3600 {
		t.Fatalf("presence: %+v", p)
	}
	// Samples older than the window fall out; the fraction follows.
	p = tr.record("k", arcusVolSample{t0.Add(window + 35*time.Second), true, "b2", "a3"}, window, 1000)
	if p.Samples != 2 || *p.QuotingFraction != 1 || p.Requotes != 0 {
		t.Fatalf("after window roll: %+v", p)
	}
	// The sample cap bounds memory regardless of poll rate.
	for i := 0; i < 50; i++ {
		p = tr.record("cap", arcusVolSample{t0.Add(time.Duration(i) * time.Second), true, "b", "a"}, window, 10)
	}
	if p.Samples != 10 {
		t.Fatalf("cap: %d samples", p.Samples)
	}
	// Separate targets do not share a buffer.
	if q := tr.record("other", arcusVolSample{t0, false, "", ""}, window, 10); q.Samples != 1 || *q.QuotingFraction != 0 {
		t.Fatalf("other key: %+v", q)
	}
}

// An outage between two quoting polls is time the runtime was not seen
// quoting; it must lower the fraction, not disappear from it. The ids
// seen before and after it must not pair up into a re-peg either.
func TestArcusVolFailedPollsCountAsNotQuoting(t *testing.T) {
	defer arcusVolTrackerForTest()()
	payload := arcusFixture(t)
	target := arcusTarget(t, payload, ArcusVolConfig{})
	t0 := time.UnixMilli(1791055800000).Add(time.Second)
	r := fetchArcusVol(target, t0, true)
	if *r.Status.ArcusVol.Presence.QuotingFraction != 1 {
		t.Fatalf("first poll: %+v", r.Status.ArcusVol.Presence)
	}
	// Three unreadable polls, one invalid, one stale (the file frozen).
	if err := os.Remove(target.ArcusVol.StatusPath); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		r = fetchArcusVol(target, t0.Add(time.Duration(i)*time.Second), true)
		if r.Error != "Arcus-vol status unavailable" || r.Status.ArcusVol.Presence == nil {
			t.Fatalf("unreadable poll %d: %+v", i, r)
		}
	}
	if err := os.WriteFile(target.ArcusVol.StatusPath, []byte(`{"bot":"other"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r = fetchArcusVol(target, t0.Add(4*time.Second), true)
	if r.Error != "invalid arcus-vol status" || r.Status.ArcusVol.Presence.Samples != 5 {
		t.Fatalf("invalid poll: %+v", r)
	}
	// Same ids, but the file is now stale: not quoting, and no re-peg.
	frozen := mutate(t, payload, func(m map[string]any) {
		m["quotes"].(map[string]any)["ask"].(map[string]any)["order_id"] = "zzzz"
	})
	if err := os.WriteFile(target.ArcusVol.StatusPath, frozen, 0o644); err != nil {
		t.Fatal(err)
	}
	r = fetchArcusVol(target, t0.Add(60*time.Second), true)
	if r.ServiceStatus != "stale" || r.Status.ArcusVol.State != "stale" {
		t.Fatalf("stale poll: %+v", r)
	}
	// Back to a fresh quoting status with a new ask id.
	if err := os.WriteFile(target.ArcusVol.StatusPath, mutate(t, payload, func(m map[string]any) {
		m["ts_ms"] = t0.Add(61 * time.Second).UnixMilli()
		m["quotes"].(map[string]any)["ask"].(map[string]any)["order_id"] = "new-ask"
	}), 0o644); err != nil {
		t.Fatal(err)
	}
	r = fetchArcusVol(target, t0.Add(62*time.Second), true)
	p := r.Status.ArcusVol.Presence
	// 7 polls, 2 quoting: 1 (first) + 1 (last).
	if p.Samples != 7 || *p.QuotingFraction < 0.285 || *p.QuotingFraction > 0.286 || p.Requotes != 0 {
		t.Fatalf("after outage: %+v", p)
	}
}

// Every page load asks for /api/status?range=…, which goes through the
// same fetcher. Those reads must not count as presence observations or
// reloads and extra viewers would inflate the sample count and skew the
// quoting fraction; only the scheduled poll records.
func TestArcusVolHistoryRequestsDoNotRecordPresence(t *testing.T) {
	defer arcusVolTrackerForTest()()
	payload := arcusFixture(t)
	target := arcusTarget(t, payload, ArcusVolConfig{})
	t0 := time.UnixMilli(1791055800000).Add(time.Second)
	if p := fetchArcusVol(target, t0, true).Status.ArcusVol.Presence; p.Samples != 1 {
		t.Fatalf("poll 1: %+v", p)
	}
	// Two history reads in between, one of them while the file is missing.
	if p := fetchArcusVol(target, t0.Add(time.Second), false).Status.ArcusVol.Presence; p.Samples != 1 || *p.QuotingFraction != 1 {
		t.Fatalf("history read changed the summary: %+v", p)
	}
	if err := os.Remove(target.ArcusVol.StatusPath); err != nil {
		t.Fatal(err)
	}
	r := fetchArcusVol(target, t0.Add(2*time.Second), false)
	if r.Error != "Arcus-vol status unavailable" || r.Status.ArcusVol.Presence.Samples != 1 || *r.Status.ArcusVol.Presence.QuotingFraction != 1 {
		t.Fatalf("failed history read recorded a sample: %+v", r.Status.ArcusVol.Presence)
	}
	if err := os.WriteFile(target.ArcusVol.StatusPath, mutate(t, payload, func(m map[string]any) { m["ts_ms"] = t0.Add(3 * time.Second).UnixMilli() }), 0o644); err != nil {
		t.Fatal(err)
	}
	if p := fetchArcusVol(target, t0.Add(4*time.Second), true).Status.ArcusVol.Presence; p.Samples != 2 || *p.QuotingFraction != 1 {
		t.Fatalf("poll 2: %+v", p)
	}
	// A history read still drops expired samples from the summary it shows.
	if p := fetchArcusVol(target, t0.Add(arcusVolPresenceWindow+5*time.Second), false).Status.ArcusVol.Presence; p.Samples != 0 || p.QuotingFraction != nil {
		t.Fatalf("expired samples shown: %+v", p)
	}
}

func TestArcusVolAPIKeyWarning(t *testing.T) {
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	ok := arcusVolAPIKey("2027-03-30T06:32:00Z", now)
	if ok == nil || ok.Warn || ok.Expired || ok.DaysLeft < 178 || ok.DaysLeft > 179 {
		t.Fatalf("far expiry: %+v", ok)
	}
	warn := arcusVolAPIKey("2026-10-20T00:00:00Z", now)
	if warn == nil || !warn.Warn || warn.Expired || warn.DaysLeft != 17 {
		t.Fatalf("near expiry: %+v", warn)
	}
	gone := arcusVolAPIKey("2026-10-01T00:00:00Z", now)
	if gone == nil || !gone.Warn || !gone.Expired || gone.DaysLeft != -2 {
		t.Fatalf("expired: %+v", gone)
	}
	if arcusVolAPIKey("", now) != nil || arcusVolAPIKey("tomorrow", now) != nil {
		t.Fatal("unset or unparsable expiry renders as unknown")
	}
}

func TestArcusVolConfigValidation(t *testing.T) {
	for _, target := range []TargetConfig{
		{ArcusVol: &ArcusVolConfig{StatusPath: "relative"}},
		{ArcusVol: &ArcusVolConfig{StatusPath: "/tmp/status", DailyStopUSD: f64(0)}},
		{ArcusVol: &ArcusVolConfig{StatusPath: "/tmp/status", CumStopUSD: f64(-1)}},
		{ArcusVol: &ArcusVolConfig{StatusPath: "/tmp/status", APIKeyValidUntil: "2027-03-30"}},
		{ArcusVol: &ArcusVolConfig{StatusPath: "/tmp/status", StaleAfterSecs: -1}},
		{ArcusVol: &ArcusVolConfig{StatusPath: "/tmp/status"}, S3Bucket: "bucket", S3Key: "key"},
		{ArcusVol: &ArcusVolConfig{StatusPath: "/tmp/status"}, BullHolder: &BullHolderConfig{StatusPath: "/tmp/other"}},
	} {
		target.Service = "debot-arcus-vol"
		target.Region = "ap-northeast-1"
		if normalizeConfig(&Config{Targets: []TargetConfig{target}}) == nil {
			t.Fatalf("invalid config accepted: %+v", target.ArcusVol)
		}
	}
	both := Config{Region: "ap-northeast-1", Targets: []TargetConfig{{
		Service: "debot-arcus-vol", ArcusVol: &ArcusVolConfig{StatusPath: "/tmp/status"}, BullHolder: &BullHolderConfig{StatusPath: "/tmp/other"},
	}}}
	if err := normalizeConfig(&both); err == nil || !strings.Contains(err.Error(), "bull_holder or arcus_vol") {
		t.Fatalf("two local sources accepted or misreported: %v", err)
	}
	valid := Config{Region: "ap-northeast-1", Targets: []TargetConfig{{
		Service:  "debot-arcus-vol",
		Bucket:   BucketSubsidy,
		ArcusVol: &ArcusVolConfig{StatusPath: "/var/lib/debot-arcus-vol/state/status.json", DailyStopUSD: f64(5), CumStopUSD: f64(21), APIKeyValidUntil: "2027-03-30T06:32:00Z"},
	}}}
	if err := normalizeConfig(&valid); err != nil {
		t.Fatal(err)
	}
	if valid.Targets[0].Bucket != BucketSubsidy || valid.Targets[0].Name != "debot-arcus-vol" {
		t.Fatalf("normalised target: %+v", valid.Targets[0])
	}
}

// The deployed config does not mention arcus_vol; it must load exactly as
// before (the dashboard restarts on every merge against that file).
func TestConfigWithoutArcusVolLoadsUnchanged(t *testing.T) {
	cfg := Config{Region: "eu-central-1", Targets: []TargetConfig{
		{Service: "debot-xvenue-hedge-holder", S3Bucket: "b", S3Key: "k"},
		{Service: "debot-bull-holder", BullHolder: &BullHolderConfig{StatusPath: "/opt/debot-bull-holder/bull_holder/status.json"}},
		{Service: "engine-b-live", S3Bucket: "b", S3Key: "k"},
	}}
	if err := normalizeConfig(&cfg); err != nil {
		t.Fatalf("existing config shape rejected: %v", err)
	}
	for i, target := range cfg.Targets {
		if target.ArcusVol != nil {
			t.Fatalf("targets[%d] grew an arcus_vol block", i)
		}
	}
	if cfg.Targets[0].Bucket != BucketSubsidy || cfg.Targets[1].Bucket != BucketBeta || cfg.Targets[2].Bucket != BucketAlphaCandidate {
		t.Fatalf("buckets changed: %+v", cfg.Targets)
	}
	// And the YAML path: the example config still loads.
	example, err := loadConfig("config.example.yaml")
	if err != nil {
		t.Fatalf("config.example.yaml: %v", err)
	}
	if len(example.Targets) == 0 {
		t.Fatal("example config has no targets")
	}
}
