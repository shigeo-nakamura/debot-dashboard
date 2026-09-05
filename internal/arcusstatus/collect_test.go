package arcusstatus

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 9, 5, 15, 20, 0, 0, time.UTC)

func testUnits() Units {
	return Units{Service: map[string]string{"LoadState": "loaded", "ActiveState": "inactive", "Result": "success", "ExecMainCode": "1", "ExecMainStatus": "0", "ExecMainStartTimestamp": "Sat 2026-09-05 15:17:00 UTC"}, Timer: map[string]string{"LoadState": "loaded", "ActiveState": "active", "UnitFileState": "enabled"}}
}
func put(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func envelope(t *testing.T, sequence int, code string) string {
	t.Helper()
	e := fmt.Sprintf(`{"sequence":%d,"observed_at":"2026-09-05T15:17:28Z","pair":"SPY/QQQ","mode":"live","z_score":0.3,"decision":{"action":"observe","hold":{"code":%q,"detail":"secret-error-detail"}}}`, sequence, code)
	b, err := json.Marshal(eventEnvelope{1, fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(e))), e})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	put(t, dir, "runtime_state.json", `{"schema_version":1,"config":{"mode":"live","pair":{"sell_symbol":"SPY","buy_symbol":"QQQ"},"daily_loss_limit_usd":"20","cumulative_loss_limit_usd":"100","signer":"secret-config"},"state":{"sequence":1915,"inventory":{"token_a":"2","token_b":"1"},"regime":"neutral","last_token_a_reference_price_usd":"100","last_token_b_reference_price_usd":"200","last_observation_at":"2026-09-05T15:17:28Z","initial_equity_usd":"800","initial_baseline_inventory":{"token_a":"2","token_b":"2"},"daily_baseline_day":"2026-09-05","daily_baseline_inventory":{"token_a":"2","token_b":"2"},"risk_halt":null,"last_live_execution_idempotency_key":"secret-idempotency"}}`)
	put(t, dir, "config.yaml", "executor:\n  max_swaps_per_utc_day: 10\n  signer_private_key: secret-signer\n")
	put(t, dir, "ledger.json", `{"schema_version":2,"history":[{"phase":"reconciled","updated_at":"2026-09-04T23:00:00Z"},{"phase":"rejected","updated_at":"2026-09-05T01:00:00Z"},{"phase":"reconciled","updated_at":"2026-09-05T13:00:00Z","post_balances":{"observed_at":"2026-09-05T12:59:58Z","gas_balance_wei":"1234000000000000"}}],"active":{"phase":"reconciled","updated_at":"2026-09-05T15:00:00Z","payload_hash":"secret-payload","post_balances":{"observed_at":"2026-09-05T14:59:58Z","gas_balance_wei":"1000000000000000"}}}`)
	put(t, dir, "live-tick-events/2026-09-05.jsonl", envelope(t, 1915, "no_signal")+"\n")
	return dir
}
func collect(t *testing.T, dir string, u Units) Payload {
	t.Helper()
	return Collect(dir, filepath.Join(dir, "config.yaml"), u, testNow)
}
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			m[path] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func TestCollectHealthyNoSignalIsReadOnlyAndAllowlisted(t *testing.T) {
	dir := fixture(t)
	before := snapshot(t, dir)
	p := collect(t, dir, testUnits())
	s := p.Arcus
	if !s.Healthy || s.ServiceStatus(testNow, 1920) != "active" || s.TickOutcome != "success" || s.HoldCode != "no_signal" {
		t.Fatalf("unexpected status: %+v", s)
	}
	if *s.EquityUSD != 400 || *s.DailyLossUSD != 200 || *s.CumulativeLossUSD != 200 || *s.InventoryDrawdownUSD != 200 {
		t.Fatalf("risk values: %+v", s)
	}
	if *s.DailyBudgetUsed != 2 || *s.MaxSwapsPerDay != 10 {
		t.Fatal("budget must count archived rejections, not active attempts")
	}
	if *s.GasBalanceETH != 0.001 || s.GasObservedAt != "2026-09-05T14:59:58Z" {
		t.Fatal("gas must use most recent reconciled snapshot")
	}
	if p.TS != time.Date(2026, 9, 5, 15, 17, 0, 0, time.UTC).Unix() {
		t.Fatal("heartbeat must not replace bot clock")
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "secret-") {
		t.Fatal("private source fields leaked")
	}
	after := snapshot(t, dir)
	if len(before) != len(after) {
		t.Fatal("exporter created runtime files")
	}
	for path, b := range before {
		if after[path] != b {
			t.Fatalf("exporter modified %s", path)
		}
	}
}
func TestCollectFailedTickWithFreshCheckpointAndPendingDecision(t *testing.T) {
	dir := fixture(t)
	put(t, dir, "live-tick-events/2026-09-05.jsonl", envelope(t, 1914, "no_signal")+"\n")
	put(t, dir, "live-tick-event-pending.json", envelope(t, 1915, "route_unavailable"))
	u := testUnits()
	u.Service["ActiveState"] = "failed"
	u.Service["Result"] = "exit-code"
	u.Service["ExecMainStatus"] = "1"
	s := collect(t, dir, u).Arcus
	if s.Healthy || s.TickOutcome != "failed" || !s.DecisionPending || s.HoldCode != "route_unavailable" || s.ServiceStatus(testNow, 1920) != "degraded" {
		t.Fatalf("fresh checkpoint hid failed tick: %+v", s)
	}
}
func TestCollectMissingCorruptAndUnmatchedSources(t *testing.T) {
	for _, kind := range []string{"missing_checkpoint", "missing_ledger", "missing_config", "old_event", "partial_event", "bad_hash", "wrong_pair", "disabled_timer", "unknown_systemd", "pending_execution"} {
		t.Run(kind, func(t *testing.T) {
			dir := fixture(t)
			u := testUnits()
			switch kind {
			case "missing_checkpoint":
				os.Remove(filepath.Join(dir, "runtime_state.json"))
			case "missing_ledger":
				os.Remove(filepath.Join(dir, "ledger.json"))
			case "missing_config":
				os.Remove(filepath.Join(dir, "config.yaml"))
			case "old_event":
				put(t, dir, "live-tick-events/2026-09-05.jsonl", envelope(t, 1914, "no_signal")+"\n")
			case "partial_event":
				put(t, dir, "live-tick-events/2026-09-05.jsonl", envelope(t, 1915, "no_signal"))
			case "bad_hash":
				put(t, dir, "live-tick-events/2026-09-05.jsonl", strings.Replace(envelope(t, 1915, "no_signal"), "no_signal", "risk_halted", 1)+"\n")
			case "wrong_pair":
				put(t, dir, "live-tick-events/2026-09-05.jsonl", strings.Replace(envelope(t, 1915, "no_signal"), "SPY/QQQ", "BTC/ETH", 1)+"\n")
			case "disabled_timer":
				u.Timer["UnitFileState"] = "disabled"
			case "unknown_systemd":
				u.Error = true
			case "pending_execution":
				put(t, dir, "ledger.json", `{"schema_version":2,"history":[],"active":{"phase":"submitted","updated_at":"2026-09-05T15:17:28Z"}}`)
			}
			s := collect(t, dir, u).Arcus
			if s.Healthy || s.ServiceStatus(testNow, 1920) == "active" {
				t.Fatalf("%s reported healthy", kind)
			}
			if strings.Contains(kind, "event") || kind == "bad_hash" || kind == "wrong_pair" {
				if s.Decision != "" {
					t.Fatal("unmatched decision must be unknown")
				}
			}
			if kind == "missing_ledger" && (s.DailyBudgetUsed != nil || s.GasBalanceETH != nil) {
				t.Fatal("missing ledger became zero")
			}
		})
	}
}
func TestIndependentFreshnessClocks(t *testing.T) {
	base := Status{Healthy: true, ExportedAt: testNow.Format(time.RFC3339), LastTickAt: testNow.Add(-15 * time.Minute).Format(time.RFC3339), LastObservationAt: testNow.Add(-15 * time.Minute).Format(time.RFC3339)}
	if got := base.ServiceStatus(testNow, 1920); got != "active" {
		t.Fatal(got)
	}
	for _, clock := range []string{"heartbeat", "tick", "observation", "future", "missing"} {
		t.Run(clock, func(t *testing.T) {
			s := base
			want := "stale"
			switch clock {
			case "heartbeat":
				s.ExportedAt = testNow.Add(-181 * time.Second).Format(time.RFC3339)
			case "tick":
				s.LastTickAt = testNow.Add(-1921 * time.Second).Format(time.RFC3339)
			case "observation":
				s.LastObservationAt = testNow.Add(-1921 * time.Second).Format(time.RFC3339)
			case "future":
				s.LastTickAt = testNow.Add(time.Minute).Format(time.RFC3339)
				want = "unknown"
			case "missing":
				s.LastObservationAt = ""
				want = "unknown"
			}
			if got := s.ServiceStatus(testNow, 1920); got != want {
				t.Fatalf("got %s want %s", got, want)
			}
		})
	}
}
func TestRiskHaltAndUnknownBaselines(t *testing.T) {
	dir := fixture(t)
	path := filepath.Join(dir, "runtime_state.json")
	b, _ := os.ReadFile(path)
	b = []byte(strings.Replace(string(b), `"risk_halt":null`, `"risk_halt":{"kind":"daily_loss","engaged_at":"2026-09-05T15:17:28Z","loss_usd":"21","limit_usd":"20"}`, 1))
	b = []byte(strings.Replace(string(b), `"daily_baseline_inventory":{"token_a":"2","token_b":"2"}`, `"daily_baseline_inventory":{}`, 1))
	put(t, dir, "runtime_state.json", string(b))
	s := collect(t, dir, testUnits()).Arcus
	if s.RiskHalt == nil || *s.RiskHalt.LossUSD != 21 || s.Healthy || s.DailyLossUSD != nil {
		t.Fatalf("halt/unknown baseline: %+v", s)
	}
}

