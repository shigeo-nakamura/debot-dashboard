package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const llpMonitorFixturePath = "tests/fixtures/llp-monitor-status-v1.json"

func TestLLPMonitorFixtureDecodes(t *testing.T) {
	payload, err := os.ReadFile(llpMonitorFixturePath)
	if err != nil {
		t.Fatalf("read llp-monitor fixture: %v", err)
	}
	status, err := decodeTargetPayload(TargetConfig{Service: llpMonitorService}, payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// `t` is the freshness timestamp: without it the target would read
	// as "unknown" instead of active/stale.
	if status.TS != 1791916499 || status.UpdatedAt != "2026-10-13T18:34:59Z" {
		t.Fatalf("ts/updated_at = %d/%q, want 1791916499/2026-10-13T18:34:59Z", status.TS, status.UpdatedAt)
	}
	m := status.LLPMonitor
	if m == nil {
		t.Fatal("llp_monitor missing")
	}
	if m.Level != "OK" || len(m.Fired) != 0 || len(m.Errors) != 0 || m.Notify != nil {
		t.Fatalf("level/fired/errors/notify = %q/%v/%v/%v", m.Level, m.Fired, m.Errors, m.Notify)
	}
	if m.LLP == nil || m.LLP.R30Ann == nil || *m.LLP.R30Ann != 0.207303 || m.LLP.ApyVenue == nil {
		t.Fatalf("llp = %+v", m.LLP)
	}
	if m.LIT == nil || m.LIT.Price == nil || *m.LIT.Price != 3.3696 || m.LIT.FundingHours == nil || *m.LIT.FundingHours != 168 {
		t.Fatalf("lit = %+v", m.LIT)
	}
	p := m.Position
	if p == nil || p.ShortLIT == nil || *p.ShortLIT != 260 || p.LiqPrice == nil || p.LLPPnlUsd == nil || p.HedgePnlUsd == nil {
		t.Fatalf("position = %+v", p)
	}
	if len(m.Weekly) != 1 || m.Weekly[0].Week != "2026-W42" || m.Weekly[0].Position == nil {
		t.Fatalf("weekly = %+v", m.Weekly)
	}
	if m.FailRuns["funding"] != 0 || len(m.FailRuns) != 4 {
		t.Fatalf("fail_runs = %v", m.FailRuns)
	}
	// Not a bot: none of the trading fields are filled in.
	if status.PnlTotal != 0 || status.PositionCount != 0 || status.Positions != nil || status.Accumulator != nil {
		t.Fatalf("trading fields set: %+v", status)
	}
}

// The monitor publishes null for the position (no owner account yet, or its
// read failed) and for the pool block (pool read failed), and leaves a
// return out entirely when the daily series is too short. All of those must
// decode, and reach the browser as null / absent rather than zero.
func TestLLPMonitorNullAndMissingFields(t *testing.T) {
	payload := []byte(`{
		"t": "2026-10-13T19:07:02Z",
		"level": "alert",
		"fired": [{"rule": "R5", "level": "ALERT", "msg": "Lighter API failing for >= 3 consecutive runs: pool (3)"}],
		"errors": ["pool: URLError: timed out"],
		"notify": "sent",
		"llp": null,
		"tvl_change": {"value": null, "full_7d": false},
		"live_proxy": null,
		"lit": {"price": 3.31, "funding_short_7d_ann": null, "funding_hours": 0},
		"position": null,
		"fail_runs": {"pool": 3}
	}`)
	status, err := decodeTargetPayload(TargetConfig{Service: llpMonitorService}, payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	m := status.LLPMonitor
	if m.Level != "ALERT" {
		t.Fatalf("level = %q, want ALERT (normalised)", m.Level)
	}
	if m.LLP != nil || m.Position != nil || m.LiveProxy != nil || m.Weekly != nil {
		t.Fatalf("null blocks decoded as values: %+v", m)
	}
	if m.LIT.FundingShort7dAnn != nil || m.TVLChange.Value != nil {
		t.Fatalf("null figures decoded as zero: %+v / %+v", m.LIT, m.TVLChange)
	}
	if len(m.Fired) != 1 || m.Fired[0].Rule != "R5" || len(m.Errors) != 1 || m.FailRuns["pool"] != 3 {
		t.Fatalf("fired/errors/fail_runs = %+v / %v / %v", m.Fired, m.Errors, m.FailRuns)
	}

	// A minimal document: no lists at all, a pool block missing its
	// returns. Lists come back empty, the returns stay absent.
	minimal, err := decodeLLPMonitorPayload([]byte(`{"t":"2026-10-13T20:07:00Z","level":"OK","llp":{"tvl":87000000}}`))
	if err != nil {
		t.Fatalf("decode minimal: %v", err)
	}
	mm := minimal.LLPMonitor
	if mm.Fired == nil || mm.Errors == nil || len(mm.Fired) != 0 || len(mm.Errors) != 0 {
		t.Fatalf("fired/errors = %#v / %#v, want empty lists", mm.Fired, mm.Errors)
	}
	if mm.LLP.R30Ann != nil || mm.LLP.R1d != nil || mm.LLP.Drawdown != nil || mm.LLP.TVL == nil {
		t.Fatalf("llp = %+v", mm.LLP)
	}

	// Through the API: the browser gets llp_monitor with nulls kept and
	// empty lists as [] — "nothing firing" is a reading, not a gap.
	out, err := json.Marshal(minimal)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	block, ok := raw["llp_monitor"].(map[string]any)
	if !ok {
		t.Fatalf("llp_monitor missing from %s", out)
	}
	if v, present := block["position"]; !present || v != nil {
		t.Fatalf("position = %v (present %v), want null", v, present)
	}
	if fired, ok := block["fired"].([]any); !ok || len(fired) != 0 {
		t.Fatalf("fired = %#v, want []", block["fired"])
	}
	if pool := block["llp"].(map[string]any); pool["r30_ann"] != nil {
		t.Fatalf("r30_ann = %v, want null", pool["r30_ann"])
	}
	if _, present := block["live_proxy"]; present {
		t.Fatalf("live_proxy present in %s", out)
	}
}

func TestLLPMonitorRejectsMalformedDocuments(t *testing.T) {
	for name, payload := range map[string]string{
		"no t":      `{"level":"OK"}`,
		"blank t":   `{"t":" ","level":"OK"}`,
		"epoch t":   `{"t":1791916499,"level":"OK"}`,
		"garbage t": `{"t":"yesterday","level":"OK"}`,
		"not json":  `{"t":`,
	} {
		if _, err := decodeLLPMonitorPayload([]byte(payload)); err == nil {
			t.Fatalf("%s: decoded without error", name)
		}
	}
}

// Only the llp-monitor service is decoded as the monitor's document; every
// other target keeps the trading contract, and the monitor's decoder is not
// applied to a bot that happens to publish a `t`.
func TestLLPMonitorDecoderIsKeyedOnService(t *testing.T) {
	payload, err := os.ReadFile(llpMonitorFixturePath)
	if err != nil {
		t.Fatalf("read llp-monitor fixture: %v", err)
	}
	status, err := decodeTargetPayload(TargetConfig{Service: "debot-xvenue-hedge-holder"}, payload)
	if err != nil {
		t.Fatalf("decode as trading status: %v", err)
	}
	if status.LLPMonitor != nil || status.TS != 0 {
		t.Fatalf("trading target decoded as llp monitor: %+v", status)
	}
	trading, err := decodeTargetPayload(TargetConfig{Service: "debot-xvenue-hedge-holder"}, []byte(`{"ts":5,"pnl_total":1}`))
	if err != nil || trading.TS != 5 || trading.PnlTotal != 1 {
		t.Fatalf("trading decode = %+v / %v", trading, err)
	}
}

// The commented example in config.example.yaml is what the host config gets
// pasted from, and a dashboard merge restarts the service against that host
// config: uncommented, it must pass the same normalizeConfig the binary runs
// at startup.
func TestLLPMonitorConfigExampleValidates(t *testing.T) {
	data, err := os.ReadFile("config.example.yaml")
	if err != nil {
		t.Fatalf("read config.example.yaml: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	var block []string
	for i, line := range lines {
		if line != "#  - name: LLP monitor" {
			continue
		}
		block = append(block, strings.TrimPrefix(line, "#"))
		for _, next := range lines[i+1:] {
			if !strings.HasPrefix(next, "#    ") {
				break
			}
			block = append(block, strings.TrimPrefix(next, "#"))
		}
		break
	}
	if len(block) == 0 {
		t.Fatal("no LLP monitor example in config.example.yaml")
	}
	var cfg Config
	doc := "region: eu-central-1\ntargets:\n" + strings.Join(block, "\n") + "\n"
	if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatalf("parse uncommented example: %v\n%s", err, doc)
	}
	if err := normalizeConfig(&cfg); err != nil {
		t.Fatalf("normalizeConfig: %v\n%s", err, doc)
	}
	target := cfg.Targets[0]
	if target.Service != llpMonitorService || target.Bucket != BucketSubsidy || target.StaleAfterSecs != 7200 {
		t.Fatalf("target = %+v", target)
	}
	if target.S3Key != "debot/status/llp-monitor/status.json" || target.S3Region != "eu-central-1" || target.Region != "ap-northeast-1" {
		t.Fatalf("target S3 = %+v", target)
	}
	// Without `bucket:` the service is not in the taxonomy yet, so it
	// renders unclassified rather than failing startup.
	cfg.Targets[0].Bucket = ""
	if err := normalizeConfig(&cfg); err != nil || cfg.Targets[0].Bucket != BucketUnclassified {
		t.Fatalf("without bucket: %v / %q", err, cfg.Targets[0].Bucket)
	}
}

const llpMonitorV2FixturePath = "tests/fixtures/llp-monitor-status-v2.json"

// v2 (bot-strategy PR #1182) adds the carry since entry — funding
// received, staking rewards, the monitor's own total — and the hourly
// pnl_history series. They pass through; v1 must decode exactly as before.
const llpMonitorV3FixturePath = "tests/fixtures/llp-monitor-status-v3.json"

// v3 (bot-strategy#1179, 2026-10-22) adds the package's own return; v2 and
// v1 documents decode without it, and leave it null for the card.
func TestLLPMonitorV3PackageReturnPassThrough(t *testing.T) {
	payload, err := os.ReadFile(llpMonitorV3FixturePath)
	if err != nil {
		t.Fatalf("read v3 fixture: %v", err)
	}
	status, err := decodeLLPMonitorPayload(payload)
	if err != nil {
		t.Fatalf("decode v3: %v", err)
	}
	p := status.LLPMonitor.Position
	if p == nil || p.ReturnAnn == nil || *p.ReturnAnn != -0.015073 || p.ReturnSinceEntry == nil || p.DaysSinceEntry == nil || *p.DaysSinceEntry != 8.93 ||
		p.CapitalUsd == nil || *p.CapitalUsd != 4083.9 || p.CapitalTwaUsd == nil || p.SpotUsdc == nil || p.OpenedAt == nil || *p.OpenedAt != "2026-10-13T18:30Z" {
		t.Fatalf("v3 position = %+v", p)
	}
	out, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, want := range []string{`"return_ann":-0.015073`, `"capital_twa_usd":4083.9`, `"opened_at":"2026-10-13T18:30Z"`, `"days_since_entry":8.93`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("re-encoded v3 lacks %s: %s", want, out)
		}
	}
	v2, err := os.ReadFile(llpMonitorV2FixturePath)
	if err != nil {
		t.Fatalf("read v2 fixture: %v", err)
	}
	old, err := decodeLLPMonitorPayload(v2)
	if err != nil {
		t.Fatalf("decode v2: %v", err)
	}
	if op := old.LLPMonitor.Position; op.ReturnAnn != nil || op.CapitalUsd != nil || op.OpenedAt != nil {
		t.Fatalf("v2 grew v3 fields: %+v", op)
	}
}

func TestLLPMonitorV2CarryAndHistoryPassThrough(t *testing.T) {
	payload, err := os.ReadFile(llpMonitorV2FixturePath)
	if err != nil {
		t.Fatalf("read v2 fixture: %v", err)
	}
	status, err := decodeLLPMonitorPayload(payload)
	if err != nil {
		t.Fatalf("decode v2: %v", err)
	}
	p := status.LLPMonitor.Position
	if p == nil || p.FundingReceived == nil || *p.FundingReceived != 0.010596 ||
		p.TotalPnlUsd == nil || p.StakingRewardUsd == nil || p.StakingRewardLIT == nil || p.StakedLITNow == nil {
		t.Fatalf("v2 position = %+v", p)
	}
	out, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var raw struct {
		LLPMonitor struct {
			PnlHistory struct {
				Fields []string    `json:"fields"`
				Rows   [][]float64 `json:"rows"`
			} `json:"pnl_history"`
			Position map[string]any `json:"position"`
		} `json:"llp_monitor"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	h := raw.LLPMonitor.PnlHistory
	if strings.Join(h.Fields, ",") != "t,total,llp,hedge,funding,staking" || len(h.Rows) != 1 || h.Rows[0][0] != 1791920564 || h.Rows[0][1] != -1.5061 {
		t.Fatalf("pnl_history = %+v", h)
	}
	if raw.LLPMonitor.Position["total_pnl_usd"] != -1.506070096988528 {
		t.Fatalf("total_pnl_usd = %v", raw.LLPMonitor.Position["total_pnl_usd"])
	}

	// v1: the new figures are null, pnl_history is absent from the API.
	v1, err := os.ReadFile(llpMonitorFixturePath)
	if err != nil {
		t.Fatalf("read v1 fixture: %v", err)
	}
	old, err := decodeLLPMonitorPayload(v1)
	if err != nil {
		t.Fatalf("decode v1: %v", err)
	}
	op := old.LLPMonitor.Position
	if op.FundingReceived != nil || op.TotalPnlUsd != nil || op.StakingRewardUsd != nil || old.LLPMonitor.PnlHistory != nil {
		t.Fatalf("v1 grew v2 fields: %+v", op)
	}
	oldOut, _ := json.Marshal(old)
	if strings.Contains(string(oldOut), "pnl_history") {
		t.Fatalf("v1 re-encoded with pnl_history: %s", oldOut)
	}
	// An explicit null history is the same as none.
	nullHist, err := decodeLLPMonitorPayload([]byte(`{"t":"2026-10-13T20:07:00Z","level":"OK","pnl_history":null}`))
	if err != nil || nullHist.LLPMonitor.PnlHistory != nil {
		t.Fatalf("null pnl_history = %s / %v", nullHist.LLPMonitor.PnlHistory, err)
	}
}