func TestMissingModeIsNotClaimedDryRun(t *testing.T) {
	dir := fixture(t)
	path := filepath.Join(dir, "runtime_state.json")
	b, _ := os.ReadFile(path)
	put(t, dir, "runtime_state.json", strings.Replace(string(b), `"mode":"live"`, `"mode":""`, 1))
	p := collect(t, dir, testUnits())
	if p.DryRun || p.Arcus.Healthy {
		t.Fatal("unknown mode must not imply safe dry-run or healthy status")
	}
}

func TestMissingOrMalformedRiskLimitsDegrade(t *testing.T) {
	for _, field := range []string{"daily_loss_limit_usd", "cumulative_loss_limit_usd"} {
		for _, replacement := range []string{`null`, `"invalid"`} {
			t.Run(field+replacement, func(t *testing.T) {
				dir := fixture(t)
				path := filepath.Join(dir, "runtime_state.json")
				b, _ := os.ReadFile(path)
				var raw map[string]json.RawMessage
				json.Unmarshal(b, &raw)
				var config map[string]json.RawMessage
				json.Unmarshal(raw["config"], &config)
				config[field] = json.RawMessage(replacement)
				raw["config"], _ = json.Marshal(config)
				b, _ = json.Marshal(raw)
				put(t, dir, "runtime_state.json", string(b))
				s := collect(t, dir, testUnits()).Arcus
				if s.Healthy || s.ServiceStatus(testNow, 1920) == "active" {
					t.Fatal("unavailable risk limit reported healthy")
				}
			})
		}
	}
}
